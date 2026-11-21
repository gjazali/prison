package session

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"

	"prison/internal/config"
	"prison/internal/isolator"
)

func TestCappedBufferDropsExtraOutput(t *testing.T) {
	buffer := &cappedBuffer{}
	chunk := bytes.Repeat([]byte("x"), guestOutputLimit-1)
	for range 2 {
		count, err := buffer.Write(chunk)
		if count != len(chunk) || err != nil {
			t.Fatalf("Write = %d, %v, want %d, nil", count, err, len(chunk))
		}
	}
	if buffer.Len() != guestOutputLimit {
		t.Errorf("Len = %d, want %d", buffer.Len(), guestOutputLimit)
	}
}

type recordingExecIsolator struct {
	isolator.Isolator
	specs []isolator.ExecSpec
}

func (r *recordingExecIsolator) Exec(_ context.Context, spec isolator.ExecSpec) (int, error) {
	r.specs = append(r.specs, spec)
	if len(spec.Command) >= 2 && spec.Command[0] == GuestBinary && spec.Command[1] == "probe" {
		if spec.Stdout != nil {
			_, _ = spec.Stdout.Write([]byte(`{"commands":{},"terminfo":true}`))
		}
		return 0, nil
	}
	return 0, nil
}

func TestRunShellWithoutTTY(t *testing.T) {
	fake := &recordingExecIsolator{}
	s := &Session{
		Environment: &Environment{Isolator: fake},
		BoxName:     "proj.prison",
	}
	status, err := s.runShell(context.Background(), "mise use -g node@lts", []string{"A=1"}, false)
	if err != nil || status != 0 {
		t.Fatalf("runShell = %d, %v, want 0, nil", status, err)
	}
	if len(fake.specs) != 1 {
		t.Fatalf("Exec calls = %d, want 1", len(fake.specs))
	}
	spec := fake.specs[0]
	if spec.TTY || spec.Interactive {
		t.Errorf("TTY=%v Interactive=%v, want false, false", spec.TTY, spec.Interactive)
	}
	if spec.Stdin != nil {
		t.Errorf("Stdin = %v, want nil", spec.Stdin)
	}
	if spec.Resizes != nil {
		t.Errorf("Resizes = %v, want nil", spec.Resizes)
	}
	if len(spec.Command) != 3 || spec.Command[2] != "mise use -g node@lts" {
		t.Errorf("Command = %q, want bash -lc mise use -g node@lts", spec.Command)
	}
}

func TestRunShellWithTTY(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("this host has no pty support")
	}
	fake := &recordingExecIsolator{}
	s := &Session{
		Environment: &Environment{Isolator: fake},
		BoxName:     "proj.prison",
	}
	status, err := s.runShell(context.Background(), "mise use -g node@lts", []string{"TERM=xterm-256color"}, true)
	if err != nil || status != 0 {
		t.Fatalf("runShell = %d, %v, want 0, nil", status, err)
	}
	if len(fake.specs) != 1 {
		t.Fatalf("Exec calls = %d, want 1", len(fake.specs))
	}
	spec := fake.specs[0]
	if !spec.TTY || spec.Interactive {
		t.Errorf("TTY=%v Interactive=%v, want true, false", spec.TTY, spec.Interactive)
	}
	if spec.Stdin == nil {
		t.Fatal("Stdin = nil, want lent pty")
	}
	if spec.Resizes == nil {
		t.Fatal("Resizes = nil, want resize channel")
	}
	if len(spec.Command) != 3 ||
		!strings.HasPrefix(spec.Command[2], "stty rows ") ||
		!strings.HasSuffix(spec.Command[2], "; mise use -g node@lts") {
		t.Errorf("Command = %q, want stty prefix + setup line", spec.Command)
	}
}

func TestRunSetupSetsTermWhenStdoutIsTerminal(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("this host has no pty support")
	}
	previous := stdoutIsTerminal
	stdoutIsTerminal = func() bool { return true }
	t.Cleanup(func() { stdoutIsTerminal = previous })
	t.Setenv("TERM", "xterm-256color")

	fake := &recordingExecIsolator{}
	s := &Session{
		Environment: &Environment{Isolator: fake},
		BoxName:     "proj.prison",
		Config: &config.Project{
			Setup: config.ProjectSetup{Commands: []string{"echo hi"}},
		},
	}
	if err := s.runSetup(context.Background(), true); err != nil {
		t.Fatalf("runSetup: %v", err)
	}
	if len(fake.specs) != 2 {
		t.Fatalf("Exec calls = %d, want 2 (probe + setup)", len(fake.specs))
	}
	setupSpec := fake.specs[1]
	if !setupSpec.TTY {
		t.Errorf("setup TTY = false, want true")
	}
	foundTerm := false
	for _, entry := range setupSpec.Environment {
		if entry == "TERM=xterm-256color" {
			foundTerm = true
		}
	}
	if !foundTerm {
		t.Errorf("setup Environment = %v, want TERM=xterm-256color", setupSpec.Environment)
	}
}
