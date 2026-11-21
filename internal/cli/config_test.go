package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"prison/internal/config"
)

func TestConfigInitRequiresTerminal(t *testing.T) {
	t.Setenv("PRISON_ROOT", t.TempDir())
	t.Chdir(t.TempDir())

	command := newRootCommand()
	command.SetArgs([]string{"config", "init"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("config init without terminal = %v", err)
	}
}

func TestConfigInitWizardCreatesAndTrustsProjectConfig(t *testing.T) {
	t.Setenv("PRISON_ROOT", t.TempDir())
	projectDir := filepath.Join(t.TempDir(), "myapp")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	environment, err := loadEnvironment()
	if err != nil {
		t.Fatalf("loadEnvironment: %v", err)
	}
	available, err := usableInmates(environment.Registry)
	if err != nil {
		t.Fatalf("usableInmates: %v", err)
	}
	if len(available) < 2 {
		t.Fatalf("expected at least two bundled inmates, got %d", len(available))
	}
	firstInmate := available[0].Name
	secondInmate := available[1].Name

	// Answers:
	// 1. inmates: choice "1" + pinned second inmate
	// 2. ports: "3000, 8080"
	// 3. sudo: "y"
	// 4. shadow: ".venv, node_modules"
	// 5. egress: "api.example.com, example.org:8081"
	// 6. write prison.toml? -> "y"
	// 7. trust prison.toml now? -> "y"
	input := strings.Join([]string{
		"1, " + secondInmate + ":1.2.3",
		"3000, 8080",
		"y",
		".venv, node_modules",
		"api.example.com, example.org:8081",
		"y",
		"y",
		"",
	}, "\n")

	var out, prompts bytes.Buffer
	err = runConfigInitWizard(
		environment, projectDir, &configInitOptions{noDetect: true},
		strings.NewReader(input), &out, &prompts,
	)
	if err != nil {
		t.Fatalf("runConfigInitWizard: %v\nprompts:\n%s\nout:\n%s",
			err, prompts.String(), out.String())
	}

	configPath := filepath.Join(projectDir, "prison.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read prison.toml: %v", err)
	}
	loaded, err := config.LoadProject(configPath, data)
	if err != nil {
		t.Fatalf("LoadProject: %v\n%s", err, data)
	}

	wantInmates := []string{firstInmate, secondInmate + ":1.2.3"}
	if loaded.Prison.Inmates == nil || !slices.Equal(*loaded.Prison.Inmates, wantInmates) {
		t.Fatalf("inmates = %v, want %v", loaded.Prison.Inmates, wantInmates)
	}
	if loaded.Box.Ports == nil || !slices.Equal(*loaded.Box.Ports, []int{3000, 8080}) {
		t.Fatalf("ports = %v, want [3000 8080]", loaded.Box.Ports)
	}
	if loaded.Box.Sudo == nil || !*loaded.Box.Sudo {
		t.Fatalf("sudo = %v, want true", loaded.Box.Sudo)
	}
	if !slices.Equal(loaded.Box.Shadow, []string{".venv", "node_modules"}) {
		t.Fatalf("shadow = %v", loaded.Box.Shadow)
	}
	if !slices.Equal(loaded.Egress.Hosts, []string{"api.example.com", "example.org:8081"}) {
		t.Fatalf("egress = %v", loaded.Egress.Hosts)
	}

	opened, err := environment.Open(projectDir)
	if err != nil {
		t.Fatalf("environment.Open: %v", err)
	}
	if !opened.ConfigTrusted {
		t.Fatalf("ConfigTrusted = false, want true")
	}
	sum := sha256.Sum256(data)
	if opened.Record.TrustedConfigSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("TrustedConfigSHA256 = %q, want %q",
			opened.Record.TrustedConfigSHA256, hex.EncodeToString(sum[:]))
	}
}

func TestConfigInitWizardRecoversFromInvalidInputs(t *testing.T) {
	t.Setenv("PRISON_ROOT", t.TempDir())
	projectDir := filepath.Join(t.TempDir(), "retry-app")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	environment, err := loadEnvironment()
	if err != nil {
		t.Fatalf("loadEnvironment: %v", err)
	}

	input := strings.Join([]string{
		"999",                 // out of range inmate choice
		"unknown-inmate",      // unknown inmate name
		"none",                // valid: []
		"99999",               // invalid port
		"none",                // valid: []
		"n",                   // sudo = false (omitted on new file)
		".git",                // invalid shadow path
		"dist",                // valid shadow path
		"https://bad.example", // invalid egress host (scheme not allowed)
		"good.example.com",    // valid egress host
		"y",                   // write prison.toml
		"n",                   // decline trust
		"",
	}, "\n")

	var out, prompts bytes.Buffer
	err = runConfigInitWizard(
		environment, projectDir, &configInitOptions{noDetect: true},
		strings.NewReader(input), &out, &prompts,
	)
	if err != nil {
		t.Fatalf("runConfigInitWizard: %v", err)
	}

	promptText := prompts.String()
	for _, wantErr := range []string{
		"invalid inmates:",
		"invalid ports:",
		"invalid shadow directories:",
		"invalid egress host:",
	} {
		if !strings.Contains(promptText, wantErr) {
			t.Fatalf("expected %q in prompts:\n%s", wantErr, promptText)
		}
	}
	if !strings.Contains(out.String(), "Run `prison trust` to approve it") {
		t.Fatalf("expected trust reminder in output:\n%s", out.String())
	}

	data, err := os.ReadFile(filepath.Join(projectDir, "prison.toml"))
	if err != nil {
		t.Fatalf("read prison.toml: %v", err)
	}
	loaded, err := config.LoadProject("prison.toml", data)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if loaded.Prison.Inmates == nil || len(*loaded.Prison.Inmates) != 0 {
		t.Fatalf("inmates = %v, want []", loaded.Prison.Inmates)
	}
	if loaded.Box.Ports == nil || len(*loaded.Box.Ports) != 0 {
		t.Fatalf("ports = %v, want []", loaded.Box.Ports)
	}
	if loaded.Box.Sudo != nil {
		t.Fatalf("sudo = %v, want nil (omitted)", loaded.Box.Sudo)
	}
	if !slices.Equal(loaded.Box.Shadow, []string{"dist"}) {
		t.Fatalf("shadow = %v", loaded.Box.Shadow)
	}
	if !slices.Equal(loaded.Egress.Hosts, []string{"good.example.com"}) {
		t.Fatalf("egress = %v", loaded.Egress.Hosts)
	}

	opened, err := environment.Open(projectDir)
	if err != nil {
		t.Fatalf("environment.Open: %v", err)
	}
	if opened.ConfigTrusted {
		t.Fatalf("ConfigTrusted = true after declining trust, want false")
	}
}

func TestConfigInitWizardUpdatesExistingAndIntegratesEcosystems(t *testing.T) {
	t.Setenv("PRISON_ROOT", t.TempDir())
	catalogDir := t.TempDir()
	projectDir := filepath.Join(t.TempDir(), "existing-app")
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
hosts = ["registry.npmjs.org"]
docs = ["pnpm.io"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "pnpm-lock.yaml"),
		[]byte("lockfileVersion: 9\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	initial := `# Keep this comment
[box]
cpus = 4
sudo = true

[environment]
MODE = "local"
`
	configPath := filepath.Join(projectDir, "prison.toml")
	if err := os.WriteFile(configPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	environment, err := loadEnvironment()
	if err != nil {
		t.Fatalf("loadEnvironment: %v", err)
	}

	// Answers:
	// 1. include detected ecosystem settings? -> "y"
	// 2. inmates -> Enter (keep/default)
	// 3. ports -> "5173"
	// 4. allow sudo? -> "n" (flip existing true -> false)
	// 5. additional shadow -> ".turbo"
	// 6. additional egress -> "api.example.com"
	// 7. write prison.toml? -> "y"
	// 8. trust prison.toml now? -> "n"
	input := strings.Join([]string{
		"y",
		"",
		"5173",
		"n",
		".turbo",
		"api.example.com",
		"y",
		"n",
		"",
	}, "\n")

	var out, prompts bytes.Buffer
	err = runConfigInitWizard(
		environment, projectDir,
		&configInitOptions{source: catalogDir, noDocs: true},
		strings.NewReader(input), &out, &prompts,
	)
	if err != nil {
		t.Fatalf("runConfigInitWizard: %v\nprompts:\n%s", err, prompts.String())
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read prison.toml: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "# Keep this comment") {
		t.Fatalf("comment not preserved:\n%s", text)
	}
	if strings.Contains(text, "pnpm.io") {
		t.Fatalf("--no-docs should omit pnpm.io:\n%s", text)
	}

	loaded, err := config.LoadProject(configPath, data)
	if err != nil {
		t.Fatalf("LoadProject: %v\n%s", err, text)
	}
	if loaded.Box.CPUs == nil || *loaded.Box.CPUs != 4 {
		t.Fatalf("cpus = %v, want 4", loaded.Box.CPUs)
	}
	if loaded.Environment["MODE"] != "local" {
		t.Fatalf("environment = %v", loaded.Environment)
	}
	if loaded.Box.Ports == nil || !slices.Equal(*loaded.Box.Ports, []int{5173}) {
		t.Fatalf("ports = %v, want [5173]", loaded.Box.Ports)
	}
	if loaded.Box.Sudo == nil || *loaded.Box.Sudo {
		t.Fatalf("sudo = %v, want false", loaded.Box.Sudo)
	}
	if !slices.Equal(loaded.Box.Shadow, []string{"node_modules", ".turbo"}) {
		t.Fatalf("shadow = %v", loaded.Box.Shadow)
	}
	if !slices.Equal(loaded.Setup.Commands, []string{"pnpm install --frozen-lockfile"}) {
		t.Fatalf("setup = %v", loaded.Setup.Commands)
	}
	if !slices.Equal(loaded.Checkpoint.Ignore, []string{".pnpm-store/"}) {
		t.Fatalf("ignore = %v", loaded.Checkpoint.Ignore)
	}
	if !slices.Equal(loaded.Egress.Hosts, []string{"registry.npmjs.org", "api.example.com"}) {
		t.Fatalf("egress = %v", loaded.Egress.Hosts)
	}
}

func TestRenderProposedConfigUsesDiffToolForNewAndExisting(t *testing.T) {
	t.Setenv("PRISON_ROOT", t.TempDir())
	t.Setenv("PRISON_DIFF_TOOL", "sed 's/^/DIFFTOOL: /'")
	environment, err := loadEnvironment()
	if err != nil {
		t.Fatalf("loadEnvironment: %v", err)
	}

	proposed := []byte("[box]\nsudo = true\n")
	var newOut bytes.Buffer
	renderProposedConfig(&newOut, environment, nil, proposed, false, true)
	gotNew := newOut.String()
	for _, want := range []string{
		"proposed prison.toml:",
		"DIFFTOOL: --- /dev/null",
		"DIFFTOOL: +++ prison.toml",
		"DIFFTOOL: +sudo = true",
	} {
		if !strings.Contains(gotNew, want) {
			t.Fatalf("new file diff output missing %q:\n%s", want, gotNew)
		}
	}

	existing := []byte("[box]\nsudo = false\n")
	var updateOut bytes.Buffer
	renderProposedConfig(&updateOut, environment, existing, proposed, true, true)
	gotUpdate := updateOut.String()
	for _, want := range []string{
		"DIFFTOOL: --- prison.toml (current)",
		"DIFFTOOL: +++ prison.toml (proposed)",
		"DIFFTOOL: -sudo = false",
		"DIFFTOOL: +sudo = true",
	} {
		if !strings.Contains(gotUpdate, want) {
			t.Fatalf("existing file diff output missing %q:\n%s", want, gotUpdate)
		}
	}
}
