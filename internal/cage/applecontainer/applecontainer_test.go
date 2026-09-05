package applecontainer

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"prison/internal/cage"
)

// fixtureResponse is a scripted answer for one command.
type fixtureResponse struct {
	stdout string
	stderr string
	status int
}

// fixtureRunner looks up responses by full command line, records
// every call, and defaults to exit 1 for unknown commands.
type fixtureRunner struct {
	responses map[string]fixtureResponse
	calls     []string
}

// newFixtureRunner returns a runner with an empty response table.
func newFixtureRunner() *fixtureRunner {
	return &fixtureRunner{responses: map[string]fixtureResponse{}}
}

// answer adds one response keyed by the expected command line.
func (runner *fixtureRunner) answer(
	line string, response fixtureResponse,
) {
	runner.responses[line] = response
}

// answerFile adds a response whose stdout comes from a testdata
// file.
func (runner *fixtureRunner) answerFile(
	t *testing.T, line string, name string,
) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	runner.answer(line, fixtureResponse{stdout: string(content)})
}

// Run records the call and returns the matching response.
func (runner *fixtureRunner) Run(
	ctx context.Context, command Command,
) (int, error) {
	line := strings.Join(
		append([]string{command.Name}, command.Arguments...), " ")
	runner.calls = append(runner.calls, line)
	response, present := runner.responses[line]
	if !present {
		return 1, nil
	}
	if command.Stdout != nil && response.stdout != "" {
		io.WriteString(command.Stdout, response.stdout)
	}
	if command.Stderr != nil && response.stderr != "" {
		io.WriteString(command.Stderr, response.stderr)
	}
	return response.status, nil
}

// newTestDriver returns a driver with a short readiness poll for
// fast test execution.
func newTestDriver(runner Runner) *Driver {
	driver := NewWithRunner(runner)
	driver.lookPath = func(file string) (string, error) {
		return "/opt/homebrew/bin/" + file, nil
	}
	driver.readinessTimeout = 30 * time.Millisecond
	driver.readinessInterval = time.Millisecond
	return driver
}

// TestListBoxesParsesFixture checks parsing of running and stopped
// boxes from recorded JSON output.
func TestListBoxesParsesFixture(t *testing.T) {
	runner := newFixtureRunner()
	runner.answerFile(t,
		"container ls --all --format json", "ls-all.json")
	boxes, err := newTestDriver(runner).ListBoxes(context.Background())
	if err != nil {
		t.Fatalf("ListBoxes: %v", err)
	}
	if len(boxes) != 2 {
		t.Fatalf("got %d boxes, want 2", len(boxes))
	}
	stopped := boxes[0]
	if stopped.Name != "site-ccf8def7f413.prison" {
		t.Errorf("first box is %q", stopped.Name)
	}
	if stopped.Running {
		t.Errorf("%s reads as running", stopped.Name)
	}
	if stopped.Address != "" {
		t.Errorf("stopped box has address %q", stopped.Address)
	}
	if stopped.Network != "prison" {
		t.Errorf("stopped box network is %q", stopped.Network)
	}
	if stopped.Image != "prison/box:84ab2e57f590" {
		t.Errorf("stopped box image is %q", stopped.Image)
	}
	running := boxes[1]
	if running.Name != "t5probe" || !running.Running {
		t.Fatalf("second box is %+v", running)
	}
	if running.Address != "192.168.128.4" {
		t.Errorf("running box address is %q", running.Address)
	}
	if running.Network != "prison" {
		t.Errorf("running box network is %q", running.Network)
	}
}

// TestBoxParsesInspectFixture checks that inspect output is parsed
// correctly and that a missing box is not an error.
func TestBoxParsesInspectFixture(t *testing.T) {
	runner := newFixtureRunner()
	runner.answerFile(t,
		"container inspect t5probe", "inspect-running.json")
	driver := newTestDriver(runner)

	info, err := driver.Box(context.Background(), "t5probe")
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if !info.Exists || !info.Running {
		t.Fatalf("running box reads as %+v", info)
	}
	if info.Address != "192.168.128.4" {
		t.Errorf("address is %q", info.Address)
	}
	if info.Image != "docker.io/library/debian:bookworm-slim" {
		t.Errorf("image is %q", info.Image)
	}

	runner.answerFile(t, "container inspect site-ccf8def7f413.prison",
		"inspect-stopped.json")
	stopped, err := driver.Box(
		context.Background(), "site-ccf8def7f413.prison")
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if !stopped.Exists || stopped.Running || stopped.Address != "" {
		t.Errorf("stopped box reads as %+v", stopped)
	}
	if stopped.Network != "prison" {
		t.Errorf("stopped box network is %q", stopped.Network)
	}

	missing, err := driver.Box(context.Background(), "absent")
	if err != nil {
		t.Fatalf("Box on an absent name: %v", err)
	}
	if missing.Exists || missing.Name != "absent" {
		t.Errorf("absent box reads as %+v", missing)
	}
}

// TestNetworkParsesFixture checks parsing of a network's host-only
// mode, gateway, and subnets from recorded output.
func TestNetworkParsesFixture(t *testing.T) {
	runner := newFixtureRunner()
	runner.answerFile(t, "container network inspect prison",
		"network-inspect-prison.json")
	runner.answerFile(t, "container network inspect default",
		"network-inspect-default.json")
	driver := newTestDriver(runner)

	prison, err := driver.Network(context.Background(), "prison")
	if err != nil {
		t.Fatalf("Network: %v", err)
	}
	if !prison.Exists || !prison.HostOnly {
		t.Fatalf("prison network reads as %+v", prison)
	}
	if prison.Gateway != "192.168.128.1" {
		t.Errorf("gateway is %q", prison.Gateway)
	}
	if prison.SubnetV4 != "192.168.128.0/24" {
		t.Errorf("ipv4 subnet is %q", prison.SubnetV4)
	}
	if prison.SubnetV6 != "fd53:f87a:1b9:1bf5::/64" {
		t.Errorf("ipv6 subnet is %q", prison.SubnetV6)
	}

	nat, err := driver.Network(context.Background(), "default")
	if err != nil {
		t.Fatalf("Network: %v", err)
	}
	if !nat.Exists || nat.HostOnly {
		t.Errorf("default network reads as %+v", nat)
	}
}

// TestEnsureNetworkRefusesNAT checks that a non-host-only network
// is refused.
func TestEnsureNetworkRefusesNAT(t *testing.T) {
	runner := newFixtureRunner()
	runner.answerFile(t, "container network inspect default",
		"network-inspect-default.json")
	err := newTestDriver(runner).EnsureNetwork(
		context.Background(), "default")
	if err == nil {
		t.Fatal("a nat network was accepted")
	}
	if !strings.Contains(err.Error(), "host-only") {
		t.Errorf("error does not mention host-only: %v", err)
	}
}

// TestEnsureNetworkCreatesMissing checks that the create command runs
// and the result is verified by reading it back.
func TestEnsureNetworkCreatesMissing(t *testing.T) {
	runner := newFixtureRunner()
	runner.answer("container network create --internal prison",
		fixtureResponse{})
	err := newTestDriver(runner).EnsureNetwork(
		context.Background(), "prison")
	if err == nil {
		t.Fatal("a network that never appeared was accepted")
	}
	if !containsCall(runner.calls,
		"container network create --internal prison") {
		t.Fatalf("create was not run; calls were %v", runner.calls)
	}
}

// TestCreateBuildsExpectedArgv pins the exact `container run`
// command line and checks readiness polling.
func TestCreateBuildsExpectedArgv(t *testing.T) {
	spec := cage.CreateSpec{
		Name:    "site-abc.prison",
		Image:   "prison/box:0da0a03e6e05",
		Network: "prison",
		CPUs:    4,
		Memory:  "4G",
		Mounts: []cage.Mount{
			{Source: "/host/work", Target: "/workspace"},
			{
				Source:   "/host/empty",
				Target:   "/workspace/.git/hooks",
				ReadOnly: true,
			},
		},
		Ports:        []cage.PortMapping{{Host: 18080, Guest: 8080}},
		Environment:  []string{"LANG=C.UTF-8", "PRISON_SUDO=yes"},
		Capabilities: []string{"NET_ADMIN"},
		Command:      []string{"sleep", "infinity"},
	}
	want := strings.Join([]string{
		"container run --detach --name site-abc.prison",
		"--network prison --cpus 4 --memory 4G",
		"--cap-add CAP_NET_ADMIN",
		"--volume /host/work:/workspace",
		"--mount type=bind,source=/host/empty," +
			"target=/workspace/.git/hooks,readonly",
		"--publish 127.0.0.1:18080:8080",
		"--env LANG=C.UTF-8 --env PRISON_SUDO=yes",
		"prison/box:0da0a03e6e05 sleep infinity",
	}, " ")

	runner := newFixtureRunner()
	runner.answer(want, fixtureResponse{})
	runner.answer("container exec site-abc.prison true",
		fixtureResponse{})
	if err := newTestDriver(runner).Create(
		context.Background(), spec); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if runner.calls[0] != want {
		t.Errorf("run command was\n  %s\nwant\n  %s",
			runner.calls[0], want)
	}
	if len(runner.calls) != 2 ||
		runner.calls[1] != "container exec site-abc.prison true" {
		t.Errorf("readiness was not polled; calls were %v",
			runner.calls)
	}
}

// TestCreateReportsAStoppedBox checks that a box that dies during
// startup reports its log output in the error.
func TestCreateReportsAStoppedBox(t *testing.T) {
	spec := cage.CreateSpec{
		Name: "doomed", Image: "prison/box:1", Network: "prison",
	}
	runner := newFixtureRunner()
	runner.answer(
		"container run --detach --name doomed --network prison "+
			"prison/box:1", fixtureResponse{})
	runner.answer("container inspect doomed", fixtureResponse{
		stdout: `[{"id":"doomed","status":{"state":"stopped"}}]`,
	})
	runner.answer("container logs doomed", fixtureResponse{
		stdout: "entrypoint: no such file\n",
	})
	err := newTestDriver(runner).Create(context.Background(), spec)
	if err == nil {
		t.Fatal("a box that stopped was accepted")
	}
	if !strings.Contains(err.Error(), "entrypoint: no such file") {
		t.Errorf("error does not quote the logs: %v", err)
	}
}

// TestExecBuildsExpectedArgv pins the exec command line for plain,
// TTY, and interactive modes.
func TestExecBuildsExpectedArgv(t *testing.T) {
	cases := []struct {
		name string
		spec cage.ExecSpec
		want string
	}{
		{
			name: "plain",
			spec: cage.ExecSpec{
				Box:         "site-abc.prison",
				Command:     []string{"true"},
				WorkDir:     "/workspace",
				UID:         501,
				GID:         501,
				Environment: []string{"TERM=xterm"},
			},
			want: "container exec --workdir /workspace " +
				"--uid 501 --gid 501 --env TERM=xterm " +
				"site-abc.prison true",
		},
		{
			name: "tty",
			spec: cage.ExecSpec{
				Box:     "site-abc.prison",
				Command: []string{"bash", "-l"},
				TTY:     true,
				UID:     0,
				GID:     0,
			},
			want: "container exec --tty --interactive " +
				"--uid 0 --gid 0 site-abc.prison bash -l",
		},
		{
			name: "interactive without a terminal",
			spec: cage.ExecSpec{
				Box:         "site-abc.prison",
				Command:     []string{"cat"},
				Interactive: true,
				UID:         501,
				GID:         20,
			},
			want: "container exec --interactive " +
				"--uid 501 --gid 20 site-abc.prison cat",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runner := newFixtureRunner()
			runner.answer(testCase.want, fixtureResponse{status: 3})
			status, err := newTestDriver(runner).Exec(
				context.Background(), testCase.spec)
			if err != nil {
				t.Fatalf("Exec: %v", err)
			}
			if status != 3 {
				t.Errorf("exit status is %d, want 3", status)
			}
			if runner.calls[0] != testCase.want {
				t.Errorf("exec command was\n  %s\nwant\n  %s",
					runner.calls[0], testCase.want)
			}
		})
	}
}

// TestBuildSortsArguments checks the build command line and that
// build args appear in a stable sorted order.
func TestBuildSortsArguments(t *testing.T) {
	var output bytes.Buffer
	want := "container build --tag prison/base:1 " +
		"--build-arg HOST_UID=501 --build-arg VARIANT=slim /tmp/ctx"
	runner := newFixtureRunner()
	runner.answer(want, fixtureResponse{stdout: "built\n"})
	err := newTestDriver(runner).Build(
		context.Background(), cage.BuildSpec{
			Tag:     "prison/base:1",
			Context: "/tmp/ctx",
			Args:    map[string]string{"VARIANT": "slim", "HOST_UID": "501"},
			Output:  &output,
		})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if runner.calls[0] != want {
		t.Errorf("build command was\n  %s\nwant\n  %s",
			runner.calls[0], want)
	}
	if output.String() != "built\n" {
		t.Errorf("build output is %q", output.String())
	}
}

// TestDeleteFallsBackToRemove checks that `rm` is tried when
// `delete` fails.
func TestDeleteFallsBackToRemove(t *testing.T) {
	runner := newFixtureRunner()
	runner.answer("container rm gone", fixtureResponse{})
	if err := newTestDriver(runner).Delete(
		context.Background(), "gone"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(runner.calls) != 2 ||
		runner.calls[0] != "container delete gone" ||
		runner.calls[1] != "container rm gone" {
		t.Errorf("calls were %v", runner.calls)
	}
}

// TestImageExistsReadsExitStatus checks that image presence is
// determined by exit status.
func TestImageExistsReadsExitStatus(t *testing.T) {
	runner := newFixtureRunner()
	runner.answer("container image inspect prison/base:1",
		fixtureResponse{})
	driver := newTestDriver(runner)
	present, err := driver.ImageExists(
		context.Background(), "prison/base:1")
	if err != nil || !present {
		t.Errorf("present tag reads as %v, %v", present, err)
	}
	absent, err := driver.ImageExists(
		context.Background(), "prison/base:2")
	if err != nil || absent {
		t.Errorf("absent tag reads as %v, %v", absent, err)
	}
}

// TestDNSExistsReadsFixture checks that the domain list is parsed
// from JSON output.
func TestDNSExistsReadsFixture(t *testing.T) {
	runner := newFixtureRunner()
	runner.answerFile(t,
		"container system dns list --format json", "dns-list.json")
	helper := newTestDriver(runner).DNS()
	registered, err := helper.Exists(context.Background(), "prison")
	if err != nil || !registered {
		t.Errorf("prison reads as %v, %v", registered, err)
	}
	other, err := helper.Exists(context.Background(), "elsewhere")
	if err != nil || other {
		t.Errorf("elsewhere reads as %v, %v", other, err)
	}
}

// TestDoctorPrintsSections checks that the doctor report includes
// expected section headings and indented output.
func TestDoctorPrintsSections(t *testing.T) {
	runner := newFixtureRunner()
	runner.answer("container --version",
		fixtureResponse{stdout: "container CLI version 1.2.0\n"})
	var report bytes.Buffer
	newTestDriver(runner).Doctor(context.Background(), &report)
	text := report.String()
	for _, heading := range []string{
		"container  /opt/homebrew/bin/container",
		"  version", "    container CLI version 1.2.0",
		"  system status", "  container ls --all",
		"  networks", "  dns domains",
	} {
		if !strings.Contains(text, heading) {
			t.Errorf("report is missing %q:\n%s", heading, text)
		}
	}
}

// containsCall reports whether the call list contains the given
// command line.
func containsCall(calls []string, wanted string) bool {
	for _, line := range calls {
		if line == wanted {
			return true
		}
	}
	return false
}
