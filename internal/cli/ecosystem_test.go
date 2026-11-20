package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"prison/internal/config"
)

func TestEcosystemUpdateListShowAndInit(t *testing.T) {
	stateDir := t.TempDir()
	catalogDir := t.TempDir()
	projectDir := filepath.Join(t.TempDir(), "webapp")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(catalogDir, "ecosystems"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catalogDir, "ecosystems", "pnpm.toml"), []byte(`
name = "pnpm"
description = "JavaScript / TypeScript (pnpm)"
group = "javascript-pkg"
priority = 30

[detect]
files = ["pnpm-lock.yaml"]

[box]
shadow = ["node_modules"]

[setup]
commands = ["pnpm install --frozen-lockfile"]

[checkpoint]
ignore = [".pnpm-store/"]

[egress]
hosts = ["registry.npmjs.org", "*.npmjs.org"]
docs = ["pnpm.io"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "pnpm-lock.yaml"),
		[]byte("lockfileVersion: 9\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PRISON_ROOT", stateDir)
	t.Chdir(projectDir)

	// Without a catalog, init guides the user to run update.
	initMissing := newRootCommand()
	initMissing.SetArgs([]string{"ecosystem", "init"})
	if err := initMissing.Execute(); err == nil || !strings.Contains(err.Error(), "prison ecosystem update") {
		t.Fatalf("init without catalog = %v", err)
	}

	// Update from local catalog directory.
	var out bytes.Buffer
	updateCmd := newRootCommand()
	updateCmd.SetOut(&out)
	updateCmd.SetArgs([]string{"ecosystem", "update", catalogDir})
	if err := updateCmd.Execute(); err != nil {
		t.Fatalf("ecosystem update: %v", err)
	}
	if !strings.Contains(out.String(), "loaded 1 ecosystem(s)") {
		t.Fatalf("update output = %q", out.String())
	}

	// List and show.
	out.Reset()
	listCmd := newRootCommand()
	listCmd.SetOut(&out)
	listCmd.SetArgs([]string{"ecosystem", "list"})
	if err := listCmd.Execute(); err != nil {
		t.Fatalf("ecosystem list: %v", err)
	}
	if !strings.Contains(out.String(), "pnpm") {
		t.Fatalf("list output = %q", out.String())
	}

	out.Reset()
	showCmd := newRootCommand()
	showCmd.SetOut(&out)
	showCmd.SetArgs([]string{"ecosystem", "show", "pnpm"})
	if err := showCmd.Execute(); err != nil {
		t.Fatalf("ecosystem show: %v", err)
	}
	if !strings.Contains(out.String(), "pnpm install --frozen-lockfile") {
		t.Fatalf("show output = %q", out.String())
	}

	// Dry-run with --no-docs omits documentation hosts.
	out.Reset()
	noDocsCmd := newRootCommand()
	noDocsCmd.SetOut(&out)
	noDocsCmd.SetArgs([]string{"ecosystem", "init", "--dry-run", "--no-docs"})
	if err := noDocsCmd.Execute(); err != nil {
		t.Fatalf("ecosystem init --dry-run --no-docs: %v", err)
	}
	if strings.Contains(out.String(), "pnpm.io") {
		t.Fatalf("--no-docs output included doc host: %s", out.String())
	}

	// Dry-run does not write prison.toml.
	out.Reset()
	dryCmd := newRootCommand()
	dryCmd.SetOut(&out)
	dryCmd.SetArgs([]string{"ecosystem", "init", "--dry-run"})
	if err := dryCmd.Execute(); err != nil {
		t.Fatalf("ecosystem init --dry-run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "prison.toml")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote prison.toml")
	}

	// Apply with --yes writes valid prison.toml.
	out.Reset()
	applyCmd := newRootCommand()
	applyCmd.SetOut(&out)
	applyCmd.SetArgs([]string{"ecosystem", "init", "--yes"})
	if err := applyCmd.Execute(); err != nil {
		t.Fatalf("ecosystem init --yes: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(projectDir, "prison.toml"))
	if err != nil {
		t.Fatalf("read prison.toml: %v", err)
	}
	loaded, err := config.LoadProject("prison.toml", data)
	if err != nil {
		t.Fatalf("LoadProject: %v\n%s", err, data)
	}
	if len(loaded.Box.Shadow) != 1 || loaded.Box.Shadow[0] != "node_modules" {
		t.Fatalf("loaded.Box.Shadow = %v", loaded.Box.Shadow)
	}

	// Second run reports already up to date.
	out.Reset()
	againCmd := newRootCommand()
	againCmd.SetOut(&out)
	againCmd.SetArgs([]string{"ecosystem", "init", "--yes"})
	if err := againCmd.Execute(); err != nil {
		t.Fatalf("second ecosystem init: %v", err)
	}
	if !strings.Contains(out.String(), "already includes") {
		t.Fatalf("second init output = %q", out.String())
	}
}
