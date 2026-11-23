package session

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"prison/internal/config"
	"prison/internal/isolator"
	"prison/internal/state"
)

type hostSetupIsolator struct {
	isolator.Isolator
	Domains          map[string]bool
	RouteIsInstalled bool
	AnchorInfo       isolator.BoxInfo
	AnchorEnsureErr  error
	Boxes            []isolator.BoxInfo
	StopErr          map[string]error
	NetworkInfo      isolator.NetworkInfo
	Calls            []string
}

func newHostSetupIsolator() *hostSetupIsolator {
	return &hostSetupIsolator{
		Domains: map[string]bool{},
		AnchorInfo: isolator.BoxInfo{
			Name: "prison-anchor",
		},
		StopErr: map[string]error{},
	}
}

func (f *hostSetupIsolator) DNS() isolator.DNSDomain { return fakeDNS{f} }

func (f *hostSetupIsolator) Route() isolator.HostRoute {
	return fakeRoute{f}
}

func (f *hostSetupIsolator) Anchor() isolator.NetworkAnchor {
	return fakeAnchor{f}
}

func (f *hostSetupIsolator) ListBoxes(ctx context.Context) ([]isolator.BoxInfo, error) {
	f.Calls = append(f.Calls, "ListBoxes")
	return append([]isolator.BoxInfo(nil), f.Boxes...), nil
}

func (f *hostSetupIsolator) Stop(ctx context.Context, name string) error {
	f.Calls = append(f.Calls, "Stop "+name)
	if err := f.StopErr[name]; err != nil {
		return err
	}
	for i := range f.Boxes {
		if f.Boxes[i].Name == name {
			f.Boxes[i].Running = false
		}
	}
	return nil
}

func (f *hostSetupIsolator) Start(ctx context.Context, name string) error {
	f.Calls = append(f.Calls, "Start "+name)
	for i := range f.Boxes {
		if f.Boxes[i].Name == name {
			f.Boxes[i].Running = true
		}
	}
	return nil
}

func (f *hostSetupIsolator) Network(ctx context.Context, name string) (isolator.NetworkInfo, error) {
	f.Calls = append(f.Calls, "Network "+name)
	return f.NetworkInfo, nil
}

type fakeAnchor struct{ owner *hostSetupIsolator }

func (a fakeAnchor) Name() string    { return "prison-anchor" }
func (a fakeAnchor) Network() string { return "default" }

func (a fakeAnchor) Status(ctx context.Context) (isolator.BoxInfo, error) {
	a.owner.Calls = append(a.owner.Calls, "Anchor.Status")
	return a.owner.AnchorInfo, nil
}

func (a fakeAnchor) Ensure(ctx context.Context) error {
	a.owner.Calls = append(a.owner.Calls, "Anchor.Ensure")
	if a.owner.AnchorEnsureErr != nil {
		return a.owner.AnchorEnsureErr
	}
	a.owner.AnchorInfo = isolator.BoxInfo{
		Name:    "prison-anchor",
		Exists:  true,
		Running: true,
		Network: "default",
	}
	return nil
}

func (a fakeAnchor) Remove(ctx context.Context) error {
	a.owner.Calls = append(a.owner.Calls, "Anchor.Remove")
	a.owner.AnchorInfo = isolator.BoxInfo{Name: "prison-anchor"}
	return nil
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
	return &Session{
		Environment: &Environment{
			Isolator: fakeIsolator,
			Overrides: &config.Overrides{
				Domain:     "prison",
				Network:    "prison",
				BrokerPort: 8787,
			},
			Root: &state.Root{Path: t.TempDir()},
		},
		BoxName: "app-1234567890ab.prison",
	}
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

func TestEnsureHostAnchorStartsWhenNoBoxesAreRunning(t *testing.T) {
	fakeIsolator := newHostSetupIsolator()
	fakeIsolator.Boxes = []isolator.BoxInfo{
		{Name: "app-1234567890ab.prison", Exists: true, Running: false, Network: "prison"},
		{Name: "unrelated", Exists: true, Running: true, Network: "default"},
	}
	s := newHostSetupSession(t, fakeIsolator)
	restore := s.ensureHostAnchor(context.Background(), false)
	if restore != nil {
		t.Error("restore != nil, want nil")
	}
	if !fakeIsolator.AnchorInfo.Running {
		t.Error("AnchorInfo.Running = false, want true")
	}
	if !slices.Contains(fakeIsolator.Calls, "Anchor.Ensure") {
		t.Errorf("Calls = %v, want Anchor.Ensure", fakeIsolator.Calls)
	}
}

func TestEnsureHostAnchorNoopsWhenAlreadyRunning(t *testing.T) {
	fakeIsolator := newHostSetupIsolator()
	fakeIsolator.AnchorInfo = isolator.BoxInfo{
		Name:    "prison-anchor",
		Exists:  true,
		Running: true,
		Network: "default",
	}
	s := newHostSetupSession(t, fakeIsolator)
	restore := s.ensureHostAnchor(context.Background(), false)
	if restore != nil {
		t.Error("restore != nil, want nil")
	}
	if len(fakeIsolator.Calls) != 1 || fakeIsolator.Calls[0] != "Anchor.Status" {
		t.Errorf("Calls = %v, want only Anchor.Status", fakeIsolator.Calls)
	}
}

func TestEnsureHostAnchorStopsRecreateBoxWithoutPromptAndRestoresOnFailure(t *testing.T) {
	previousTerminal := stdinIsTerminal
	previousConfirm := confirmPrompt
	t.Cleanup(func() {
		stdinIsTerminal = previousTerminal
		confirmPrompt = previousConfirm
	})
	stdinIsTerminal = func() bool { return false }
	prompted := 0
	confirmPrompt = func(string) bool {
		prompted++
		return false
	}

	fakeIsolator := newHostSetupIsolator()
	fakeIsolator.NetworkInfo = isolator.NetworkInfo{Name: "prison", Gateway: "192.168.128.1"}
	fakeIsolator.Boxes = []isolator.BoxInfo{
		{Name: "app-1234567890ab.prison", Exists: true, Running: true, Network: "prison"},
	}
	s := newHostSetupSession(t, fakeIsolator)
	restore := s.ensureHostAnchor(context.Background(), true)
	if prompted != 0 {
		t.Errorf("prompted = %d, want 0", prompted)
	}
	if restore == nil {
		t.Fatal("restore = nil, want a restore callback")
	}
	if fakeIsolator.Boxes[0].Running {
		t.Error("recreate box is still running, want stopped")
	}
	if !fakeIsolator.AnchorInfo.Running {
		t.Error("AnchorInfo.Running = false, want true")
	}
	wantPrefix := []string{
		"Anchor.Status",
		"ListBoxes",
		"Stop app-1234567890ab.prison",
		"Anchor.Ensure",
	}
	if strings.Join(fakeIsolator.Calls, ",") != strings.Join(wantPrefix, ",") {
		t.Errorf("Calls = %v, want %v", fakeIsolator.Calls, wantPrefix)
	}

	restore()
	if !fakeIsolator.Boxes[0].Running {
		t.Error("restore did not restart app-1234567890ab.prison")
	}
	if !fakeIsolator.RouteIsInstalled {
		t.Error("restore did not repair host route")
	}
}

func TestEnsureHostAnchorQuiescesAndRestartsOtherBoxesAfterConfirmation(t *testing.T) {
	previousTerminal := stdinIsTerminal
	previousConfirm := confirmPrompt
	t.Cleanup(func() {
		stdinIsTerminal = previousTerminal
		confirmPrompt = previousConfirm
	})
	stdinIsTerminal = func() bool { return true }
	prompted := 0
	confirmPrompt = func(string) bool {
		prompted++
		return true
	}

	fakeIsolator := newHostSetupIsolator()
	fakeIsolator.NetworkInfo = isolator.NetworkInfo{Name: "prison", Gateway: "192.168.128.1"}
	fakeIsolator.Boxes = []isolator.BoxInfo{
		{Name: "other-abcdef012345.prison", Exists: true, Running: true, Network: "prison"},
	}
	s := newHostSetupSession(t, fakeIsolator)
	restore := s.ensureHostAnchor(context.Background(), false)
	if restore != nil {
		t.Error("restore != nil, want nil")
	}
	if prompted != 1 {
		t.Errorf("prompted = %d, want 1", prompted)
	}
	if !fakeIsolator.AnchorInfo.Running {
		t.Error("AnchorInfo.Running = false, want true")
	}
	if !fakeIsolator.Boxes[0].Running {
		t.Error("other box was not restarted")
	}
	if !fakeIsolator.RouteIsInstalled {
		t.Error("host route was not checked/installed after restart")
	}
	wantOrder := []string{
		"Anchor.Status",
		"ListBoxes",
		"Stop other-abcdef012345.prison",
		"Anchor.Ensure",
		"Start other-abcdef012345.prison",
	}
	if len(fakeIsolator.Calls) < len(wantOrder) ||
		strings.Join(fakeIsolator.Calls[:len(wantOrder)], ",") != strings.Join(wantOrder, ",") {
		t.Errorf("Calls = %v, want prefix %v", fakeIsolator.Calls, wantOrder)
	}
}

func TestEnsureHostAnchorSkipsWhenUserDeclinesOrNonInteractive(t *testing.T) {
	previousTerminal := stdinIsTerminal
	previousConfirm := confirmPrompt
	t.Cleanup(func() {
		stdinIsTerminal = previousTerminal
		confirmPrompt = previousConfirm
	})

	for _, testCase := range []struct {
		name        string
		interactive bool
	}{
		{"declined", true},
		{"non-interactive", false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stdinIsTerminal = func() bool { return testCase.interactive }
			confirmPrompt = func(string) bool { return false }

			fakeIsolator := newHostSetupIsolator()
			fakeIsolator.Boxes = []isolator.BoxInfo{
				{Name: "other-abcdef012345.prison", Exists: true, Running: true, Network: "prison"},
			}
			s := newHostSetupSession(t, fakeIsolator)
			s.ensureHostAnchor(context.Background(), false)
			if fakeIsolator.AnchorInfo.Running {
				t.Error("AnchorInfo.Running = true, want false")
			}
			if slices.Contains(fakeIsolator.Calls, "Anchor.Ensure") {
				t.Errorf("Calls = %v, want no Anchor.Ensure", fakeIsolator.Calls)
			}
			if !fakeIsolator.Boxes[0].Running {
				t.Error("other box was unexpectedly stopped")
			}
		})
	}
}

func TestStartAnchorWithQuiesceRestartsStoppedBoxesOnStopFailure(t *testing.T) {
	fakeIsolator := newHostSetupIsolator()
	fakeIsolator.Boxes = []isolator.BoxInfo{
		{Name: "first.prison", Exists: true, Running: true, Network: "prison"},
		{Name: "second.prison", Exists: true, Running: true, Network: "prison"},
	}
	fakeIsolator.StopErr["second.prison"] = errors.New("busy")
	s := newHostSetupSession(t, fakeIsolator)

	_, err := s.StartAnchorWithQuiesce(context.Background(), fakeIsolator.Boxes, "")
	if err == nil {
		t.Fatal("StartAnchorWithQuiesce = nil, want error")
	}
	if slices.Contains(fakeIsolator.Calls, "Anchor.Ensure") {
		t.Errorf("Calls = %v, want no Anchor.Ensure when a box failed to stop", fakeIsolator.Calls)
	}
	if !fakeIsolator.Boxes[0].Running {
		t.Error("first.prison was not restarted after second.prison failed to stop")
	}
}
