package session

import (
	"context"
	"slices"
	"testing"

	"prison/internal/config"
	"prison/internal/isolator"
	"prison/internal/state"
)

type hostSetupIsolator struct {
	isolator.Isolator
	Domains          map[string]bool
	RouteIsInstalled bool
	Calls            []string
}

func newHostSetupIsolator() *hostSetupIsolator {
	return &hostSetupIsolator{Domains: map[string]bool{}}
}

func (f *hostSetupIsolator) DNS() isolator.DNSDomain { return fakeDNS{f} }

func (f *hostSetupIsolator) Route() isolator.HostRoute {
	return fakeRoute{f}
}

type fakeDNS struct{ owner *hostSetupIsolator }

func (d fakeDNS) Exists(ctx context.Context, domain string) (bool, error) {
	d.owner.Calls = append(d.owner.Calls, "DNS.Exists")
	return d.owner.Domains[domain], nil
}

func (d fakeDNS) Describe(domain string) []string { return nil }

func (d fakeDNS) Register(ctx context.Context, domain string) error {
	d.owner.Calls = append(d.owner.Calls, "DNS.Register")
	d.owner.Domains[domain] = true
	return nil
}

func (d fakeDNS) RepairHint(domain string) []string { return nil }

type fakeRoute struct{ owner *hostSetupIsolator }

func (r fakeRoute) Describe(
	ctx context.Context, gateway string,
) (*isolator.RouteInfo, error) {
	return &isolator.RouteInfo{}, nil
}

func (r fakeRoute) Installed(
	ctx context.Context, gateway string,
) (bool, error) {
	r.owner.Calls = append(r.owner.Calls, "Route.Installed")
	return r.owner.RouteIsInstalled, nil
}

func (r fakeRoute) Command(
	ctx context.Context, gateway string,
) (string, error) {
	return "", nil
}

func (r fakeRoute) Install(ctx context.Context, gateway string) error {
	r.owner.Calls = append(r.owner.Calls, "Route.Install")
	r.owner.RouteIsInstalled = true
	return nil
}

func newHostSetupSession(
	t *testing.T, fakeIsolator *hostSetupIsolator,
) *Session {
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

func TestEnsureHostDNSRegistersADomainThatIsNotThere(t *testing.T) {
	fakeIsolator := newHostSetupIsolator()
	session := newHostSetupSession(t, fakeIsolator)
	session.ensureHostDNS(context.Background())
	if !fakeIsolator.Domains["prison"] {
		t.Error("Domains[prison] = false, want true")
	}
}

func TestEnsureHostDNSLeavesARegisteredDomainAlone(t *testing.T) {
	fakeIsolator := newHostSetupIsolator()
	fakeIsolator.Domains["prison"] = true
	session := newHostSetupSession(t, fakeIsolator)
	session.ensureHostDNS(context.Background())
	if slices.Contains(fakeIsolator.Calls, "DNS.Register") {
		t.Errorf("Calls = %v, want no DNS.Register",
			fakeIsolator.Calls)
	}
}

func TestEnsureHostRouteAddsAMissingRoute(t *testing.T) {
	fakeIsolator := newHostSetupIsolator()
	session := newHostSetupSession(t, fakeIsolator)
	session.ensureHostRoute(context.Background(),
		isolator.NetworkInfo{Gateway: "192.168.128.1"})
	if !fakeIsolator.RouteIsInstalled {
		t.Error("RouteIsInstalled = false, want true")
	}
}

func TestEnsureHostRouteLeavesAWorkingRouteAlone(t *testing.T) {
	fakeIsolator := newHostSetupIsolator()
	fakeIsolator.RouteIsInstalled = true
	session := newHostSetupSession(t, fakeIsolator)
	session.ensureHostRoute(context.Background(),
		isolator.NetworkInfo{Gateway: "192.168.128.1"})
	if slices.Contains(fakeIsolator.Calls, "Route.Install") {
		t.Errorf("Calls = %v, want no Route.Install", fakeIsolator.Calls)
	}
}

func TestEnsureHostRouteWaitsForABridge(t *testing.T) {
	fakeIsolator := newHostSetupIsolator()
	session := newHostSetupSession(t, fakeIsolator)
	session.ensureHostRoute(context.Background(), isolator.NetworkInfo{})
	if len(fakeIsolator.Calls) != 0 {
		t.Errorf("Calls = %v, want none",
			fakeIsolator.Calls)
	}
}
