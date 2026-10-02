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
		t.Fatalf("Box = %+v, want running", info)
	}
	if info.Address != "192.168.128.2" {
		t.Errorf("Address = %q, want 192.168.128.2", info.Address)
	}

	if err := fakeCage.Stop(ctx, "site-abc.prison"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if info, _ := fakeCage.Box(ctx, "site-abc.prison"); info.Running ||
		info.Address != "" {
		t.Errorf("Box after Stop = %+v, want stopped", info)
	}
	if err := fakeCage.Start(ctx, "site-abc.prison"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if info, _ := fakeCage.Box(ctx, "site-abc.prison"); info.Address !=
		"192.168.128.3" {
		t.Errorf("Box after Start = %+v, want 192.168.128.3", info)
	}
	if err := fakeCage.Delete(ctx, "site-abc.prison"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if info, _ := fakeCage.Box(ctx, "site-abc.prison"); info.Exists {
		t.Error("Exists after Delete = true, want false")
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
		t.Errorf("Calls =\n  %v\nwant\n  %v",
			fakeCage.Calls, want)
	}
	if len(fakeCage.CreateSpecs) != 1 ||
		fakeCage.CreateSpecs[0].Image != "prison/box:1" {
		t.Errorf("CreateSpecs = %+v, want one prison/box:1", fakeCage.CreateSpecs)
	}
}

func TestCreateRefusesADuplicate(t *testing.T) {
	ctx := context.Background()
	fakeCage := New()
	spec := cage.CreateSpec{Name: "twice", Image: "prison/box:1"}
	if err := fakeCage.Create(ctx, spec); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := fakeCage.Create(ctx, spec); err == nil {
		t.Error("second Create = nil, want error")
	}
}

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
		t.Errorf("ListBoxes = %v, want alpha bravo charlie", names)
	}
}

func TestFailMakesOneMethodFail(t *testing.T) {
	ctx := context.Background()
	fakeCage := New()
	wanted := errors.New("no room on the host")
	fakeCage.Fail["Create"] = wanted

	err := fakeCage.Create(ctx, cage.CreateSpec{Name: "doomed"})
	if !errors.Is(err, wanted) {
		t.Fatalf("Create = %v, want %v", err, wanted)
	}
	if info, _ := fakeCage.Box(ctx, "doomed"); info.Exists {
		t.Error("Exists after a failed Create = true, want false")
	}
	if len(fakeCage.CreateSpecs) != 0 {
		t.Errorf("CreateSpecs = %+v, want none", fakeCage.CreateSpecs)
	}
	if fakeCage.Calls[0] != "Create doomed" {
		t.Errorf("Calls = %v, want Create doomed first", fakeCage.Calls)
	}
	if err := fakeCage.Delete(ctx, "doomed"); err != nil {
		t.Errorf("Delete: %v", err)
	}
}

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
		t.Fatalf("Exec = %d, %v, want 0", status, err)
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
		t.Errorf("Exec = %d, %q, want 7, \"guest status\"",
			status, output.String())
	}
	if len(fakeCage.ExecSpecs) != 2 {
		t.Errorf("ExecSpecs = %+v, want 2", fakeCage.ExecSpecs)
	}

	if _, err := fakeCage.Exec(ctx, cage.ExecSpec{
		Box: "elsewhere"}); err == nil {
		t.Error("Exec(elsewhere) = nil, want error")
	}
}

func TestImagesAndBuild(t *testing.T) {
	ctx := context.Background()
	fakeCage := New()
	present, err := fakeCage.ImageExists(ctx, "prison/base:1")
	if err != nil || present {
		t.Fatalf("ImageExists before Build = %v, %v, want false", present, err)
	}
	if err := fakeCage.Build(ctx, cage.BuildSpec{
		Tag: "prison/base:1", Context: "/tmp/ctx"}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	present, err = fakeCage.ImageExists(ctx, "prison/base:1")
	if err != nil || !present {
		t.Errorf("ImageExists after Build = %v, %v, want true", present, err)
	}
	if len(fakeCage.BuildSpecs) != 1 {
		t.Errorf("BuildSpecs = %+v, want 1", fakeCage.BuildSpecs)
	}
}

func TestEnsureNetworkDefaults(t *testing.T) {
	ctx := context.Background()
	fakeCage := New()
	if info, _ := fakeCage.Network(ctx, "prison"); info.Exists {
		t.Fatal("Exists before EnsureNetwork = true, want false")
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
		t.Errorf("Network = %+v, want the defaults", info)
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
		t.Errorf("Network = %+v, want gateway 10.0.0.1", info)
	}
}

func TestCapabilitiesDecideTheHelpers(t *testing.T) {
	fakeCage := New()
	capabilities := fakeCage.Capabilities()
	if capabilities.Isolation != cage.IsolationVM {
		t.Errorf("Isolation = %q, want %q",
			capabilities.Isolation, cage.IsolationVM)
	}
	if !capabilities.GuestAddresses || !capabilities.GuestHostnames ||
		!capabilities.DNSDomain || !capabilities.RouteRepair ||
		!capabilities.HostOnlyNetwork || !capabilities.HostFirewall {
		t.Errorf("Capabilities = %+v, want all true", capabilities)
	}
	if fakeCage.DNS() == nil || fakeCage.Route() == nil {
		t.Fatal("DNS or Route = nil, want a helper")
	}

	fakeCage.DeclaredCapabilities.DNSDomain = false
	fakeCage.DeclaredCapabilities.RouteRepair = false
	if fakeCage.DNS() != nil {
		t.Error("DNS = non-nil, want nil")
	}
	if fakeCage.Route() != nil {
		t.Error("Route = non-nil, want nil")
	}
}

func TestHelpersRecordAndRemember(t *testing.T) {
	ctx := context.Background()
	fakeCage := New()

	if registered, _ := fakeCage.DNS().Exists(ctx, "prison"); registered {
		t.Fatal("Exists before Register = true, want false")
	}
	if err := fakeCage.DNS().Register(ctx, "prison"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if registered, _ := fakeCage.DNS().Exists(ctx, "prison"); !registered {
		t.Error("Exists after Register = false, want true")
	}

	if installed, _ := fakeCage.Route().Installed(
		ctx, DefaultGateway); installed {
		t.Fatal("Installed before Install = true, want false")
	}
	if err := fakeCage.Route().Install(ctx, DefaultGateway); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if installed, _ := fakeCage.Route().Installed(
		ctx, DefaultGateway); !installed {
		t.Error("Installed after Install = false, want true")
	}
	if !strings.Contains(strings.Join(fakeCage.Calls, "|"),
		"DNS.Register prison") {
		t.Errorf("Calls = %v, want DNS.Register prison", fakeCage.Calls)
	}
}

func TestUnavailableCage(t *testing.T) {
	fakeCage := New()
	if !fakeCage.Available() {
		t.Fatal("Available = false, want true")
	}
	if err := fakeCage.Require(context.Background()); err != nil {
		t.Fatalf("Require: %v", err)
	}
	fakeCage.Unavailable = true
	if fakeCage.Available() {
		t.Error("Available = true, want false")
	}
	if err := fakeCage.Require(context.Background()); err == nil {
		t.Error("Require = nil, want error")
	}
}

func TestLogsWriteWhatTheTestSet(t *testing.T) {
	ctx := context.Background()
	fakeCage := New()
	fakeCage.LogOutput["box"] = "started\n"
	var output bytes.Buffer
	if err := fakeCage.Logs(ctx, "box", &output); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if output.String() != "started\n" {
		t.Errorf("Logs = %q, want \"started\\n\"", output.String())
	}
	output.Reset()
	if err := fakeCage.Logs(ctx, "quiet", &output); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if output.Len() != 0 {
		t.Errorf("Logs(quiet) = %q, want empty", output.String())
	}
}
