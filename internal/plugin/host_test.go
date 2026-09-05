package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// claudeManifest is the shipped inmate manifest with a declarative
// `[host]` table.
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

// fakeHome builds a host home directory with the files the claude
// operations need. It returns the path.
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

// write creates a file and every directory above it.
func write(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readJSON reads the file at path and parses it as a JSON object.
func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "}\n") {
		t.Errorf("%s does not end with a newline after the object", path)
	}
	document := map[string]any{}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return document
}

// claudeInmate returns the claude manifest as an Inmate.
func claudeInmate(t *testing.T) *Inmate {
	t.Helper()
	manifest, err := ParseManifest([]byte(claudeManifest))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	return &Inmate{Manifest: *manifest, Name: "claude", Trusted: true}
}

// TestRunHostOperationsCarriesClaude checks that the declarative
// table carries instructions, directories, filtered settings, and
// the placeholder key.
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
			t.Errorf("%s was not carried: %v", name, err)
		}
	}

	settings := readJSON(t, filepath.Join(persisted, "settings.json"))
	if settings["model"] != "opus" || settings["theme"] != "dark" {
		t.Errorf("settings lost a carried key: %v", settings)
	}
	if settings["permissions"] == nil {
		t.Error("settings lost permissions")
	}
	for _, dropped := range []string{"hooks", "apiKeyHelper", "env"} {
		if _, present := settings[dropped]; present {
			t.Errorf("settings still hold %s", dropped)
		}
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "apiKeyHelper") ||
		!strings.Contains(notes[0], "hooks") {
		t.Errorf("notes = %v, want one naming the dropped keys", notes)
	}

	configuration := readJSON(t, filepath.Join(persisted, ".claude.json"))
	approved := approvedKeys(t, configuration)
	if len(approved) != 1 || approved[0] != placeholders.PlaceholderTail {
		t.Errorf("approved = %v, want the placeholder tail", approved)
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

// approvedKeys returns the approved API key list from a
// `.claude.json` object.
func approvedKeys(t *testing.T, configuration map[string]any) []string {
	t.Helper()
	responses, ok := configuration["customApiKeyResponses"].(map[string]any)
	if !ok {
		t.Fatalf("customApiKeyResponses is missing from %v", configuration)
	}
	list, ok := responses["approved"].([]any)
	if !ok {
		t.Fatalf("approved is missing from %v", responses)
	}
	var keys []string
	for _, member := range list {
		keys = append(keys, member.(string))
	}
	return keys
}

// TestRunHostOperationsRepeats checks that a second run keeps the
// approved key unique and preserves files the box created.
func TestRunHostOperationsRepeats(t *testing.T) {
	home := fakeHome(t, "0f4c1c8e-2b5a-4d3e-9a1f-000000000001")
	persisted := filepath.Join(t.TempDir(), "homes", "claude")
	inmate := claudeInmate(t)
	placeholders := Placeholders{PlaceholderTail: "abcdefghij0123456789"}

	if _, _, err := RunHostOperations(inmate, home, persisted, placeholders); err != nil {
		t.Fatalf("first run: %v", err)
	}
	write(t, filepath.Join(persisted, "agents", "own.md"), "the box wrote this\n")
	if _, _, err := RunHostOperations(inmate, home, persisted, placeholders); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if approved := approvedKeys(t, readJSON(t, filepath.Join(persisted, ".claude.json"))); len(approved) != 1 {
		t.Errorf("approved = %v, want one entry after two runs", approved)
	}
	if _, err := os.Stat(filepath.Join(persisted, "agents", "own.md")); err != nil {
		t.Errorf("the box's own file was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(persisted, ".DS_Store")); err == nil {
		t.Error(".DS_Store was carried")
	}
}

// TestRunHostOperationsSkipsMissingSources checks that missing
// source files produce notes instead of errors.
func TestRunHostOperationsSkipsMissingSources(t *testing.T) {
	home := t.TempDir()
	persisted := filepath.Join(t.TempDir(), "homes", "claude")
	inmate := claudeInmate(t)

	carried, notes, err := RunHostOperations(inmate, home, persisted, Placeholders{})
	if err != nil {
		t.Fatalf("RunHostOperations: %v", err)
	}
	if len(carried) != 1 || carried[0] != ".claude.json" {
		t.Errorf("carried = %v, want only the file the box owns", carried)
	}
	if len(notes) != 3 {
		t.Errorf("notes = %v, want one per missing copy", notes)
	}
	for _, note := range notes {
		if !strings.Contains(note, "not on the host") {
			t.Errorf("note %q does not say why nothing was carried", note)
		}
	}
	environment, err := HostEnvironment(inmate, home)
	if err != nil {
		t.Fatalf("HostEnvironment: %v", err)
	}
	if len(environment) != 0 {
		t.Errorf("environment = %v, want nothing from a home with no account", environment)
	}
}

// TestHostEnvironmentRefusesUnmatchedValue checks that values not
// matching the pattern are kept on the host.
func TestHostEnvironmentRefusesUnmatchedValue(t *testing.T) {
	inmate := claudeInmate(t)
	for _, value := range []string{"not a uuid", "0f4c1c8e-2b5a wide", ""} {
		home := fakeHome(t, value)
		environment, err := HostEnvironment(inmate, home)
		if err != nil {
			t.Fatalf("HostEnvironment: %v", err)
		}
		if len(environment) != 0 {
			t.Errorf("value %q crossed as %v", value, environment)
		}
	}
}

// TestRunHostOperationsRefusesEscape checks that a path escaping
// the persisted home is refused.
func TestRunHostOperationsRefusesEscape(t *testing.T) {
	home := fakeHome(t, "0f4c1c8e")
	persisted := filepath.Join(t.TempDir(), "homes", "claude")
	inmate := &Inmate{Name: "escape", Manifest: Manifest{Host: HostTable{
		Root: "~/.claude",
		JSON: []HostJSONOperation{{To: "../../escape.json", Set: map[string]string{"a": "b"}}},
	}}}
	if _, _, err := RunHostOperations(inmate, home, persisted, Placeholders{}); err == nil {
		t.Fatal("an operation wrote outside the persisted home")
	}
}

// TestRunHostOperationsSetsAndReplaces checks the `set` operation
// and the note left when it replaces a non-object value.
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
		t.Errorf("id = %v, want the substituted project id", state["id"])
	}
	ui, ok := state["ui"].(map[string]any)
	if !ok || ui["theme"] != "dark" {
		t.Errorf("ui = %v, want an object holding the theme", state["ui"])
	}
	if state["keep"] == nil {
		t.Error("set dropped a key it was not asked about")
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "was not an object") {
		t.Errorf("notes = %v, want one naming the replaced value", notes)
	}
}
