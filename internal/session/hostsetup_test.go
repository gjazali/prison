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

func calledMethod(calls []string, method string) bool {
	return slices.ContainsFunc(calls, func(line string) bool {
		return line == method || len(line) > len(method) &&
			line[:len(method)+1] == method+" "
	})
}

func TestEnsureHostDNSRegistersADomainThatIsNotThere(t *testing.T) {
	fakeCage := fake.New()
	session := newHostSetupSession(t, fakeCage)
	session.ensureHostDNS(context.Background())
	if !fakeCage.Domains["prison"] {
		t.Error("Domains[prison] = false, want true")
	}
}

func TestEnsureHostDNSLeavesARegisteredDomainAlone(t *testing.T) {
	fakeCage := fake.New()
	fakeCage.Domains["prison"] = true
	session := newHostSetupSession(t, fakeCage)
	session.ensureHostDNS(context.Background())
	if calledMethod(fakeCage.Calls, "DNS.Register") {
		t.Errorf("Calls = %v, want no DNS.Register",
			fakeCage.Calls)
	}
}

func TestEnsureHostDNSSkipsACageThatNamesNothing(t *testing.T) {
	fakeCage := fake.New()
	fakeCage.DeclaredCapabilities.DNSDomain = false
	session := newHostSetupSession(t, fakeCage)
	session.ensureHostDNS(context.Background())
	if len(fakeCage.Calls) != 0 {
		t.Errorf("Calls = %v, want none",
			fakeCage.Calls)
	}
}

func TestEnsureHostRouteAddsAMissingRoute(t *testing.T) {
	fakeCage := fake.New()
	session := newHostSetupSession(t, fakeCage)
	session.ensureHostRoute(context.Background(),
		cage.NetworkInfo{Gateway: "192.168.128.1"})
	if !fakeCage.RouteIsInstalled {
		t.Error("RouteIsInstalled = false, want true")
	}
}

func TestEnsureHostRouteLeavesAWorkingRouteAlone(t *testing.T) {
	fakeCage := fake.New()
	fakeCage.RouteIsInstalled = true
	session := newHostSetupSession(t, fakeCage)
	session.ensureHostRoute(context.Background(),
		cage.NetworkInfo{Gateway: "192.168.128.1"})
	if calledMethod(fakeCage.Calls, "Route.Install") {
		t.Errorf("Calls = %v, want no Route.Install", fakeCage.Calls)
	}
}

func TestEnsureHostRouteWaitsForABridge(t *testing.T) {
	fakeCage := fake.New()
	session := newHostSetupSession(t, fakeCage)
	session.ensureHostRoute(context.Background(), cage.NetworkInfo{})
	if len(fakeCage.Calls) != 0 {
		t.Errorf("Calls = %v, want none",
			fakeCage.Calls)
	}
}

func TestEnsureHostFirewallSkipsACageThatCannotHoldRules(t *testing.T) {
	fakeCage := fake.New()
	fakeCage.DeclaredCapabilities.HostFirewall = false
	session := newHostSetupSession(t, fakeCage)
	inPlace := session.ensureHostFirewall(context.Background(),
		cage.NetworkInfo{SubnetV4: fake.DefaultSubnetV4})
	if !inPlace {
		t.Error("ensureHostFirewall = false, want true")
	}
	if len(fakeCage.Calls) != 0 {
		t.Errorf("Calls = %v, want none",
			fakeCage.Calls)
	}
}
