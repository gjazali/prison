package session

import (
	"context"
	"slices"
	"testing"

	"prison/internal/config"
	"prison/internal/isolator"
	"prison/internal/isolator/fake"
	"prison/internal/state"
)

func newHostSetupSession(t *testing.T, fakeIsolator *fake.Isolator) *Session {
	t.Helper()
	return &Session{Environment: &Environment{
		Isolator: fakeIsolator,
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
	fakeIsolator := fake.New()
	session := newHostSetupSession(t, fakeIsolator)
	session.ensureHostDNS(context.Background())
	if !fakeIsolator.Domains["prison"] {
		t.Error("Domains[prison] = false, want true")
	}
}

func TestEnsureHostDNSLeavesARegisteredDomainAlone(t *testing.T) {
	fakeIsolator := fake.New()
	fakeIsolator.Domains["prison"] = true
	session := newHostSetupSession(t, fakeIsolator)
	session.ensureHostDNS(context.Background())
	if calledMethod(fakeIsolator.Calls, "DNS.Register") {
		t.Errorf("Calls = %v, want no DNS.Register",
			fakeIsolator.Calls)
	}
}

func TestEnsureHostRouteAddsAMissingRoute(t *testing.T) {
	fakeIsolator := fake.New()
	session := newHostSetupSession(t, fakeIsolator)
	session.ensureHostRoute(context.Background(),
		isolator.NetworkInfo{Gateway: "192.168.128.1"})
	if !fakeIsolator.RouteIsInstalled {
		t.Error("RouteIsInstalled = false, want true")
	}
}

func TestEnsureHostRouteLeavesAWorkingRouteAlone(t *testing.T) {
	fakeIsolator := fake.New()
	fakeIsolator.RouteIsInstalled = true
	session := newHostSetupSession(t, fakeIsolator)
	session.ensureHostRoute(context.Background(),
		isolator.NetworkInfo{Gateway: "192.168.128.1"})
	if calledMethod(fakeIsolator.Calls, "Route.Install") {
		t.Errorf("Calls = %v, want no Route.Install", fakeIsolator.Calls)
	}
}

func TestEnsureHostRouteWaitsForABridge(t *testing.T) {
	fakeIsolator := fake.New()
	session := newHostSetupSession(t, fakeIsolator)
	session.ensureHostRoute(context.Background(), isolator.NetworkInfo{})
	if len(fakeIsolator.Calls) != 0 {
		t.Errorf("Calls = %v, want none",
			fakeIsolator.Calls)
	}
}
