package broker

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"

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

func (broker *Broker) interceptHosts(snapshot *projectSnapshot) []string {
	seen := map[string]bool{}
	var hosts []string
	for _, secret := range broker.interceptRoutes(snapshot) {
		host, _ := vault.SplitUpstream(secret.Route.Upstream)
		if host != "" && !seen[host] {
			seen[host] = true
			hosts = append(hosts, host)
		}
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
func (broker *Broker) chooseInmateCredential(snapshot *projectSnapshot,
	inmate string, specs []state.CredentialSpec) (header, value, refusal string, ok bool) {
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
		return header, spec.Prefix + credential.Value, "", true
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
