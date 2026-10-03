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

	"prison/internal/isolator"
)

type fixtureResponse struct {
	stdout string
	stderr string
	status int
}

// fixtureRunner answers an unknown command with exit status 1.
type fixtureRunner struct {
	responses map[string]fixtureResponse
	calls     []string
}

func newFixtureRunner() *fixtureRunner {
	return &fixtureRunner{responses: map[string]fixtureResponse{}}
}

func (runner *fixtureRunner) answer(
	line string, response fixtureResponse,
) {
	runner.responses[line] = response
}

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

func newTestDriver(runner Runner) *Driver {
	driver := NewWithRunner(runner)
	driver.lookPath = func(file string) (string, error) {
		return "/opt/homebrew/bin/" + file, nil
	}
	driver.readinessTimeout = 30 * time.Millisecond
	driver.readinessInterval = time.Millisecond
	return driver
}

func TestListBoxesParsesFixture(t *testing.T) {
	runner := newFixtureRunner()
	runner.answerFile(t,
		"container ls --all --format json", "ls-all.json")
	boxes, err := newTestDriver(runner).ListBoxes(context.Background())
	if err != nil {
		t.Fatalf("ListBoxes: %v", err)
	}
	if len(boxes) != 2 {
		t.Fatalf("len(ListBoxes) = %d, want 2", len(boxes))
	}
	stopped := boxes[0]
	if stopped.Name != "site-ccf8def7f413.prison" {
		t.Errorf("boxes[0].Name = %q, want site-ccf8def7f413.prison", stopped.Name)
	}
	if stopped.Running {
		t.Error("boxes[0].Running = true, want false")
	}
	if stopped.Address != "" {
		t.Errorf("boxes[0].Address = %q, want empty", stopped.Address)
	}
	if stopped.Network != "prison" {
		t.Errorf("boxes[0].Network = %q, want prison", stopped.Network)
	}
	if stopped.Image != "prison/box:84ab2e57f590" {
		t.Errorf("boxes[0].Image = %q, want prison/box:84ab2e57f590", stopped.Image)
	}
	running := boxes[1]
	if running.Name != "t5probe" || !running.Running {
		t.Fatalf("boxes[1] = %+v, want running t5probe", running)
	}
	if running.Address != "192.168.128.4" {
		t.Errorf("boxes[1].Address = %q, want 192.168.128.4", running.Address)
	}
	if running.Network != "prison" {
		t.Errorf("boxes[1].Network = %q, want prison", running.Network)
	}
}

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
		t.Fatalf("Box = %+v, want running", info)
	}
	if info.Address != "192.168.128.4" {
		t.Errorf("Address = %q, want 192.168.128.4", info.Address)
	}
	if info.Image != "docker.io/library/debian:bookworm-slim" {
		t.Errorf("Image = %q, want docker.io/library/debian:bookworm-slim",
			info.Image)
	}

	runner.answerFile(t, "container inspect site-ccf8def7f413.prison",
		"inspect-stopped.json")
	stopped, err := driver.Box(
		context.Background(), "site-ccf8def7f413.prison")
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if !stopped.Exists || stopped.Running || stopped.Address != "" {
		t.Errorf("Box = %+v, want stopped", stopped)
	}
	if stopped.Network != "prison" {
		t.Errorf("Network = %q, want prison", stopped.Network)
	}

	missing, err := driver.Box(context.Background(), "absent")
	if err != nil {
		t.Fatalf("Box(absent): %v", err)
	}
	if missing.Exists || missing.Name != "absent" {
		t.Errorf("Box(absent) = %+v, want missing", missing)
	}
}

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
		t.Fatalf("Network(prison) = %+v, want host-only", prison)
	}
	if prison.Gateway != "192.168.128.1" {
		t.Errorf("Gateway = %q, want 192.168.128.1", prison.Gateway)
	}
	if prison.SubnetV4 != "192.168.128.0/24" {
		t.Errorf("SubnetV4 = %q, want 192.168.128.0/24", prison.SubnetV4)
	}
	if prison.SubnetV6 != "fd53:f87a:1b9:1bf5::/64" {
		t.Errorf("SubnetV6 = %q, want fd53:f87a:1b9:1bf5::/64", prison.SubnetV6)
	}

	nat, err := driver.Network(context.Background(), "default")
	if err != nil {
		t.Fatalf("Network: %v", err)
	}
	if !nat.Exists || nat.HostOnly {
		t.Errorf("Network(default) = %+v, want NAT", nat)
	}
}

func TestEnsureNetworkRefusesNAT(t *testing.T) {
	runner := newFixtureRunner()
	runner.answerFile(t, "container network inspect default",
		"network-inspect-default.json")
	err := newTestDriver(runner).EnsureNetwork(
		context.Background(), "default")
	if err == nil {
		t.Fatal("EnsureNetwork = nil, want error")
	}
	if !strings.Contains(err.Error(), "host-only") {
		t.Errorf("EnsureNetwork = %v, want a host-only error", err)
	}
}

func TestEnsureNetworkCreatesMissing(t *testing.T) {
	runner := newFixtureRunner()
	runner.answer("container network create --internal prison",
		fixtureResponse{})
	err := newTestDriver(runner).EnsureNetwork(
		context.Background(), "prison")
	if err == nil {
		t.Fatal("EnsureNetwork = nil, want error")
	}
	if !containsCall(runner.calls,
		"container network create --internal prison") {
		t.Fatalf("calls = %v, want network create", runner.calls)
	}
}

func TestCreateBuildsExpectedArgv(t *testing.T) {
	spec := isolator.CreateSpec{
		Name:    "site-abc.prison",
		Image:   "prison/box:0da0a03e6e05",
		Network: "prison",
		CPUs:    4,
		Memory:  "4G",
		Mounts: []isolator.Mount{
			{Source: "/host/work", Target: "/workspace"},
			{
				Source:   "/host/empty",
				Target:   "/workspace/.git/hooks",
				ReadOnly: true,
			},
		},
		Ports:        []isolator.PortMapping{{Host: 18080, Guest: 8080}},
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
		t.Errorf("run command =\n  %s\nwant\n  %s",
			runner.calls[0], want)
	}
	if len(runner.calls) != 2 ||
		runner.calls[1] != "container exec site-abc.prison true" {
		t.Errorf("calls = %v, want a readiness poll", runner.calls)
	}
}

func TestCreateReportsAStoppedBox(t *testing.T) {
	spec := isolator.CreateSpec{
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
		t.Fatal("Create = nil, want error")
	}
	if !strings.Contains(err.Error(), "entrypoint: no such file") {
		t.Errorf("Create = %v, want the logs", err)
	}
}

func TestExecBuildsExpectedArgv(t *testing.T) {
	cases := []struct {
		name string
		spec isolator.ExecSpec
		want string
	}{
		{
			name: "plain",
			spec: isolator.ExecSpec{
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
			spec: isolator.ExecSpec{
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
			spec: isolator.ExecSpec{
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
				t.Errorf("Exec = %d, want 3", status)
			}
			if runner.calls[0] != testCase.want {
				t.Errorf("exec command =\n  %s\nwant\n  %s",
					runner.calls[0], testCase.want)
			}
		})
	}
}

func TestBuildSortsArguments(t *testing.T) {
	var output bytes.Buffer
	want := "container build --tag prison/base:1 " +
		"--build-arg HOST_UID=501 --build-arg VARIANT=slim /tmp/ctx"
	runner := newFixtureRunner()
	runner.answer(want, fixtureResponse{stdout: "built\n"})
	err := newTestDriver(runner).Build(
		context.Background(), isolator.BuildSpec{
			Tag:     "prison/base:1",
			Context: "/tmp/ctx",
			Args:    map[string]string{"VARIANT": "slim", "HOST_UID": "501"},
			Output:  &output,
		})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if runner.calls[0] != want {
		t.Errorf("build command =\n  %s\nwant\n  %s",
			runner.calls[0], want)
	}
	if output.String() != "built\n" {
		t.Errorf("build output = %q, want \"built\\n\"", output.String())
	}
}

func TestReleaseBuilderStopsTheBuilder(t *testing.T) {
	runner := newFixtureRunner()
	runner.answer("container builder stop", fixtureResponse{})
	if err := newTestDriver(runner).ReleaseBuilder(
		context.Background()); err != nil {
		t.Fatalf("ReleaseBuilder: %v", err)
	}
	if len(runner.calls) != 1 ||
		runner.calls[0] != "container builder stop" {
		t.Errorf("calls = %v", runner.calls)
	}
}

func TestReleaseBuilderIgnoresAStoppedBuilder(t *testing.T) {
	runner := newFixtureRunner()
	if err := newTestDriver(runner).ReleaseBuilder(
		context.Background()); err != nil {
		t.Errorf("ReleaseBuilder: %v", err)
	}
}

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
		t.Errorf("calls = %v", runner.calls)
	}
}

func TestImageExistsReadsExitStatus(t *testing.T) {
	runner := newFixtureRunner()
	runner.answer("container image inspect prison/base:1",
		fixtureResponse{})
	driver := newTestDriver(runner)
	present, err := driver.ImageExists(
		context.Background(), "prison/base:1")
	if err != nil || !present {
		t.Errorf("ImageExists(prison/base:1) = %v, %v, want true", present, err)
	}
	absent, err := driver.ImageExists(
		context.Background(), "prison/base:2")
	if err != nil || absent {
		t.Errorf("ImageExists(prison/base:2) = %v, %v, want false", absent, err)
	}
}

func TestDNSExistsReadsFixture(t *testing.T) {
	runner := newFixtureRunner()
	runner.answerFile(t,
		"container system dns list --format json", "dns-list.json")
	helper := newTestDriver(runner).DNS()
	registered, err := helper.Exists(context.Background(), "prison")
	if err != nil || !registered {
		t.Errorf("Exists(prison) = %v, %v, want true", registered, err)
	}
	other, err := helper.Exists(context.Background(), "elsewhere")
	if err != nil || other {
		t.Errorf("Exists(elsewhere) = %v, %v, want false", other, err)
	}
}

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

func containsCall(calls []string, wanted string) bool {
	for _, line := range calls {
		if line == wanted {
			return true
		}
	}
	return false
}
