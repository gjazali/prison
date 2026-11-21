package config

import (
	"slices"
	"strings"
	"testing"
)

func TestApplyProjectPatchNewFile(t *testing.T) {
	inmates := []string{"claude-code", "codex", "claude-code"}
	ports := []int{3000, 8080, 3000}
	sudo := true

	rendered, changed, err := ApplyProjectPatch(nil, ProjectPatch{
		Inmates:          &inmates,
		Ports:            &ports,
		Sudo:             &sudo,
		AddShadow:        []string{"node_modules", ".venv"},
		AddSetupCommands: []string{"pnpm install --frozen-lockfile"},
		AddIgnore:        []string{".pnpm-store/"},
		AddEgressHosts:   []string{"registry.npmjs.org", "api.example.com:8081"},
	})
	if err != nil {
		t.Fatalf("ApplyProjectPatch: %v", err)
	}
	if !changed {
		t.Fatalf("changed = false, want true")
	}

	loaded, err := LoadProject("prison.toml", rendered)
	if err != nil {
		t.Fatalf("LoadProject: %v\n%s", err, rendered)
	}
	if loaded.Prison.Inmates == nil || !slices.Equal(*loaded.Prison.Inmates, []string{"claude-code", "codex"}) {
		t.Fatalf("inmates = %v", loaded.Prison.Inmates)
	}
	if loaded.Box.Ports == nil || !slices.Equal(*loaded.Box.Ports, []int{3000, 8080}) {
		t.Fatalf("ports = %v", loaded.Box.Ports)
	}
	if loaded.Box.Sudo == nil || !*loaded.Box.Sudo {
		t.Fatalf("sudo = %v, want true", loaded.Box.Sudo)
	}
	if !slices.Equal(loaded.Box.Shadow, []string{"node_modules", ".venv"}) {
		t.Fatalf("shadow = %v", loaded.Box.Shadow)
	}
	if !slices.Equal(loaded.Setup.Commands, []string{"pnpm install --frozen-lockfile"}) {
		t.Fatalf("setup = %v", loaded.Setup.Commands)
	}
	if !slices.Equal(loaded.Checkpoint.Ignore, []string{".pnpm-store/"}) {
		t.Fatalf("ignore = %v", loaded.Checkpoint.Ignore)
	}
	if !slices.Equal(loaded.Egress.Hosts, []string{"registry.npmjs.org", "api.example.com:8081"}) {
		t.Fatalf("egress = %v", loaded.Egress.Hosts)
	}
}

func TestApplyProjectPatchEmptyExplicitLists(t *testing.T) {
	noInmates := []string{}
	noPorts := []int{}

	rendered, changed, err := ApplyProjectPatch(nil, ProjectPatch{
		Inmates: &noInmates,
		Ports:   &noPorts,
	})
	if err != nil {
		t.Fatalf("ApplyProjectPatch: %v", err)
	}
	if !changed {
		t.Fatalf("changed = false, want true")
	}
	text := string(rendered)
	if !strings.Contains(text, "inmates = []") {
		t.Fatalf("missing inmates = [] in:\n%s", text)
	}
	if !strings.Contains(text, "ports = []") {
		t.Fatalf("missing ports = [] in:\n%s", text)
	}

	loaded, err := LoadProject("prison.toml", rendered)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if loaded.Prison.Inmates == nil || len(*loaded.Prison.Inmates) != 0 {
		t.Fatalf("inmates = %v, want non-nil empty slice", loaded.Prison.Inmates)
	}
	if loaded.Box.Ports == nil || len(*loaded.Box.Ports) != 0 {
		t.Fatalf("ports = %v, want non-nil empty slice", loaded.Box.Ports)
	}
}

func TestApplyProjectPatchEmptyPatchOnEmptyFile(t *testing.T) {
	rendered, changed, err := ApplyProjectPatch(nil, ProjectPatch{})
	if err != nil {
		t.Fatalf("ApplyProjectPatch: %v", err)
	}
	if changed {
		t.Fatalf("changed = true, want false")
	}
	if len(rendered) != 0 {
		t.Fatalf("rendered = %q, want empty", rendered)
	}
}

func TestApplyProjectPatchPreservesCommentsAndUpdatesExisting(t *testing.T) {
	existing := []byte(`# Project config header
[prison]
inmates = [
  "claude-code", # primary
]

[box]
cpus = 4
memory = "4G"
ports = [3000] # web server
sudo = false # disabled by default
shadow = [
  "node_modules"
]

[environment]
APP_ENV = "dev"

[egress]
hosts = ["registry.npmjs.org"]
`)

	inmates := []string{"claude-code", "codex"}
	ports := []int{3000, 5173}
	sudo := true

	updated, changed, err := ApplyProjectPatch(existing, ProjectPatch{
		Inmates:        &inmates,
		Ports:          &ports,
		Sudo:           &sudo,
		AddShadow:      []string{"node_modules", ".venv"},
		AddEgressHosts: []string{"registry.npmjs.org", "api.example.com"},
	})
	if err != nil {
		t.Fatalf("ApplyProjectPatch: %v", err)
	}
	if !changed {
		t.Fatalf("changed = false, want true")
	}

	text := string(updated)
	for _, preserved := range []string{
		"# Project config header",
		"cpus = 4",
		`memory = "4G"`,
		"# web server",
		"# disabled by default",
		"[environment]",
		`APP_ENV = "dev"`,
	} {
		if !strings.Contains(text, preserved) {
			t.Fatalf("expected %q to be preserved in:\n%s", preserved, text)
		}
	}

	loaded, err := LoadProject("prison.toml", updated)
	if err != nil {
		t.Fatalf("LoadProject: %v\n%s", err, text)
	}
	if loaded.Prison.Inmates == nil || !slices.Equal(*loaded.Prison.Inmates, inmates) {
		t.Fatalf("inmates = %v, want %v", loaded.Prison.Inmates, inmates)
	}
	if loaded.Box.Ports == nil || !slices.Equal(*loaded.Box.Ports, ports) {
		t.Fatalf("ports = %v, want %v", loaded.Box.Ports, ports)
	}
	if loaded.Box.Sudo == nil || !*loaded.Box.Sudo {
		t.Fatalf("sudo = %v, want true", loaded.Box.Sudo)
	}
	if !slices.Equal(loaded.Box.Shadow, []string{"node_modules", ".venv"}) {
		t.Fatalf("shadow = %v", loaded.Box.Shadow)
	}
	if !slices.Equal(loaded.Egress.Hosts, []string{"registry.npmjs.org", "api.example.com"}) {
		t.Fatalf("egress = %v", loaded.Egress.Hosts)
	}

	// Re-applying the identical patch should be a no-op.
	again, changedAgain, err := ApplyProjectPatch(updated, ProjectPatch{
		Inmates:        &inmates,
		Ports:          &ports,
		Sudo:           &sudo,
		AddShadow:      []string{"node_modules", ".venv"},
		AddEgressHosts: []string{"registry.npmjs.org", "api.example.com"},
	})
	if err != nil {
		t.Fatalf("second ApplyProjectPatch: %v", err)
	}
	if changedAgain {
		t.Fatalf("second ApplyProjectPatch changed = true, want false\n%s", again)
	}
}

func TestApplyProjectPatchInsertsMissingKeysIntoExistingSection(t *testing.T) {
	existing := []byte(`[box]
cpus = 2

[environment]
DEBUG = "1"
`)
	ports := []int{8080}
	sudo := true

	updated, changed, err := ApplyProjectPatch(existing, ProjectPatch{
		Ports:     &ports,
		Sudo:      &sudo,
		AddShadow: []string{".venv"},
	})
	if err != nil {
		t.Fatalf("ApplyProjectPatch: %v", err)
	}
	if !changed {
		t.Fatalf("changed = false, want true")
	}

	loaded, err := LoadProject("prison.toml", updated)
	if err != nil {
		t.Fatalf("LoadProject: %v\n%s", err, updated)
	}
	if loaded.Box.CPUs == nil || *loaded.Box.CPUs != 2 {
		t.Fatalf("cpus = %v, want 2", loaded.Box.CPUs)
	}
	if loaded.Box.Ports == nil || !slices.Equal(*loaded.Box.Ports, []int{8080}) {
		t.Fatalf("ports = %v", loaded.Box.Ports)
	}
	if loaded.Box.Sudo == nil || !*loaded.Box.Sudo {
		t.Fatalf("sudo = %v", loaded.Box.Sudo)
	}
	if !slices.Equal(loaded.Box.Shadow, []string{".venv"}) {
		t.Fatalf("shadow = %v", loaded.Box.Shadow)
	}
	if loaded.Environment["DEBUG"] != "1" {
		t.Fatalf("environment = %v", loaded.Environment)
	}
}

func TestApplyProjectPatchRejectsInvalidValues(t *testing.T) {
	if _, _, err := ApplyProjectPatch(nil, ProjectPatch{
		AddShadow: []string{".git"},
	}); err == nil {
		t.Fatalf("expected error for .git shadow path")
	}

	if _, _, err := ApplyProjectPatch(nil, ProjectPatch{
		AddEgressHosts: []string{"https://example.com"},
	}); err == nil {
		t.Fatalf("expected error for URL scheme in egress hosts")
	}
}
