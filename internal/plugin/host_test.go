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

func TestRunHostOperationsRefusesSymlinkEscape(t *testing.T) {
	cases := []struct {
		name     string
		host     HostTable
		link     string
		isFolder bool
	}{
		{name: "json", link: ".claude.json", host: HostTable{
			Root: "~/.claude",
			JSON: []HostJSONOperation{{
				To: ".claude.json", Set: map[string]string{"a": "b"},
			}},
		}},
		{name: "file", link: "CLAUDE.md", host: HostTable{
			Root: "~/.claude", Copy: []string{"CLAUDE.md"},
		}},
		{name: "directory", link: "agents", isFolder: true, host: HostTable{
			Root: "~/.claude", Copy: []string{"agents/"},
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			home := fakeHome(t, "0f4c1c8e")
			base := t.TempDir()
			persisted := filepath.Join(base, "homes", "claude")
			outside := filepath.Join(base, "outside")
			if test.isFolder {
				if err := os.MkdirAll(outside, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				write(t, outside, "host file\n")
			}
			if err := os.MkdirAll(persisted, 0o700); err != nil {
				t.Fatal(err)
			}
			err := os.Symlink(filepath.Join("..", "..", "outside"),
				filepath.Join(persisted, test.link))
			if err != nil {
				t.Fatal(err)
			}
			inmate := &Inmate{Name: "escape", Manifest: Manifest{Host: test.host}}

			_, _, err = RunHostOperations(inmate, home, persisted, Placeholders{})
			if err == nil {
				t.Error("RunHostOperations = nil, want an error")
			}
			if test.isFolder {
				entries, _ := os.ReadDir(outside)
				if len(entries) != 0 {
					t.Errorf("outside entries = %v, want none", entries)
				}
				return
			}
			data, _ := os.ReadFile(outside)
			if string(data) != "host file\n" {
				t.Errorf("outside = %q, want %q", data, "host file\n")
			}
		})
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

func TestRunHostOperationsWhenCredentialAndRemove(t *testing.T) {
	home := t.TempDir()
	persisted := t.TempDir()
	inmate := &Inmate{Name: "example", Manifest: Manifest{Host: HostTable{
		JSON: []HostJSONOperation{
			{
				To:             "example/settings.json",
				WhenCredential: []string{"EXAMPLE_API_KEY"},
				Set:            map[string]string{"authMode": "api-key"},
			},
			{
				To:             "example/settings.json",
				WhenCredential: []string{"EXAMPLE_OAUTH_REFRESH_TOKEN"},
				Remove:         []string{"authMode"},
			},
			{
				To:             "placeholder-oauth.json",
				WhenCredential: []string{"EXAMPLE_OAUTH_REFRESH_TOKEN"},
				Set: map[string]string{
					"type":          "authorized_user",
					"refresh_token": "prison-placeholder-${project_id}",
				},
			},
		},
	}}}

	_, _, err := RunHostOperations(inmate, home, persisted, Placeholders{
		ProjectID:        "aabbccddeeff",
		ActiveCredential: "EXAMPLE_API_KEY",
	})
	if err != nil {
		t.Fatalf("API key run: %v", err)
	}
	settings := readJSON(t, filepath.Join(persisted, "example", "settings.json"))
	if settings["authMode"] != "api-key" {
		t.Fatalf("authMode = %v, want api-key", settings["authMode"])
	}
	if _, err := os.Stat(filepath.Join(persisted, "placeholder-oauth.json")); !os.IsNotExist(err) {
		t.Fatalf("placeholder-oauth.json exists in API key mode")
	}

	_, _, err = RunHostOperations(inmate, home, persisted, Placeholders{
		ProjectID:        "aabbccddeeff",
		ActiveCredential: "EXAMPLE_OAUTH_REFRESH_TOKEN",
	})
	if err != nil {
		t.Fatalf("OAuth run: %v", err)
	}
	settings = readJSON(t, filepath.Join(persisted, "example", "settings.json"))
	if _, present := settings["authMode"]; present {
		t.Fatalf("authMode survived OAuth mode: %v", settings)
	}
	oauthFile := readJSON(t, filepath.Join(persisted, "placeholder-oauth.json"))
	if oauthFile["type"] != "authorized_user" || oauthFile["refresh_token"] != "prison-placeholder-aabbccddeeff" {
		t.Fatalf("placeholder-oauth.json = %v", oauthFile)
	}
}

func TestRunHostOperationsWhenAuth(t *testing.T) {
	home := t.TempDir()
	persisted := t.TempDir()
	inmate := &Inmate{Name: "example", Manifest: Manifest{Host: HostTable{
		JSON: []HostJSONOperation{
			{
				To:       "example/settings.json",
				WhenAuth: AuthModeBroker,
				Set:      map[string]string{"modelProvider": "gemini"},
			},
			{
				To:       "example/settings.json",
				WhenAuth: AuthModeInBox,
				Remove:   []string{"modelProvider"},
			},
		},
	}}}

	if _, _, err := RunHostOperations(inmate, home, persisted, Placeholders{
		ProjectID: "aabbccddeeff",
	}); err != nil {
		t.Fatalf("broker run: %v", err)
	}
	settings := readJSON(t, filepath.Join(persisted, "example", "settings.json"))
	if settings["modelProvider"] != "gemini" {
		t.Fatalf("modelProvider = %v, want gemini", settings["modelProvider"])
	}

	if _, _, err := RunHostOperations(inmate, home, persisted, Placeholders{
		ProjectID:  "aabbccddeeff",
		InBoxLogin: true,
	}); err != nil {
		t.Fatalf("in-box run: %v", err)
	}
	settings = readJSON(t, filepath.Join(persisted, "example", "settings.json"))
	if _, present := settings["modelProvider"]; present {
		t.Fatalf("modelProvider survived in-box login: %v", settings)
	}
}

func TestMirrorHostOperationsMirrorsEditsAndDeletions(t *testing.T) {
	home := fakeHome(t, "0f4c1c8e-2b5a-4d3e-9a1f-000000000001")
	persisted := filepath.Join(t.TempDir(), "homes", "claude")
	inmate := claudeInmate(t)
	placeholders := Placeholders{
		PlaceholderTail: "abcdefghij0123456789",
		Inmate:          "claude",
		ProjectID:       "0123456789ab",
	}

	// Initial sync populates persisted home.
	if _, err := MirrorHostOperations(inmate, home, persisted, placeholders); err != nil {
		t.Fatalf("initial MirrorHostOperations: %v", err)
	}
	// Box writes extra files inside mirrored dirs.
	write(t, filepath.Join(persisted, "agents", "box-only.md"), "box\n")
	write(t, filepath.Join(persisted, "skills", "old-skill", "SKILL.md"), "old\n")
	// Box also writes unrelated runtime files outside [host].copy that must survive.
	write(t, filepath.Join(persisted, "history.jsonl"), "keep me\n")

	// Modify host: edit CLAUDE.md, remove agents/x.md, remove entire skills/ dir,
	// and update settings.json.
	write(t, filepath.Join(home, ".claude", "CLAUDE.md"), "updated instructions\n")
	if err := os.Remove(filepath.Join(home, ".claude", "agents", "x.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(home, ".claude", "skills")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(home, ".claude", "settings.json"), `{"model": "sonnet"}`)

	res, err := MirrorHostOperations(inmate, home, persisted, placeholders)
	if err != nil {
		t.Fatalf("second MirrorHostOperations: %v", err)
	}

	gotClaudeMD, err := os.ReadFile(filepath.Join(persisted, "CLAUDE.md"))
	if err != nil || string(gotClaudeMD) != "updated instructions\n" {
		t.Fatalf("CLAUDE.md = %q, %v", gotClaudeMD, err)
	}
	for _, gone := range []string{"agents/x.md", "agents/box-only.md", "skills"} {
		if _, err := os.Stat(filepath.Join(persisted, filepath.FromSlash(gone))); !os.IsNotExist(err) {
			t.Errorf("%s still exists (err=%v), want removed", gone, err)
		}
	}
	// Empty agents/ dir should still exist because it exists on host.
	if info, err := os.Stat(filepath.Join(persisted, "agents")); err != nil || !info.IsDir() {
		t.Errorf("agents/ = %v, %v, want empty dir", info, err)
	}
	// Unmanaged files in persisted home must remain untouched.
	if _, err := os.Stat(filepath.Join(persisted, "history.jsonl")); err != nil {
		t.Errorf("history.jsonl = %v, want preserved", err)
	}
	settings := readJSON(t, filepath.Join(persisted, "settings.json"))
	if settings["model"] != "sonnet" {
		t.Errorf("settings.model = %v, want sonnet", settings["model"])
	}
	if _, present := settings["theme"]; present {
		t.Errorf("settings.theme survived removal on host")
	}
	wantRemoved := []string{"agents/box-only.md", "agents/x.md", "skills/"}
	if strings.Join(res.Removed, " ") != strings.Join(wantRemoved, " ") {
		t.Errorf("Removed = %v, want %v", res.Removed, wantRemoved)
	}

	// Now delete CLAUDE.md and settings.json on the host as well.
	if err := os.Remove(filepath.Join(home, ".claude", "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, ".claude", "settings.json")); err != nil {
		t.Fatal(err)
	}
	res, err = MirrorHostOperations(inmate, home, persisted, placeholders)
	if err != nil {
		t.Fatalf("third MirrorHostOperations: %v", err)
	}
	for _, gone := range []string{"CLAUDE.md", "settings.json"} {
		if _, err := os.Stat(filepath.Join(persisted, gone)); !os.IsNotExist(err) {
			t.Errorf("%s still exists (err=%v), want removed", gone, err)
		}
	}
	wantRemoved2 := []string{"CLAUDE.md", "settings.json"}
	if strings.Join(res.Removed, " ") != strings.Join(wantRemoved2, " ") {
		t.Errorf("Removed = %v, want %v", res.Removed, wantRemoved2)
	}
}

func TestMirrorHostOperationsJSONFromDeletionWithSubsequentSet(t *testing.T) {
	home := t.TempDir()
	persisted := t.TempDir()
	inmate := &Inmate{Name: "example", Manifest: Manifest{Host: HostTable{
		Root: "~/.example",
		JSON: []HostJSONOperation{
			{
				From: "settings.json",
				To:   "settings.json",
				Keep: []string{"theme"},
			},
			{
				To:       "settings.json",
				WhenAuth: AuthModeBroker,
				Set:      map[string]string{"modelProvider": "gemini"},
			},
			{
				To:       "settings.json",
				WhenAuth: AuthModeInBox,
				Remove:   []string{"modelProvider"},
			},
		},
	}}}

	write(t, filepath.Join(home, ".example", "settings.json"), `{"theme": "dark"}`)
	if _, err := MirrorHostOperations(inmate, home, persisted, Placeholders{}); err != nil {
		t.Fatalf("initial mirror: %v", err)
	}
	settings := readJSON(t, filepath.Join(persisted, "settings.json"))
	if settings["theme"] != "dark" || settings["modelProvider"] != "gemini" {
		t.Fatalf("initial settings = %v", settings)
	}

	// Delete host settings.json: broker mode should drop theme but keep modelProvider
	// and not report settings.json as removed.
	if err := os.Remove(filepath.Join(home, ".example", "settings.json")); err != nil {
		t.Fatal(err)
	}
	res, err := MirrorHostOperations(inmate, home, persisted, Placeholders{})
	if err != nil {
		t.Fatalf("broker mirror after delete: %v", err)
	}
	settings = readJSON(t, filepath.Join(persisted, "settings.json"))
	if _, present := settings["theme"]; present {
		t.Fatalf("theme survived host deletion: %v", settings)
	}
	if settings["modelProvider"] != "gemini" {
		t.Fatalf("modelProvider = %v, want gemini", settings["modelProvider"])
	}
	if contains(res.Removed, "settings.json") {
		t.Fatalf("Removed = %v, should not include recreated settings.json", res.Removed)
	}

	// In-box mode with deleted host settings.json removes settings.json completely.
	res, err = MirrorHostOperations(inmate, home, persisted, Placeholders{InBoxLogin: true})
	if err != nil {
		t.Fatalf("in-box mirror after delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(persisted, "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("settings.json still exists in in-box mode (err=%v)", err)
	}
	if !contains(res.Removed, "settings.json") {
		t.Fatalf("Removed = %v, want settings.json", res.Removed)
	}
}

func TestMirrorHostOperationsKeepsDestinationOnInvalidHostJSON(t *testing.T) {
	home := t.TempDir()
	persisted := t.TempDir()
	inmate := &Inmate{Name: "example", Manifest: Manifest{Host: HostTable{
		Root: "~/.example",
		JSON: []HostJSONOperation{{
			From: "settings.json",
			To:   "settings.json",
			Keep: []string{"theme"},
		}},
	}}}
	write(t, filepath.Join(persisted, "settings.json"), "{\"theme\": \"dark\"}\n")
	write(t, filepath.Join(home, ".example", "settings.json"), `{not valid json`)

	res, err := MirrorHostOperations(inmate, home, persisted, Placeholders{})
	if err != nil {
		t.Fatalf("MirrorHostOperations: %v", err)
	}
	settings := readJSON(t, filepath.Join(persisted, "settings.json"))
	if settings["theme"] != "dark" {
		t.Fatalf("settings = %v, want preserved", settings)
	}
	if len(res.Removed) != 0 {
		t.Fatalf("Removed = %v, want none", res.Removed)
	}
}

func TestMirrorHostOperationsRefusesSymlinkEscapeOnRemoval(t *testing.T) {
	home := t.TempDir() // host has neither CLAUDE.md nor agents/
	base := t.TempDir()
	persisted := filepath.Join(base, "homes", "claude")
	outside := filepath.Join(base, "outside")
	write(t, outside, "host file\n")
	if err := os.MkdirAll(persisted, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "outside"),
		filepath.Join(persisted, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	inmate := &Inmate{Name: "escape", Manifest: Manifest{Host: HostTable{
		Root: "~/.claude",
		Copy: []string{"CLAUDE.md"},
	}}}

	if _, err := MirrorHostOperations(inmate, home, persisted, Placeholders{}); err == nil {
		t.Fatal("MirrorHostOperations = nil, want error for escaping symlink")
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "host file\n" {
		t.Fatalf("outside = %q, %v, want untouched", data, err)
	}
}

func TestMirrorHostOperationsReplacesTypeMismatchInDestination(t *testing.T) {
	home := t.TempDir()
	persisted := t.TempDir()
	write(t, filepath.Join(home, ".claude", "CLAUDE.md"), "file now\n")
	write(t, filepath.Join(home, ".claude", "agents", "sub", "a.md"), "nested\n")
	// Persisted home has opposite types: CLAUDE.md is a dir, agents/sub is a file.
	write(t, filepath.Join(persisted, "CLAUDE.md", "nested.txt"), "old dir\n")
	write(t, filepath.Join(persisted, "agents", "sub"), "old file\n")

	inmate := &Inmate{Name: "claude", Manifest: Manifest{Host: HostTable{
		Root: "~/.claude",
		Copy: []string{"CLAUDE.md", "agents/"},
	}}}
	if _, err := MirrorHostOperations(inmate, home, persisted, Placeholders{}); err != nil {
		t.Fatalf("MirrorHostOperations: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(persisted, "CLAUDE.md"))
	if err != nil || string(data) != "file now\n" {
		t.Fatalf("CLAUDE.md = %q, %v", data, err)
	}
	nested, err := os.ReadFile(filepath.Join(persisted, "agents", "sub", "a.md"))
	if err != nil || string(nested) != "nested\n" {
		t.Fatalf("agents/sub/a.md = %q, %v", nested, err)
	}
}
