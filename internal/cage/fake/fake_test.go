package fake

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"prison/internal/cage"
)

// TestLifecycleRecordsEveryCall walks a box through create, stop,
// start, and delete, checking state and call log at each step.
func TestLifecycleRecordsEveryCall(t *testing.T) {
	ctx := context.Background()
	fakeCage := New()

	err := fakeCage.Create(ctx, cage.CreateSpec{
		Name:    "site-abc.prison",
		Image:   "prison/box:1",
		Network: "prison",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	info, err := fakeCage.Box(ctx, "site-abc.prison")
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if !info.Exists || !info.Running {
		t.Fatalf("box reads as %+v", info)
	}
	if info.Address != "192.168.128.2" {
		t.Errorf("address is %q", info.Address)
	}

	if err := fakeCage.Stop(ctx, "site-abc.prison"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if info, _ := fakeCage.Box(ctx, "site-abc.prison"); info.Running ||
		info.Address != "" {
		t.Errorf("stopped box reads as %+v", info)
	}
	if err := fakeCage.Start(ctx, "site-abc.prison"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if info, _ := fakeCage.Box(ctx, "site-abc.prison"); info.Address !=
		"192.168.128.3" {
		t.Errorf("restarted box reads as %+v", info)
	}
	if err := fakeCage.Delete(ctx, "site-abc.prison"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if info, _ := fakeCage.Box(ctx, "site-abc.prison"); info.Exists {
		t.Error("the box outlived Delete")
	}

	want := []string{
		"Create site-abc.prison",
		"Box site-abc.prison",
		"Stop site-abc.prison",
		"Box site-abc.prison",
		"Start site-abc.prison",
		"Box site-abc.prison",
		"Delete site-abc.prison",
		"Box site-abc.prison",
	}
	if strings.Join(fakeCage.Calls, "|") != strings.Join(want, "|") {
		t.Errorf("call log is\n  %v\nwant\n  %v",
			fakeCage.Calls, want)
	}
	if len(fakeCage.CreateSpecs) != 1 ||
		fakeCage.CreateSpecs[0].Image != "prison/box:1" {
		t.Errorf("create specs are %+v", fakeCage.CreateSpecs)
	}
}

// TestCreateRefusesADuplicate checks that creating a box with a
// duplicate name returns an error.
func TestCreateRefusesADuplicate(t *testing.T) {
	ctx := context.Background()
	fakeCage := New()
	spec := cage.CreateSpec{Name: "twice", Image: "prison/box:1"}
	if err := fakeCage.Create(ctx, spec); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := fakeCage.Create(ctx, spec); err == nil {
		t.Error("a duplicate name was accepted")
	}
}

// TestListBoxesIsSorted checks that boxes are listed in sorted name
// order.
func TestListBoxesIsSorted(t *testing.T) {
	ctx := context.Background()
	fakeCage := New()
	for _, name := range []string{"charlie", "alpha", "bravo"} {
		if err := fakeCage.Create(
			ctx, cage.CreateSpec{Name: name}); err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
	}
	boxes, err := fakeCage.ListBoxes(ctx)
	if err != nil {
		t.Fatalf("ListBoxes: %v", err)
	}
	var names []string
	for _, box := range boxes {
		names = append(names, box.Name)
	}
	if strings.Join(names, ",") != "alpha,bravo,charlie" {
		t.Errorf("boxes came back as %v", names)
	}
}

// TestFailMakesOneMethodFail checks that a method in the Fail map
// returns the configured error and changes no state.
func TestFailMakesOneMethodFail(t *testing.T) {
	ctx := context.Background()
	fakeCage := New()
	wanted := errors.New("no room on the host")
	fakeCage.Fail["Create"] = wanted

	err := fakeCage.Create(ctx, cage.CreateSpec{Name: "doomed"})
	if !errors.Is(err, wanted) {
		t.Fatalf("Create returned %v", err)
	}
	if info, _ := fakeCage.Box(ctx, "doomed"); info.Exists {
		t.Error("a failed Create left a box behind")
	}
	if len(fakeCage.CreateSpecs) != 0 {
		t.Error("a failed Create recorded its spec")
	}
	if fakeCage.Calls[0] != "Create doomed" {
		t.Errorf("a failed call was not logged: %v", fakeCage.Calls)
	}
	if err := fakeCage.Delete(ctx, "doomed"); err != nil {
		t.Errorf("Delete also failed: %v", err)
	}
}

// TestExecHandlerScriptsTheResult checks the default exit status and
// a custom handler that writes output and returns a code.
func TestExecHandlerScriptsTheResult(t *testing.T) {
	ctx := context.Background()
	fakeCage := New()
	if err := fakeCage.Create(
		ctx, cage.CreateSpec{Name: "box"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	status, err := fakeCage.Exec(ctx, cage.ExecSpec{
		Box: "box", Command: []string{"true"},
	})
	if err != nil || status != 0 {
		t.Fatalf("default exec returned %d, %v", status, err)
	}

	fakeCage.ExecHandler = func(spec cage.ExecSpec) (int, error) {
		io.WriteString(spec.Stdout, strings.Join(spec.Command, " "))
		return 7, nil
	}
	var output bytes.Buffer
	status, err = fakeCage.Exec(ctx, cage.ExecSpec{
		Box:     "box",
		Command: []string{"guest", "status"},
		Stdout:  &output,
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if status != 7 || output.String() != "guest status" {
		t.Errorf("exec returned %d and wrote %q",
			status, output.String())
	}
	if len(fakeCage.ExecSpecs) != 2 {
		t.Errorf("exec specs are %+v", fakeCage.ExecSpecs)
	}

	if _, err := fakeCage.Exec(ctx, cage.ExecSpec{
		Box: "elsewhere"}); err == nil {
		t.Error("exec into a box that is not running was accepted")
	}
}

// TestImagesAndBuild checks that Build makes a tag present and
// unbuilt tags read as absent.
func TestImagesAndBuild(t *testing.T) {
	ctx := context.Background()
	fakeCage := New()
	present, err := fakeCage.ImageExists(ctx, "prison/base:1")
	if err != nil || present {
		t.Fatalf("an unbuilt tag reads as %v, %v", present, err)
	}
	if err := fakeCage.Build(ctx, cage.BuildSpec{
		Tag: "prison/base:1", Context: "/tmp/ctx"}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	present, err = fakeCage.ImageExists(ctx, "prison/base:1")
	if err != nil || !present {
		t.Errorf("a built tag reads as %v, %v", present, err)
	}
	if len(fakeCage.BuildSpecs) != 1 {
		t.Errorf("build specs are %+v", fakeCage.BuildSpecs)
	}
}

// TestEnsureNetworkDefaults checks that a new network gets default
// values and an existing one is not overwritten.
func TestEnsureNetworkDefaults(t *testing.T) {
	ctx := context.Background()
	fakeCage := New()
	if info, _ := fakeCage.Network(ctx, "prison"); info.Exists {
		t.Fatal("a network existed before EnsureNetwork")
	}
	if err := fakeCage.EnsureNetwork(ctx, "prison"); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	info, err := fakeCage.Network(ctx, "prison")
	if err != nil {
		t.Fatalf("Network: %v", err)
	}
	if !info.Exists || !info.HostOnly ||
		info.Gateway != DefaultGateway ||
		info.SubnetV4 != DefaultSubnetV4 {
		t.Errorf("network reads as %+v", info)
	}

	fakeCage.Networks["prison"] = cage.NetworkInfo{
		Name: "prison", Exists: true, HostOnly: true,
		Gateway: "10.0.0.1",
	}
	if err := fakeCage.EnsureNetwork(ctx, "prison"); err != nil {
		t.Fatalf("EnsureNetwork on an existing network: %v", err)
	}
	if info, _ := fakeCage.Network(ctx, "prison"); info.Gateway !=
		"10.0.0.1" {
		t.Errorf("an existing network was overwritten: %+v", info)
	}
}

// TestCapabilitiesDecideTheHelpers checks default capabilities and
// that disabling one makes its helper return nil.
func TestCapabilitiesDecideTheHelpers(t *testing.T) {
	fakeCage := New()
	capabilities := fakeCage.Capabilities()
	if capabilities.Isolation != cage.IsolationVM {
		t.Errorf("isolation is %q", capabilities.Isolation)
	}
	if !capabilities.GuestAddresses || !capabilities.GuestHostnames ||
		!capabilities.DNSDomain || !capabilities.RouteRepair ||
		!capabilities.HostOnlyNetwork || !capabilities.HostFirewall {
		t.Errorf("capabilities are %+v", capabilities)
	}
	if fakeCage.DNS() == nil || fakeCage.Route() == nil {
		t.Fatal("a fully capable cage returned a nil helper")
	}

	fakeCage.DeclaredCapabilities.DNSDomain = false
	fakeCage.DeclaredCapabilities.RouteRepair = false
	if fakeCage.DNS() != nil {
		t.Error("DNS is not nil without the capability")
	}
	if fakeCage.Route() != nil {
		t.Error("Route is not nil without the capability")
	}
}

// TestHelpersRecordAndRemember checks that DNS and route helpers
// log calls and persist their state.
func TestHelpersRecordAndRemember(t *testing.T) {
	ctx := context.Background()
	fakeCage := New()

	if registered, _ := fakeCage.DNS().Exists(ctx, "prison"); registered {
		t.Fatal("a domain was registered before Register")
	}
	if err := fakeCage.DNS().Register(ctx, "prison"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if registered, _ := fakeCage.DNS().Exists(ctx, "prison"); !registered {
		t.Error("Register did not take")
	}

	if installed, _ := fakeCage.Route().Installed(
		ctx, DefaultGateway); installed {
		t.Fatal("a route was installed before Install")
	}
	if err := fakeCage.Route().Install(ctx, DefaultGateway); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if installed, _ := fakeCage.Route().Installed(
		ctx, DefaultGateway); !installed {
		t.Error("Install did not take")
	}
	if !strings.Contains(strings.Join(fakeCage.Calls, "|"),
		"DNS.Register prison") {
		t.Errorf("call log is %v", fakeCage.Calls)
	}
}

// TestUnavailableCage checks that the Unavailable flag affects
// both Available and Require.
func TestUnavailableCage(t *testing.T) {
	fakeCage := New()
	if !fakeCage.Available() {
		t.Fatal("a new fake cage is unavailable")
	}
	if err := fakeCage.Require(context.Background()); err != nil {
		t.Fatalf("Require: %v", err)
	}
	fakeCage.Unavailable = true
	if fakeCage.Available() {
		t.Error("Available ignored the flag")
	}
	if err := fakeCage.Require(context.Background()); err == nil {
		t.Error("Require ignored the flag")
	}
}

// TestLogsWriteWhatTheTestSet checks that Logs writes the scripted
// output or nothing when none is set.
func TestLogsWriteWhatTheTestSet(t *testing.T) {
	ctx := context.Background()
	fakeCage := New()
	fakeCage.LogOutput["box"] = "started\n"
	var output bytes.Buffer
	if err := fakeCage.Logs(ctx, "box", &output); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if output.String() != "started\n" {
		t.Errorf("logs wrote %q", output.String())
	}
	output.Reset()
	if err := fakeCage.Logs(ctx, "quiet", &output); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if output.Len() != 0 {
		t.Errorf("a box with no logs wrote %q", output.String())
	}
}
