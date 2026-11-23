package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"prison/internal/state"
	"prison/internal/vault"
)

func TestInmateSyncFlags(t *testing.T) {
	stateDir := t.TempDir()
	projectDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRISON_ROOT", stateDir)
	t.Setenv("PRISON_INMATES", "claude-code")
	t.Chdir(projectDir)

	for _, args := range [][]string{
		{"inmate", "sync"},
		{"inmate", "sync", "--all", "claude-code"},
	} {
		cmd := newRootCommand()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Errorf("args %v: Execute = nil, want error", args)
		}
	}
}

func TestInmateSyncNoEnabledInmates(t *testing.T) {
	stateDir := t.TempDir()
	projectDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRISON_ROOT", stateDir)
	t.Setenv("PRISON_INMATES", "")
	t.Chdir(projectDir)

	var out bytes.Buffer
	cmd := newRootCommand()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"inmate", "sync", "--all"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("inmate sync --all: %v", err)
	}
	if !strings.Contains(out.String(), "no inmates enabled in this project") {
		t.Fatalf("output = %q, want no inmates message", out.String())
	}
}

func TestInmateSyncAllAndSingleInmateWithMirroring(t *testing.T) {
	stateDir := t.TempDir()
	homeDir := t.TempDir()
	projectDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeDir)
	t.Setenv("PRISON_ROOT", stateDir)
	t.Setenv("PRISON_INMATES", "claude-code opencode")
	t.Chdir(projectDir)

	// Populate host configs for both claude-code and opencode.
	writeTestFile(t, filepath.Join(homeDir, ".claude", "CLAUDE.md"), "v1\n")
	writeTestFile(t, filepath.Join(homeDir, ".claude", "skills", "keep", "SKILL.md"), "keep\n")
	writeTestFile(t, filepath.Join(homeDir, ".claude", "settings.json"), `{"model": "opus", "hooks": {}}`)
	writeTestFile(t, filepath.Join(homeDir, ".config", "opencode", "AGENTS.md"), "agents v1\n")
	writeTestFile(t, filepath.Join(homeDir, ".config", "opencode", "opencode.json"), `{"model": "gpt-5"}`)

	// Sync all enabled inmates.
	var out bytes.Buffer
	cmd := newRootCommand()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"inmate", "sync", "--all"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("inmate sync --all: %v", err)
	}
	if !strings.Contains(out.String(), "claude-code: synced") ||
		!strings.Contains(out.String(), "opencode: synced") {
		t.Fatalf("output = %q, want both inmates synced", out.String())
	}

	root, err := state.OpenRoot(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	project, err := root.Project(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	claudeHome := filepath.Join(project.HomeDir("claude-code"), ".claude")
	opencodeHome := filepath.Join(project.HomeDir("opencode"), ".config", "opencode")

	// Add stale file in claude persisted home and update both host configs.
	writeTestFile(t, filepath.Join(claudeHome, "skills", "stale", "SKILL.md"), "stale\n")
	writeTestFile(t, filepath.Join(homeDir, ".claude", "CLAUDE.md"), "v2\n")
	writeTestFile(t, filepath.Join(homeDir, ".config", "opencode", "AGENTS.md"), "agents v2\n")

	// Sync only claude via alias; opencode should remain untouched.
	out.Reset()
	cmd = newRootCommand()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"inmate", "sync", "claude"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("inmate sync claude: %v", err)
	}
	if !strings.Contains(out.String(), "claude-code: synced") ||
		!strings.Contains(out.String(), "removed skills/stale/") {
		t.Fatalf("output = %q, want synced and removed skills/stale/", out.String())
	}
	if strings.Contains(out.String(), "opencode:") {
		t.Fatalf("output = %q, should not include opencode", out.String())
	}

	gotClaude, err := os.ReadFile(filepath.Join(claudeHome, "CLAUDE.md"))
	if err != nil || string(gotClaude) != "v2\n" {
		t.Fatalf("claude CLAUDE.md = %q, %v", gotClaude, err)
	}
	if _, err := os.Stat(filepath.Join(claudeHome, "skills", "stale")); !os.IsNotExist(err) {
		t.Fatalf("stale skill still exists (err=%v)", err)
	}
	gotOpencode, err := os.ReadFile(filepath.Join(opencodeHome, "AGENTS.md"))
	if err != nil || string(gotOpencode) != "agents v1\n" {
		t.Fatalf("opencode AGENTS.md = %q, %v, want v1 untouched", gotOpencode, err)
	}
}

func TestInmateSyncErrorsForDisabledOrUnknownInmate(t *testing.T) {
	stateDir := t.TempDir()
	projectDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRISON_ROOT", stateDir)
	t.Setenv("PRISON_INMATES", "claude-code")
	t.Chdir(projectDir)

	cmd := newRootCommand()
	cmd.SetArgs([]string{"inmate", "sync", "opencode"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "not enabled in this project") {
		t.Fatalf("disabled inmate err = %v, want not enabled error", err)
	}

	cmd = newRootCommand()
	cmd.SetArgs([]string{"inmate", "sync", "does-not-exist"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "no inmate called") {
		t.Fatalf("unknown inmate err = %v, want unknown inmate error", err)
	}
}

func TestInmateSyncRespectsInBoxAuthMode(t *testing.T) {
	stateDir := t.TempDir()
	homeDir := t.TempDir()
	projectDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeDir)
	t.Setenv("PRISON_ROOT", stateDir)
	t.Setenv("PRISON_INMATES", "antigravity-cli")
	t.Chdir(projectDir)

	writeTestFile(t, filepath.Join(homeDir, ".gemini", "antigravity-cli", "settings.json"), `{"colorScheme": "dark"}`)

	root, err := state.OpenRoot(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	project, err := root.Project(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.SetInmateAuthProfile("antigravity-cli", vault.InBoxInmateProfile); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newRootCommand()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"inmate", "sync", "agy"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("inmate sync agy: %v", err)
	}

	settingsPath := filepath.Join(project.HomeDir("antigravity-cli"), ".gemini", "antigravity-cli", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["colorScheme"] != "dark" {
		t.Fatalf("settings = %v, want colorScheme=dark", doc)
	}
	if _, present := doc["modelProvider"]; present {
		t.Fatalf("settings = %v, should not set modelProvider in in-box mode", doc)
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
