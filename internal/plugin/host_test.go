package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const claudeManifest = `
[inmate]
name = "claude"
description = "Claude Code, Anthropic's CLI"

[image]
dockerfile = "Dockerfile"

[command]
run = "claude"
unsafe = "claude --dangerously-skip-permissions"

[auth]
upstream = "api.anthropic.com"
path_prefix = "/v1/"
base_url_variable = "ANTHROPIC_BASE_URL"
token_variable = "ANTHROPIC_API_KEY"

[[auth.credentials]]
variable = "ANTHROPIC_API_KEY"
header = "x-api-key"

[[auth.credentials]]
variable = "CLAUDE_CODE_OAUTH_TOKEN"
header = "Authorization"
prefix = "Bearer "

[persist]
paths = ["/home/dev/.claude"]

[environment]
CLAUDE_CONFIG_DIR = "/home/dev/.claude"

[egress]
hosts = ["api.anthropic.com"]

[hooks]
box_command = "true"

[host]
copy = ["CLAUDE.md", "agents/", "skills/"]

[[host.json]]
from = "settings.json"
to = "settings.json"
keep = ["model", "theme", "effortLevel", "outputStyle",
        "permissions", "includeCoAuthoredBy",
        "alwaysThinkingEnabled", "spinnerVerbs"]

[[host.json]]
to = ".claude.json"
append = { "customApiKeyResponses.approved" = "${placeholder_tail}" }

[[host.environment]]
name = "CLAUDE_CODE_ACCOUNT_UUID"
from = "~/.claude.json"
path = "oauthAccount.accountUuid"
pattern = "^[0-9a-fA-F-]{1,64}$"
`

func fakeHome(t *testing.T, accountUUID string) string {
	t.Helper()
	home := t.TempDir()
	write(t, filepath.Join(home, ".claude", "settings.json"), `{
  "model": "opus",
  "theme": "dark",
  "permissions": {"allow": ["Bash"]},
  "hooks": {"Stop": "say done"},
  "apiKeyHelper": "/usr/local/bin/key",
  "env": {"SECRET": "x"}
}`)
	write(t, filepath.Join(home, ".claude", "CLAUDE.md"), "be brief\n")
	write(t, filepath.Join(home, ".claude", "agents", "x.md"), "agent\n")
	write(t, filepath.Join(home, ".claude", "skills", "y", "SKILL.md"), "skill\n")
	write(t, filepath.Join(home, ".claude", ".DS_Store"), "junk")
	write(t, filepath.Join(home, ".claude.json"), `{
  "oauthAccount": {"accountUuid": "`+accountUUID+`", "emailAddress": "you@example.com"},
  "tips": 3
}`)
	return home
}

func write(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "}\n") {
		t.Errorf("%s ends with %q, want \"}\\n\"", path, data[max(0, len(data)-2):])
	}
	document := map[string]any{}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return document
}

func claudeInmate(t *testing.T) *Inmate {
	t.Helper()
	manifest, err := ParseManifest([]byte(claudeManifest))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	return &Inmate{Manifest: *manifest, Name: "claude", Trusted: true}
}

func TestRunHostOperationsCarriesClaude(t *testing.T) {
	home := fakeHome(t, "0f4c1c8e-2b5a-4d3e-9a1f-000000000001")
	persisted := filepath.Join(t.TempDir(), "homes", "claude")
	inmate := claudeInmate(t)
	placeholders := Placeholders{
		PlaceholderTail: "abcdefghij0123456789",
		Inmate:          "claude",
		ProjectID:       "0123456789ab",
	}

	carried, notes, err := RunHostOperations(inmate, home, persisted, placeholders)
	if err != nil {
		t.Fatalf("RunHostOperations: %v", err)
	}
	want := []string{"CLAUDE.md", "agents/", "skills/", "settings.json", ".claude.json"}
	if strings.Join(carried, " ") != strings.Join(want, " ") {
		t.Errorf("carried = %v, want %v", carried, want)
	}
	for _, name := range []string{"CLAUDE.md", "agents/x.md", "skills/y/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(persisted, filepath.FromSlash(name))); err != nil {
			t.Errorf("Stat(%s) = %v, want nil", name, err)
		}
	}

	settings := readJSON(t, filepath.Join(persisted, "settings.json"))
	if settings["model"] != "opus" || settings["theme"] != "dark" {
		t.Errorf("settings = %v, want model and theme", settings)
	}
	if settings["permissions"] == nil {
		t.Errorf("settings = %v, want permissions", settings)
	}
	for _, dropped := range []string{"hooks", "apiKeyHelper", "env"} {
		if _, present := settings[dropped]; present {
			t.Errorf("settings[%q] is present, want it dropped", dropped)
		}
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "apiKeyHelper") ||
		!strings.Contains(notes[0], "hooks") {
		t.Errorf("notes = %v, want one note with the dropped keys", notes)
	}

	configuration := readJSON(t, filepath.Join(persisted, ".claude.json"))
	approved := approvedKeys(t, configuration)
	if len(approved) != 1 || approved[0] != placeholders.PlaceholderTail {
		t.Errorf("approved = %v, want [%s]", approved,
			placeholders.PlaceholderTail)
	}

	environment, err := HostEnvironment(inmate, home)
	if err != nil {
		t.Fatalf("HostEnvironment: %v", err)
	}
	wantLine := "CLAUDE_CODE_ACCOUNT_UUID=0f4c1c8e-2b5a-4d3e-9a1f-000000000001"
	if len(environment) != 1 || environment[0] != wantLine {
		t.Errorf("environment = %v, want %q", environment, wantLine)
	}
}

func approvedKeys(t *testing.T, configuration map[string]any) []string {
	t.Helper()
	responses, ok := configuration["customApiKeyResponses"].(map[string]any)
	if !ok {
		t.Fatalf("configuration = %v, want customApiKeyResponses",
			configuration)
	}
	list, ok := responses["approved"].([]any)
	if !ok {
		t.Fatalf("customApiKeyResponses = %v, want approved", responses)
	}
	var keys []string
	for _, member := range list {
		keys = append(keys, member.(string))
	}
	return keys
}

func TestRunHostOperationsRepeats(t *testing.T) {
	home := fakeHome(t, "0f4c1c8e-2b5a-4d3e-9a1f-000000000001")
	persisted := filepath.Join(t.TempDir(), "homes", "claude")
	inmate := claudeInmate(t)
	placeholders := Placeholders{PlaceholderTail: "abcdefghij0123456789"}

	if _, _, err := RunHostOperations(inmate, home, persisted, placeholders); err != nil {
		t.Fatalf("RunHostOperations, first run: %v", err)
	}
	write(t, filepath.Join(persisted, "agents", "own.md"), "the box wrote this\n")
	if _, _, err := RunHostOperations(inmate, home, persisted, placeholders); err != nil {
		t.Fatalf("RunHostOperations, second run: %v", err)
	}
	if approved := approvedKeys(t, readJSON(t, filepath.Join(persisted, ".claude.json"))); len(approved) != 1 {
		t.Errorf("approved = %v, want one entry", approved)
	}
	if _, err := os.Stat(filepath.Join(persisted, "agents", "own.md")); err != nil {
		t.Errorf("Stat(agents/own.md) = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(persisted, ".DS_Store")); err == nil {
		t.Error("Stat(.DS_Store) = nil, want an error")
	}
}

func TestRunHostOperationsSkipsMissingSources(t *testing.T) {
	home := t.TempDir()
	persisted := filepath.Join(t.TempDir(), "homes", "claude")
	inmate := claudeInmate(t)

	carried, notes, err := RunHostOperations(inmate, home, persisted, Placeholders{})
	if err != nil {
		t.Fatalf("RunHostOperations: %v", err)
	}
	if len(carried) != 1 || carried[0] != ".claude.json" {
		t.Errorf("carried = %v, want [.claude.json]", carried)
	}
	if len(notes) != 3 {
		t.Errorf("notes = %v, want 3 notes", notes)
	}
	for _, note := range notes {
		if !strings.Contains(note, "not on the host") {
			t.Errorf("note = %q, want \"not on the host\"", note)
		}
	}
	environment, err := HostEnvironment(inmate, home)
	if err != nil {
		t.Fatalf("HostEnvironment: %v", err)
	}
	if len(environment) != 0 {
		t.Errorf("environment = %v, want none", environment)
	}
}

func TestHostEnvironmentRefusesUnmatchedValue(t *testing.T) {
	inmate := claudeInmate(t)
	for _, value := range []string{"not a uuid", "0f4c1c8e-2b5a wide", ""} {
		home := fakeHome(t, value)
		environment, err := HostEnvironment(inmate, home)
		if err != nil {
			t.Fatalf("HostEnvironment: %v", err)
		}
		if len(environment) != 0 {
			t.Errorf("HostEnvironment for %q = %v, want none", value, environment)
		}
	}
}

func TestRunHostOperationsRefusesEscape(t *testing.T) {
	home := fakeHome(t, "0f4c1c8e")
	persisted := filepath.Join(t.TempDir(), "homes", "claude")
	inmate := &Inmate{Name: "escape", Manifest: Manifest{Host: HostTable{
		Root: "~/.claude",
		JSON: []HostJSONOperation{{To: "../../escape.json", Set: map[string]string{"a": "b"}}},
	}}}
	if _, _, err := RunHostOperations(inmate, home, persisted, Placeholders{}); err == nil {
		t.Fatal("RunHostOperations = nil, want an error")
	}
}

func TestRunHostOperationsSetsAndReplaces(t *testing.T) {
	home := t.TempDir()
	persisted := t.TempDir()
	write(t, filepath.Join(persisted, "state.json"), `{"ui": "plain", "keep": 1}`)
	inmate := &Inmate{Name: "setter", Manifest: Manifest{Host: HostTable{
		JSON: []HostJSONOperation{{
			To:  "state.json",
			Set: map[string]string{"ui.theme": "dark", "id": "${project_id}"},
		}},
	}}}

	_, notes, err := RunHostOperations(inmate, home, persisted,
		Placeholders{ProjectID: "0123456789ab"})
	if err != nil {
		t.Fatalf("RunHostOperations: %v", err)
	}
	state := readJSON(t, filepath.Join(persisted, "state.json"))
	if state["id"] != "0123456789ab" {
		t.Errorf("id = %v, want 0123456789ab", state["id"])
	}
	ui, ok := state["ui"].(map[string]any)
	if !ok || ui["theme"] != "dark" {
		t.Errorf("ui = %v, want {theme: dark}", state["ui"])
	}
	if state["keep"] == nil {
		t.Errorf("state = %v, want keep", state)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "was not an object") {
		t.Errorf("notes = %v, want one \"was not an object\" note", notes)
	}
}
