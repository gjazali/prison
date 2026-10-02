package broker

import (
	"errors"
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

// mergeCredentials keeps credentials in memory only. An empty value
// deletes the entry.
func (broker *Broker) mergeCredentials(values map[string]string) {
	broker.credentialsMutex.Lock()
	defer broker.credentialsMutex.Unlock()
	for name, value := range values {
		if value == "" {
			delete(broker.credentials, name)
			continue
		}
		broker.credentials[name] = value
	}
}

func (broker *Broker) credentialVariables() []string {
	broker.credentialsMutex.Lock()
	defer broker.credentialsMutex.Unlock()
	names := make([]string, 0, len(broker.credentials))
	for name := range broker.credentials {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (broker *Broker) chooseCredential(specs []state.CredentialSpec) (header, value string, ok bool) {
	broker.credentialsMutex.Lock()
	defer broker.credentialsMutex.Unlock()
	for _, spec := range specs {
		held := broker.credentials[spec.Variable]
		if held == "" {
			continue
		}
		header = spec.Header
		if header == "" {
			header = "Authorization"
		}
		return header, spec.Prefix + held, true
	}
	return "", "", false
}
