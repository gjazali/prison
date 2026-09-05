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

// unlockVault opens the vault with the given passphrase and installs
// it. Replaces and locks any previously open vault. Returns vault
// errors such as wrong passphrase or missing file.
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

// lockVault forgets the vault key and drops the authorities. Safe to
// call when nothing is unlocked.
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

// currentVault returns the unlocked vault, or nil if locked.
func (broker *Broker) currentVault() *vault.Vault {
	broker.vaultMutex.Lock()
	defer broker.vaultMutex.Unlock()
	return broker.vault
}

// currentAuthorities returns the CA store, or nil if the vault is
// locked.
func (broker *Broker) currentAuthorities() *ca.Authorities {
	broker.vaultMutex.Lock()
	defer broker.vaultMutex.Unlock()
	return broker.authorities
}

// vaultState returns the vault status string for GET /status.
func (broker *Broker) vaultState() string {
	if broker.currentVault() != nil {
		return control.VaultUnlocked
	}
	if !vault.Exists(broker.root.VaultFile()) {
		return control.VaultAbsent
	}
	return control.VaultLocked
}

// isWrongPassphrase returns true if err is a wrong-passphrase error.
func isWrongPassphrase(err error) bool {
	return errors.Is(err, vault.ErrWrongPassphrase)
}

// grantedSecrets returns the vault secrets granted to the project,
// sorted by name. Returns nil while the vault is locked.
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

// grantedSecret returns the named secret if the project has a grant
// and the vault is unlocked. Returns nil otherwise, so a caller
// cannot tell a locked vault from an unknown name.
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

// interceptRoutes returns the project's granted route secrets that
// use TLS interception, sorted by name.
func (broker *Broker) interceptRoutes(snapshot *projectSnapshot) []*vault.Secret {
	var routes []*vault.Secret
	for _, secret := range broker.grantedSecrets(snapshot) {
		if secret.Mode == vault.ModeRoute && secret.Route != nil && secret.Route.Intercept {
			routes = append(routes, secret)
		}
	}
	return routes
}

// interceptRouteFor returns the intercepting route matching the given
// host and port, or nil if none matches.
func (broker *Broker) interceptRouteFor(snapshot *projectSnapshot, host string, port int) *vault.Secret {
	for _, secret := range broker.interceptRoutes(snapshot) {
		upstreamHost, upstreamPort := vault.SplitUpstream(secret.Route.Upstream)
		if upstreamHost == host && upstreamPort == port {
			return secret
		}
	}
	return nil
}

// interceptHosts returns the unique upstream hosts from the project's
// intercepting routes, sorted.
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

// allowListFor builds the full allowlist for a project. Combines the
// floor, profile egress, project egress, and intercept entries.
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

// environmentLines returns NAME=value strings from the project's
// granted secrets. Route secrets contribute base URLs and token
// placeholders. Expose secrets contribute real values. Expose wins
// on name conflicts. Returns nil while the vault is locked.
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

// upstreamAddress formats an upstream as host:port with the default
// port made explicit.
func upstreamAddress(upstream string) string {
	host, port := vault.SplitUpstream(upstream)
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// mergeCredentials updates in-memory credentials with the given map.
// Non-empty values are set, empty values are deleted. Nothing is
// written to disk.
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

// credentialVariables returns the sorted names of held credentials.
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

// chooseCredential picks the first spec whose variable is held by the
// broker. Returns the header name, value, and true. Returns false if
// none match. An empty header name defaults to Authorization.
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
