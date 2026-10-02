//go:build integration && linux

package firecracker

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"prison/internal/cage"
)

func TestIntegrationImageChainAndDisk(t *testing.T) {
	driver := New(t.TempDir(), nil)
	if _, err := driver.builderAddress(); err != nil {
		t.Skip(err)
	}
	if _, err := exec.LookPath("debugfs"); err != nil {
		t.Skip("debugfs is not on PATH")
	}
	ctx := context.Background()
	var output bytes.Buffer
	baseContext := t.TempDir()
	writeFile(t, filepath.Join(baseContext, "Dockerfile"), `
FROM debian:bookworm-slim
RUN useradd -m -u 501 dev && install -o 501 -g 501 -d /home/dev/data
`)
	inmateContext := t.TempDir()
	writeFile(t, filepath.Join(inmateContext, "Dockerfile"), `
ARG PRISON_BASE
FROM ${PRISON_BASE}
RUN echo inmate > /inmate.txt
`)
	steps := []cage.BuildSpec{
		{Tag: "prison-base:integration", Context: baseContext},
		{
			Tag:     "prison-box:integration",
			Context: inmateContext,
			Args:    map[string]string{"PRISON_BASE": "prison-base:integration"},
		},
	}
	for _, step := range steps {
		step.Output = &output
		if err := driver.Build(ctx, step); err != nil {
			t.Fatalf("Build(%s) = %v, want nil\n%s", step.Tag, err,
				output.String())
		}
	}
	disk, err := driver.ensureDisk(ctx, "prison-box:integration", &output)
	if err != nil {
		t.Fatalf("ensureDisk = %v, want nil\n%s", err, output.String())
	}
	listing := debugfs(t, disk, "ls -l /home/dev")
	if !strings.Contains(listing, " 501 ") || !strings.Contains(listing, "data") {
		t.Errorf("/home/dev listing = %q, want data owned by 501", listing)
	}
	content := debugfs(t, disk, "cat /inmate.txt")
	if strings.TrimSpace(content) != "inmate" {
		t.Errorf("/inmate.txt = %q, want inmate", content)
	}
}

func debugfs(t *testing.T, disk, request string) string {
	t.Helper()
	command := exec.Command("debugfs", "-R", request, disk)
	command.Env = append(os.Environ(), "DEBUGFS_PAGER=cat")
	result, err := command.Output()
	if err != nil {
		t.Fatalf("debugfs %q = %v, want nil", request, err)
	}
	return string(result)
}

// TestIntegrationBoxLifecycle needs sudo, `make guest`, and `make kernel`.
func TestIntegrationBoxLifecycle(t *testing.T) {
	repository, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	kernel := os.DirFS(filepath.Join(repository, "images", "kernel"))
	state, err := os.MkdirTemp("/tmp", "prison-cage-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(state) })
	driver := New(state, kernel)
	if err := driver.Require(context.Background()); err != nil {
		t.Skip(err)
	}
	helper := filepath.Join(repository, "bin", "prison")
	if _, err := os.Stat(helper); err != nil {
		t.Skip("run `make` first")
	}
	driver.helperSource = func() (string, error) { return helper, nil }
	share, err := os.MkdirTemp("/tmp", "prison-share-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(share) })
	writeFile(t, filepath.Join(share, "from-host.txt"), "host data")
	if _, err := driver.kernelPath(); err != nil {
		t.Skip(err)
	}
	ctx := context.Background()
	if err := driver.EnsureNetwork(ctx, "prison-it"); err != nil {
		t.Fatalf("EnsureNetwork = %v, want nil", err)
	}
	var output bytes.Buffer
	base := cage.BuildSpec{
		Tag:     "prison-base:integration",
		Context: filepath.Join(repository, "images", "base"),
		Args:    map[string]string{"UID": "501"},
		Output:  &output,
	}
	if err := driver.Build(ctx, base); err != nil {
		t.Fatalf("Build = %v, want nil\n%s", err, output.String())
	}
	name := "prison-it-box"
	t.Cleanup(func() { driver.Delete(context.Background(), name) })
	spec := cage.CreateSpec{
		Name:    name,
		Image:   base.Tag,
		Network: "prison-it",
		CPUs:    4,
		Memory:  "1G",
		Environment: []string{"PRISON_TOKEN=integration",
			"PRISON_BROKER_PORT=8787"},
		Command: []string{"sleep", "infinity"},
		Mounts:  []cage.Mount{{Source: share, Target: "/workspace"}},
	}
	started := time.Now()
	if err := driver.Create(ctx, spec); err != nil {
		t.Fatalf("Create = %v, want nil", err)
	}
	t.Logf("Create took %s", time.Since(started).Round(time.Millisecond))
	started = time.Now()
	// The first exec uses the share before the guest firewall starts, like a
	// session.
	assertShareWorks(t, driver, name, share)
	t.Logf("the first exec took %s", time.Since(started).Round(time.Millisecond))
	info, err := driver.Box(ctx, name)
	if err != nil || !info.Running || info.Address == "" {
		t.Fatalf("Box = %+v, %v, want a running box with an address",
			info, err)
	}
	var logs bytes.Buffer
	for range 60 {
		logs.Reset()
		driver.Logs(ctx, name, &logs)
		if strings.Contains(logs.String(), "egress restricted") {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !strings.Contains(logs.String(), "egress restricted") {
		t.Errorf("Logs = %q, want \"egress restricted\"", logs.String())
	}
	assertExecWorks(t, driver, name)
	if err := driver.Stop(ctx, name); err != nil {
		t.Fatalf("Stop = %v, want nil", err)
	}
	if info, _ := driver.Box(ctx, name); info.Running || !info.Exists {
		t.Errorf("Box after Stop = %+v, want stopped", info)
	}
	if err := driver.Start(ctx, name); err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}
	boxes, err := driver.ListBoxes(ctx)
	if err != nil || len(boxes) != 1 || !boxes[0].Running {
		t.Errorf("ListBoxes = %+v, %v, want one running box", boxes, err)
	}
	if err := driver.Delete(ctx, name); err != nil {
		t.Fatalf("Delete = %v, want nil", err)
	}
	if info, _ := driver.Box(ctx, name); info.Exists {
		t.Errorf("Box after Delete = %+v, want gone", info)
	}
}

func assertExecWorks(t *testing.T, driver *Driver, name string) {
	t.Helper()
	ctx := context.Background()
	run := func(spec cage.ExecSpec) (int, string) {
		var stdout bytes.Buffer
		spec.Box, spec.Stdout, spec.Stderr = name, &stdout, &stdout
		if spec.Stdin == nil {
			spec.Stdin = strings.NewReader("")
		}
		status, err := driver.Exec(ctx, spec)
		if err != nil {
			t.Fatalf("Exec(%q) = %v, want nil", spec.Command, err)
		}
		return status, stdout.String()
	}
	status, output := run(cage.ExecSpec{
		Command: []string{"sh", "-c", "id -u; echo $HOME; pwd"},
		WorkDir: "/workspace", UID: 501, GID: 501,
	})
	if status != 0 || output != "501\n/home/dev\n/workspace\n" {
		t.Errorf("id = %d, %q, want 501, /home/dev, /workspace", status,
			output)
	}
	status, output = run(cage.ExecSpec{
		Command: []string{"cat"}, Interactive: true,
		Stdin: strings.NewReader("from the host"),
	})
	if status != 0 || output != "from the host" {
		t.Errorf("cat = %d, %q, want 0, \"from the host\"", status, output)
	}
	status, _ = run(cage.ExecSpec{Command: []string{"sh", "-c", "exit 7"}})
	if status != 7 {
		t.Errorf("exit 7 = %d, want 7", status)
	}
	status, output = run(cage.ExecSpec{
		Command: []string{"sh", "-c", "test -t 0 && echo terminal"},
		TTY:     true,
	})
	if status != 0 || !strings.Contains(output, "terminal") {
		t.Errorf("tty = %d, %q, want a terminal", status, output)
	}
	_, err := driver.Exec(ctx, cage.ExecSpec{Box: name,
		Command: []string{"no-such-command"}, Stdin: strings.NewReader(""),
		Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "command not found") {
		t.Errorf("Exec(no-such-command) = %v, want \"command not found\"",
			err)
	}
}

func assertShareWorks(t *testing.T, driver *Driver, name, share string) {
	t.Helper()
	var output bytes.Buffer
	status, err := driver.Exec(context.Background(), cage.ExecSpec{
		Box: name, UID: 501, GID: 501, WorkDir: "/workspace",
		Command: []string{"sh", "-c",
			"cat from-host.txt && echo box data > from-box.txt"},
		Stdin: strings.NewReader(""), Stdout: &output, Stderr: &output,
	})
	if err != nil || status != 0 || output.String() != "host data" {
		t.Fatalf("Exec = %d, %q, %v, want 0, \"host data\", nil", status,
			output.String(), err)
	}
	content, err := os.ReadFile(filepath.Join(share, "from-box.txt"))
	if err != nil || string(content) != "box data\n" {
		t.Errorf("from-box.txt = %q, %v, want \"box data\\n\"", content, err)
	}
	info, err := os.Stat(filepath.Join(share, "from-box.txt"))
	if err == nil {
		owner := info.Sys().(*syscall.Stat_t).Uid
		if int(owner) != os.Getuid() {
			t.Errorf("from-box.txt owner = %d, want %d", owner,
				os.Getuid())
		}
	}
}
