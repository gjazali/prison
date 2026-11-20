package broker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"prison/internal/broker/ca"
	"prison/internal/broker/control"
	"prison/internal/broker/protocol"
	"prison/internal/policy"
	"prison/internal/state"
	"prison/internal/vault"
)

func (broker *Broker) unlockVault(passphrase string) error {
	opened, err := vault.Open(broker.root.VaultFile(), passphrase)
	if err != nil {
		return err
	}
	broker.vaultMutex.Lock()
	previous := broker.vault
	broker.vault = opened
	broker.authorities = ca.New(opened, broker.now)
	broker.vaultMutex.Unlock()
	if previous != nil {
		previous.Lock()
	}
	return nil
}

func (broker *Broker) lockVault() {
	broker.vaultMutex.Lock()
	opened := broker.vault
	broker.vault = nil
	broker.authorities = nil
	broker.vaultMutex.Unlock()
	if broker.oauth != nil {
		broker.oauth.clear()
	}
	if opened != nil {
		opened.Lock()
	}
}

func (broker *Broker) currentVault() *vault.Vault {
	broker.vaultMutex.Lock()
	defer broker.vaultMutex.Unlock()
	return broker.vault
}

func (broker *Broker) currentAuthorities() *ca.Authorities {
	broker.vaultMutex.Lock()
	defer broker.vaultMutex.Unlock()
	return broker.authorities
}

func (broker *Broker) vaultState() string {
	if broker.currentVault() != nil {
		return control.VaultUnlocked
	}
	if !vault.Exists(broker.root.VaultFile()) {
		return control.VaultAbsent
	}
	return control.VaultLocked
}

func isWrongPassphrase(err error) bool {
	return errors.Is(err, vault.ErrWrongPassphrase)
}

func (broker *Broker) grantedSecrets(snapshot *projectSnapshot) []*vault.Secret {
	opened := broker.currentVault()
	if opened == nil || len(snapshot.granted) == 0 {
		return nil
	}
	var granted []*vault.Secret
	for _, secret := range opened.Secrets() {
		if snapshot.granted[secret.Name] {
			granted = append(granted, secret)
		}
	}
	return granted
}

// grantedSecret returns nil for a locked vault and for an unknown name
// alike, so that a caller cannot tell the two apart.
func (broker *Broker) grantedSecret(snapshot *projectSnapshot, name string) *vault.Secret {
	if name == "" || !snapshot.granted[name] {
		return nil
	}
	opened := broker.currentVault()
	if opened == nil {
		return nil
	}
	secret, found := opened.Secret(name)
	if !found {
		return nil
	}
	return secret
}

func (broker *Broker) interceptRoutes(snapshot *projectSnapshot) []*vault.Secret {
	var routes []*vault.Secret
	for _, secret := range broker.grantedSecrets(snapshot) {
		if secret.Mode == vault.ModeRoute && secret.Route != nil && secret.Route.Intercept {
			routes = append(routes, secret)
		}
	}
	return routes
}

func (broker *Broker) interceptRouteFor(snapshot *projectSnapshot, host string, port int) *vault.Secret {
	for _, secret := range broker.interceptRoutes(snapshot) {
		upstreamHost, upstreamPort := vault.SplitUpstream(secret.Route.Upstream)
		if upstreamHost == host && upstreamPort == port {
			return secret
		}
	}
	return nil
}

type inmateInterceptTarget struct {
	inmate   string
	host     string
	port     int
	wildcard bool
}

func (target inmateInterceptTarget) matches(host string, port int) bool {
	if target.port != port {
		return false
	}
	if target.wildcard {
		return host == target.host || strings.HasSuffix(host, "."+target.host)
	}
	return host == target.host
}

func (target inmateInterceptTarget) patternText() string {
	host := target.host
	if target.wildcard {
		host = "*." + host
	}
	return host + ":" + strconv.Itoa(target.port)
}

func parseInmateInterceptEntry(inmate, entry string) (inmateInterceptTarget, bool) {
	trimmed := strings.TrimSpace(entry)
	if trimmed == "" {
		return inmateInterceptTarget{}, false
	}
	wildcard := false
	if strings.HasPrefix(trimmed, "*.") {
		wildcard = true
		trimmed = trimmed[2:]
	}
	host, port := vault.SplitUpstream(trimmed)
	if host == "" {
		return inmateInterceptTarget{}, false
	}
	return inmateInterceptTarget{
		inmate:   inmate,
		host:     host,
		port:     port,
		wildcard: wildcard,
	}, true
}

func (broker *Broker) inmateInterceptTargets(snapshot *projectSnapshot) []inmateInterceptTarget {
	if snapshot == nil || snapshot.profile == nil {
		return nil
	}
	var targets []inmateInterceptTarget
	for _, route := range snapshot.profile.Inmates {
		if !route.Intercept {
			continue
		}
		if route.Upstream != "" {
			if target, ok := parseInmateInterceptEntry(route.Name, route.Upstream); ok {
				targets = append(targets, target)
			}
		}
		for _, entry := range route.InterceptHosts {
			if target, ok := parseInmateInterceptEntry(route.Name, entry); ok {
				targets = append(targets, target)
			}
		}
	}
	return targets
}

func (broker *Broker) interceptInmateFor(snapshot *projectSnapshot,
	host string, port int) (inmate, authorityHost string, ok bool) {
	for _, target := range broker.inmateInterceptTargets(snapshot) {
		if target.matches(host, port) {
			return target.inmate, target.host, true
		}
	}
	return "", "", false
}

func (broker *Broker) interceptHosts(snapshot *projectSnapshot) []string {
	seen := map[string]bool{}
	var hosts []string
	add := func(host string) {
		if host != "" && !seen[host] {
			seen[host] = true
			hosts = append(hosts, host)
		}
	}
	for _, secret := range broker.interceptRoutes(snapshot) {
		host, _ := vault.SplitUpstream(secret.Route.Upstream)
		add(host)
	}
	for _, target := range broker.inmateInterceptTargets(snapshot) {
		add(target.host)
	}
	sort.Strings(hosts)
	return hosts
}

func (broker *Broker) allowListFor(snapshot *projectSnapshot) *policy.AllowList {
	var intercepts []policy.Pattern
	for _, secret := range broker.interceptRoutes(snapshot) {
		host, port := vault.SplitUpstream(secret.Route.Upstream)
		pattern, err := policy.ParsePattern(host + ":" + strconv.Itoa(port))
		if err == nil {
			intercepts = append(intercepts, pattern)
		}
	}
	for _, target := range broker.inmateInterceptTargets(snapshot) {
		pattern, err := policy.ParsePattern(target.patternText())
		if err == nil {
			intercepts = append(intercepts, pattern)
		}
	}
	return policy.NewAllowList(broker.floor.Load().patterns,
		snapshot.profileEgress, snapshot.egress, intercepts)
}

// environmentLines gives expose secrets priority over route secrets
// when two secrets set the same variable.
func (broker *Broker) environmentLines(snapshot *projectSnapshot) []string {
	assignments := map[string]string{}
	secrets := broker.grantedSecrets(snapshot)
	for _, secret := range secrets {
		if secret.Mode == vault.ModeRoute && secret.Route != nil &&
			secret.Route.BaseURLVariable != "" {
			assignments[secret.Route.BaseURLVariable] =
				"http://" + protocol.RouteHost(secret.Name)
		}
	}
	for _, secret := range secrets {
		if secret.Mode == vault.ModeRoute && secret.Route != nil &&
			secret.Route.TokenVariable != "" {
			assignments[secret.Route.TokenVariable] = secret.Route.TokenPlaceholder
		}
	}
	for _, secret := range secrets {
		if secret.Mode == vault.ModeExpose && secret.Expose != nil &&
			secret.Expose.Variable != "" {
			assignments[secret.Expose.Variable] = secret.Value
		}
	}
	names := make([]string, 0, len(assignments))
	for name := range assignments {
		names = append(names, name)
	}
	sort.Strings(names)
	lines := make([]string, 0, len(names))
	for _, name := range names {
		lines = append(lines, name+"="+assignments[name])
	}
	return lines
}

func upstreamAddress(upstream string) string {
	host, port := vault.SplitUpstream(upstream)
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func (broker *Broker) inmateCredentialSummaries() []control.InmateCredential {
	opened := broker.currentVault()
	if opened == nil {
		return nil
	}
	stored := opened.InmateCredentials()
	if len(stored) == 0 {
		return nil
	}
	summaries := make([]control.InmateCredential, 0, len(stored))
	for _, credential := range stored {
		summaries = append(summaries, control.InmateCredential{
			Inmate:   credential.Inmate,
			Profile:  credential.Profile,
			Variable: credential.Variable,
		})
	}
	return summaries
}

// A named profile does not fall back to the default profile when missing.
func (broker *Broker) chooseInmateCredential(ctx context.Context,
	snapshot *projectSnapshot, inmate string,
	specs []state.CredentialSpec) (header, value, refusal string, ok bool) {
	profile := vault.DefaultInmateProfile
	overridden := false
	if snapshot != nil {
		if selected := snapshot.inmateProfiles[inmate]; selected != "" &&
			selected != vault.DefaultInmateProfile {
			profile = selected
			overridden = true
		}
	}
	opened := broker.currentVault()
	if opened == nil {
		if !vault.Exists(broker.root.VaultFile()) {
			return "", "", fmt.Sprintf(
				"no credential for the %s inmate. Run `prison secret init`, "+
					"then `prison inmate auth set %s`", inmate, inmate), false
		}
		return "", "", "the vault is locked. Run `prison secret unlock`", false
	}
	for _, spec := range specs {
		credential, found := opened.InmateCredential(inmate, profile, spec.Variable)
		if !found || credential.Value == "" {
			continue
		}
		header = spec.Header
		if header == "" {
			header = "Authorization"
		}
		secretValue := credential.Value
		tokenURL := spec.TokenURL
		clientID := spec.ClientID
		clientSecret := ""
		if credential.OAuth != nil {
			if credential.OAuth.TokenURL != "" {
				tokenURL = credential.OAuth.TokenURL
			}
			if credential.OAuth.ClientID != "" {
				clientID = credential.OAuth.ClientID
			}
			clientSecret = credential.OAuth.ClientSecret
		}
		if tokenURL != "" {
			refreshed, rotated, err := broker.oauth.accessToken(ctx, oauthRequest{
				inmate:       inmate,
				profile:      profile,
				variable:     spec.Variable,
				tokenURL:     tokenURL,
				clientID:     clientID,
				clientSecret: clientSecret,
				refreshToken: credential.Value,
			})
			if err != nil {
				return "", "", fmt.Sprintf(
					"cannot refresh OAuth token for the %s inmate: %v", inmate, err), false
			}
			if rotated != "" {
				credential.Value = rotated
				if _, err := opened.PutInmateCredential(credential); err != nil {
					broker.logger.Printf("inmate %s: cannot save rotated refresh token: %v",
						inmate, err)
				}
			}
			secretValue = refreshed
		}
		return header, spec.Prefix + secretValue, "", true
	}
	if overridden {
		return "", "", fmt.Sprintf(
			"no credential for the %s inmate in profile %q. "+
				"Run `prison inmate auth set %s --profile %s`",
			inmate, profile, inmate, profile), false
	}
	return "", "", fmt.Sprintf(
		"no credential for the %s inmate. Run `prison inmate auth set %s`",
		inmate, inmate), false
}
