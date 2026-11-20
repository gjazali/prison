package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"prison/internal/state"
)

func TestInmateUpdateFlagsAndRevisionBumping(t *testing.T) {
	stateDir := t.TempDir()
	projectDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRISON_ROOT", stateDir)
	t.Chdir(projectDir)

	for _, args := range [][]string{
		{"inmate", "update"},
		{"inmate", "update", "--all", "claude-code"},
		{"inmate", "update", "--all", "--ref", "main"},
		{"inmate", "update", "claude-code", "--ref", "main"},
	} {
		cmd := newRootCommand()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Errorf("args %v: Execute = nil, want error", args)
		}
	}

	// Updating a bundled inmate not enabled in the current project bumps its revision.
	var out bytes.Buffer
	cmd := newRootCommand()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"inmate", "update", "claude"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("inmate update claude: %v", err)
	}
	if !strings.Contains(out.String(), "updated claude-code") {
		t.Fatalf("output = %q", out.String())
	}

	root, err := state.OpenRoot(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := root.InmateRevisions()
	if err != nil || revisions["claude-code"] != 1 {
		t.Fatalf("revisions = %v, %v, want claude-code=1", revisions, err)
	}
}

func TestInmateUpdateAllOnlyUpdatesCurrentProjectUnpinnedInmates(t *testing.T) {
	stateDir := t.TempDir()
	projectDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRISON_ROOT", stateDir)
	t.Setenv("PRISON_INMATES", "claude-code:1.0.50 opencode")
	t.Setenv("PRISON_IMAGE", "custom:test")
	t.Chdir(projectDir)

	var out bytes.Buffer
	cmd := newRootCommand()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"inmate", "update", "--all"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("inmate update --all: %v", err)
	}
	if !strings.Contains(out.String(), "the claude-code inmate is pinned to 1.0.50") {
		t.Fatalf("output = %q, want pinned note", out.String())
	}

	root, err := state.OpenRoot(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := root.InmateRevisions()
	if err != nil {
		t.Fatal(err)
	}
	if revisions["opencode"] != 1 {
		t.Errorf("opencode revision = %d, want 1", revisions["opencode"])
	}
	if revisions["claude-code"] != 0 {
		t.Errorf("pinned claude-code revision = %d, want 0", revisions["claude-code"])
	}
	if revisions["antigravity-cli"] != 0 {
		t.Errorf("unused antigravity-cli revision = %d, want 0", revisions["antigravity-cli"])
	}
}

func TestInmateUpdateAllRefreshesMovingTags(t *testing.T) {
	stateDir := t.TempDir()
	projectDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRISON_ROOT", stateDir)
	t.Setenv("PRISON_INMATES", "claude-code:stable opencode:0.2.3")
	t.Setenv("PRISON_IMAGE", "custom:test")
	t.Chdir(projectDir)

	var out bytes.Buffer
	cmd := newRootCommand()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"inmate", "update", "--all"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("inmate update --all: %v", err)
	}
	if !strings.Contains(out.String(), "the opencode inmate is pinned to 0.2.3") {
		t.Fatalf("output = %q, want opencode pinned note", out.String())
	}

	root, err := state.OpenRoot(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := root.InmateRevisions()
	if err != nil {
		t.Fatal(err)
	}
	if revisions["claude-code"] != 1 {
		t.Errorf("moving tag claude-code:stable revision = %d, want 1", revisions["claude-code"])
	}
	if revisions["opencode"] != 0 {
		t.Errorf("pinned opencode:0.2.3 revision = %d, want 0", revisions["opencode"])
	}
}
