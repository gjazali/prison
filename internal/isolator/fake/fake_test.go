package fake

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"prison/internal/isolator"
)

func TestLifecycleRecordsEveryCall(t *testing.T) {
	ctx := context.Background()
	fakeIsolator := New()

	err := fakeIsolator.Create(ctx, isolator.CreateSpec{
		Name:    "site-abc.prison",
		Image:   "prison/box:1",
		Network: "prison",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	info, err := fakeIsolator.Box(ctx, "site-abc.prison")
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if !info.Exists || !info.Running {
		t.Fatalf("Box = %+v, want running", info)
	}
	if info.Address != "192.168.128.2" {
		t.Errorf("Address = %q, want 192.168.128.2", info.Address)
	}

	if err := fakeIsolator.Stop(ctx, "site-abc.prison"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if info, _ := fakeIsolator.Box(ctx, "site-abc.prison"); info.Running ||
		info.Address != "" {
		t.Errorf("Box after Stop = %+v, want stopped", info)
	}
	if err := fakeIsolator.Start(ctx, "site-abc.prison"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if info, _ := fakeIsolator.Box(ctx, "site-abc.prison"); info.Address !=
		"192.168.128.3" {
		t.Errorf("Box after Start = %+v, want 192.168.128.3", info)
	}
	if err := fakeIsolator.Delete(ctx, "site-abc.prison"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if info, _ := fakeIsolator.Box(ctx, "site-abc.prison"); info.Exists {
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
	if strings.Join(fakeIsolator.Calls, "|") != strings.Join(want, "|") {
		t.Errorf("Calls =\n  %v\nwant\n  %v",
			fakeIsolator.Calls, want)
	}
	if len(fakeIsolator.CreateSpecs) != 1 ||
		fakeIsolator.CreateSpecs[0].Image != "prison/box:1" {
		t.Errorf("CreateSpecs = %+v, want one prison/box:1",
			fakeIsolator.CreateSpecs)
	}
}

func TestCreateRefusesADuplicate(t *testing.T) {
	ctx := context.Background()
	fakeIsolator := New()
	spec := isolator.CreateSpec{Name: "twice", Image: "prison/box:1"}
	if err := fakeIsolator.Create(ctx, spec); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := fakeIsolator.Create(ctx, spec); err == nil {
		t.Error("second Create = nil, want error")
	}
}

func TestListBoxesIsSorted(t *testing.T) {
	ctx := context.Background()
	fakeIsolator := New()
	for _, name := range []string{"charlie", "alpha", "bravo"} {
		if err := fakeIsolator.Create(
			ctx, isolator.CreateSpec{Name: name}); err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
	}
	boxes, err := fakeIsolator.ListBoxes(ctx)
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
	fakeIsolator := New()
	wanted := errors.New("no room on the host")
	fakeIsolator.Fail["Create"] = wanted

	err := fakeIsolator.Create(ctx, isolator.CreateSpec{Name: "doomed"})
	if !errors.Is(err, wanted) {
		t.Fatalf("Create = %v, want %v", err, wanted)
	}
	if info, _ := fakeIsolator.Box(ctx, "doomed"); info.Exists {
		t.Error("Exists after a failed Create = true, want false")
	}
	if len(fakeIsolator.CreateSpecs) != 0 {
		t.Errorf("CreateSpecs = %+v, want none", fakeIsolator.CreateSpecs)
	}
	if fakeIsolator.Calls[0] != "Create doomed" {
		t.Errorf("Calls = %v, want Create doomed first", fakeIsolator.Calls)
	}
	if err := fakeIsolator.Delete(ctx, "doomed"); err != nil {
		t.Errorf("Delete: %v", err)
	}
}

func TestExecHandlerScriptsTheResult(t *testing.T) {
	ctx := context.Background()
	fakeIsolator := New()
	if err := fakeIsolator.Create(
		ctx, isolator.CreateSpec{Name: "box"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	status, err := fakeIsolator.Exec(ctx, isolator.ExecSpec{
		Box: "box", Command: []string{"true"},
	})
	if err != nil || status != 0 {
		t.Fatalf("Exec = %d, %v, want 0", status, err)
	}

	fakeIsolator.ExecHandler = func(spec isolator.ExecSpec) (int, error) {
		io.WriteString(spec.Stdout, strings.Join(spec.Command, " "))
		return 7, nil
	}
	var output bytes.Buffer
	status, err = fakeIsolator.Exec(ctx, isolator.ExecSpec{
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
	if len(fakeIsolator.ExecSpecs) != 2 {
		t.Errorf("ExecSpecs = %+v, want 2", fakeIsolator.ExecSpecs)
	}

	if _, err := fakeIsolator.Exec(ctx, isolator.ExecSpec{
		Box: "elsewhere"}); err == nil {
		t.Error("Exec(elsewhere) = nil, want error")
	}
}

func TestImagesAndBuild(t *testing.T) {
	ctx := context.Background()
	fakeIsolator := New()
	present, err := fakeIsolator.ImageExists(ctx, "prison/base:1")
	if err != nil || present {
		t.Fatalf("ImageExists before Build = %v, %v, want false", present, err)
	}
	if err := fakeIsolator.Build(ctx, isolator.BuildSpec{
		Tag: "prison/base:1", Context: "/tmp/ctx"}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	present, err = fakeIsolator.ImageExists(ctx, "prison/base:1")
	if err != nil || !present {
		t.Errorf("ImageExists after Build = %v, %v, want true", present, err)
	}
	if len(fakeIsolator.BuildSpecs) != 1 {
		t.Errorf("BuildSpecs = %+v, want 1", fakeIsolator.BuildSpecs)
	}
}

func TestEnsureNetworkDefaults(t *testing.T) {
	ctx := context.Background()
	fakeIsolator := New()
	if info, _ := fakeIsolator.Network(ctx, "prison"); info.Exists {
		t.Fatal("Exists before EnsureNetwork = true, want false")
	}
	if err := fakeIsolator.EnsureNetwork(ctx, "prison"); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	info, err := fakeIsolator.Network(ctx, "prison")
	if err != nil {
		t.Fatalf("Network: %v", err)
	}
	if !info.Exists || !info.HostOnly ||
		info.Gateway != DefaultGateway ||
		info.SubnetV4 != DefaultSubnetV4 {
		t.Errorf("Network = %+v, want the defaults", info)
	}

	fakeIsolator.Networks["prison"] = isolator.NetworkInfo{
		Name: "prison", Exists: true, HostOnly: true,
		Gateway: "10.0.0.1",
	}
	if err := fakeIsolator.EnsureNetwork(ctx, "prison"); err != nil {
		t.Fatalf("EnsureNetwork on an existing network: %v", err)
	}
	if info, _ := fakeIsolator.Network(ctx, "prison"); info.Gateway !=
		"10.0.0.1" {
		t.Errorf("Network = %+v, want gateway 10.0.0.1", info)
	}
}

func TestHelpersRecordAndRemember(t *testing.T) {
	ctx := context.Background()
	fakeIsolator := New()

	if registered, _ := fakeIsolator.DNS().Exists(ctx, "prison"); registered {
		t.Fatal("Exists before Register = true, want false")
	}
	if err := fakeIsolator.DNS().Register(ctx, "prison"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if registered, _ := fakeIsolator.DNS().Exists(ctx, "prison"); !registered {
		t.Error("Exists after Register = false, want true")
	}

	if installed, _ := fakeIsolator.Route().Installed(
		ctx, DefaultGateway); installed {
		t.Fatal("Installed before Install = true, want false")
	}
	if err := fakeIsolator.Route().Install(ctx, DefaultGateway); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if installed, _ := fakeIsolator.Route().Installed(
		ctx, DefaultGateway); !installed {
		t.Error("Installed after Install = false, want true")
	}
	if !strings.Contains(strings.Join(fakeIsolator.Calls, "|"),
		"DNS.Register prison") {
		t.Errorf("Calls = %v, want DNS.Register prison", fakeIsolator.Calls)
	}
}

func TestUnavailableIsolator(t *testing.T) {
	fakeIsolator := New()
	if err := fakeIsolator.Require(context.Background()); err != nil {
		t.Fatalf("Require: %v", err)
	}
	fakeIsolator.Unavailable = true
	if err := fakeIsolator.Require(context.Background()); err == nil {
		t.Error("Require = nil, want error")
	}
}

func TestLogsWriteWhatTheTestSet(t *testing.T) {
	ctx := context.Background()
	fakeIsolator := New()
	fakeIsolator.LogOutput["box"] = "started\n"
	var output bytes.Buffer
	if err := fakeIsolator.Logs(ctx, "box", &output); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if output.String() != "started\n" {
		t.Errorf("Logs = %q, want \"started\\n\"", output.String())
	}
	output.Reset()
	if err := fakeIsolator.Logs(ctx, "quiet", &output); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if output.Len() != 0 {
		t.Errorf("Logs(quiet) = %q, want empty", output.String())
	}
}
