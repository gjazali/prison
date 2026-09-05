package session

import (
	"context"
	"slices"
	"testing"

	"prison/internal/cage"
	"prison/internal/cage/fake"
	"prison/internal/config"
	"prison/internal/state"
)

// newHostSetupSession returns a session with a fake cage, domain,
// broker port, and a temporary state root.
func newHostSetupSession(t *testing.T, fakeCage *fake.Cage) *Session {
	t.Helper()
	return &Session{Environment: &Environment{
		Cage: fakeCage,
		Overrides: &config.Overrides{
			Domain:     "prison",
			BrokerPort: 8787,
		},
		Root: &state.Root{Path: t.TempDir()},
	}}
}

// calledMethod reports whether calls contains a call to method.
func calledMethod(calls []string, method string) bool {
	return slices.ContainsFunc(calls, func(line string) bool {
		return line == method || len(line) > len(method) &&
			line[:len(method)+1] == method+" "
	})
}

// TestEnsureHostDNSRegistersADomainThatIsNotThere checks that a
// missing domain is registered.
func TestEnsureHostDNSRegistersADomainThatIsNotThere(t *testing.T) {
	fakeCage := fake.New()
	session := newHostSetupSession(t, fakeCage)
	session.ensureHostDNS(context.Background())
	if !fakeCage.Domains["prison"] {
		t.Error("the domain was not registered")
	}
}

// TestEnsureHostDNSLeavesARegisteredDomainAlone checks that an
// existing domain is not registered again.
func TestEnsureHostDNSLeavesARegisteredDomainAlone(t *testing.T) {
	fakeCage := fake.New()
	fakeCage.Domains["prison"] = true
	session := newHostSetupSession(t, fakeCage)
	session.ensureHostDNS(context.Background())
	if calledMethod(fakeCage.Calls, "DNS.Register") {
		t.Errorf("a registered domain was registered again: %v",
			fakeCage.Calls)
	}
}

// TestEnsureHostDNSSkipsACageThatNamesNothing checks that a cage
// without DNS domain capability is not asked about DNS.
func TestEnsureHostDNSSkipsACageThatNamesNothing(t *testing.T) {
	fakeCage := fake.New()
	fakeCage.DeclaredCapabilities.DNSDomain = false
	session := newHostSetupSession(t, fakeCage)
	session.ensureHostDNS(context.Background())
	if len(fakeCage.Calls) != 0 {
		t.Errorf("a cage that names no boxes was asked about DNS: %v",
			fakeCage.Calls)
	}
}

// TestEnsureHostRouteAddsAMissingRoute checks that a missing route
// to the gateway is added.
func TestEnsureHostRouteAddsAMissingRoute(t *testing.T) {
	fakeCage := fake.New()
	session := newHostSetupSession(t, fakeCage)
	session.ensureHostRoute(context.Background(),
		cage.NetworkInfo{Gateway: "192.168.128.1"})
	if !fakeCage.RouteIsInstalled {
		t.Error("the route was not added")
	}
}

// TestEnsureHostRouteLeavesAWorkingRouteAlone checks that an
// existing route is not added again.
func TestEnsureHostRouteLeavesAWorkingRouteAlone(t *testing.T) {
	fakeCage := fake.New()
	fakeCage.RouteIsInstalled = true
	session := newHostSetupSession(t, fakeCage)
	session.ensureHostRoute(context.Background(),
		cage.NetworkInfo{Gateway: "192.168.128.1"})
	if calledMethod(fakeCage.Calls, "Route.Install") {
		t.Errorf("a working route was added again: %v", fakeCage.Calls)
	}
}

// TestEnsureHostRouteWaitsForABridge checks that a network with no
// gateway is left alone.
func TestEnsureHostRouteWaitsForABridge(t *testing.T) {
	fakeCage := fake.New()
	session := newHostSetupSession(t, fakeCage)
	session.ensureHostRoute(context.Background(), cage.NetworkInfo{})
	if len(fakeCage.Calls) != 0 {
		t.Errorf("a network with no gateway was asked about a route: %v",
			fakeCage.Calls)
	}
}

// TestEnsureHostFirewallSkipsACageThatCannotHoldRules checks that a
// cage without the firewall capability is left alone and still
// reports rules in place.
func TestEnsureHostFirewallSkipsACageThatCannotHoldRules(t *testing.T) {
	fakeCage := fake.New()
	fakeCage.DeclaredCapabilities.HostFirewall = false
	session := newHostSetupSession(t, fakeCage)
	inPlace := session.ensureHostFirewall(context.Background(),
		cage.NetworkInfo{SubnetV4: fake.DefaultSubnetV4})
	if !inPlace {
		t.Error("a cage that holds no rules reported rules missing")
	}
	if len(fakeCage.Calls) != 0 {
		t.Errorf("a cage that holds no rules was asked to install them: %v",
			fakeCage.Calls)
	}
}
