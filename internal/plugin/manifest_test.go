package plugin

import (
	"strings"
	"testing"
)

// minimalManifest is the smallest valid manifest. The rule tests
// append fragments to it.
const minimalManifest = `
[inmate]
name = "codex"
[command]
run = "codex"
`

// TestParseManifestDefaults checks the default values for omitted
// fields.
func TestParseManifestDefaults(t *testing.T) {
	manifest, err := ParseManifest([]byte(minimalManifest + `
[persist]
paths = ["/home/dev/.codex", "/home/dev/.cache/codex"]

[auth]
upstream = "api.openai.com"
base_url_variable = "OPENAI_BASE_URL"
token_variable = "OPENAI_API_KEY"
[[auth.credentials]]
variable = "OPENAI_API_KEY"
header = "Authorization"
prefix = "Bearer "
`))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if manifest.Inmate.Description != "codex" {
		t.Errorf("description = %q, want the name", manifest.Inmate.Description)
	}
	if manifest.Image.Dockerfile != "Dockerfile" {
		t.Errorf("dockerfile = %q, want Dockerfile", manifest.Image.Dockerfile)
	}
	if manifest.Auth.PathPrefix != "/" {
		t.Errorf("path_prefix = %q, want /", manifest.Auth.PathPrefix)
	}
	if manifest.Host.Root != "~/.codex" {
		t.Errorf("host.root = %q, want ~/.codex", manifest.Host.Root)
	}
}

// TestParseManifestAccepts checks that a manifest using every table
// validates.
func TestParseManifestAccepts(t *testing.T) {
	manifest, err := ParseManifest([]byte(claudeManifest))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if manifest.Command.Unsafe == "" {
		t.Error("command.unsafe was dropped")
	}
	if len(manifest.Auth.Credentials) != 2 {
		t.Errorf("credentials = %d, want 2", len(manifest.Auth.Credentials))
	}
	if got := manifest.EnvironmentAssignments(); len(got) != 1 ||
		got[0] != "CLAUDE_CONFIG_DIR=/home/dev/.claude" {
		t.Errorf("environment = %v", got)
	}
	if len(manifest.Host.JSON) != 2 || len(manifest.Host.Environment) != 1 {
		t.Errorf("host operations = %+v", manifest.Host)
	}
	if manifest.Host.Root != "~/.claude" {
		t.Errorf("host.root = %q", manifest.Host.Root)
	}
}

// TestParseManifestRefusals checks one fragment per validation rule.
// Each must be refused with a message naming the setting.
func TestParseManifestRefusals(t *testing.T) {
	cases := []struct {
		name     string
		fragment string
		mentions string
	}{
		{"unknown table", "[extras]\nx = 1\n", "unknown table [extras]"},
		{"unknown key", "[image]\nbase = \"x\"\n", "unknown key \"base\""},
		{"unknown host key", "[[host.json]]\nto = \"a.json\"\nmerge = []\n", "[[host.json]]"},
		{"host config hook", "[hooks]\nhost_config = \"hooks/x\"\n", "[host] table"},
		{"absolute dockerfile", "[image]\ndockerfile = \"/etc/Dockerfile\"\n", "image.dockerfile"},
		{"climbing dockerfile", "[image]\ndockerfile = \"../Dockerfile\"\n", "image.dockerfile"},
		{"auth without credentials", "[auth]\nupstream = \"a.example\"\nbase_url_variable = \"A_URL\"\ntoken_variable = \"A_KEY\"\n", "[[auth.credentials]]"},
		{"auth prefix", "[auth]\nupstream = \"a.example\"\npath_prefix = \"v1/\"\nbase_url_variable = \"A_URL\"\ntoken_variable = \"A_KEY\"\n[[auth.credentials]]\nvariable = \"A_KEY\"\nheader = \"x-key\"\n", "path_prefix"},
		{"persist outside home", "[persist]\npaths = [\"/etc/codex\"]\n", "/home/dev"},
		{"persist climbs", "[persist]\npaths = [\"/home/dev/../etc\"]\n", "climb"},
		{"reserved environment", "[environment]\nPRISON_TOKEN = \"x\"\n", "PRISON_TOKEN"},
		{"list environment", "[environment]\nA = [1, 2]\n", "environment.A"},
		{"bad egress", "[egress]\nhosts = [\"https://api.example\"]\n", "egress.hosts"},
		{"copy escapes", "[persist]\npaths = [\"/home/dev/.codex\"]\n[host]\ncopy = [\"../../etc/passwd\"]\n", "host.copy"},
		{"root without persist", "[host]\ncopy = [\"a\"]\n", "host.root"},
		{"json without to", "[persist]\npaths = [\"/home/dev/.codex\"]\n[[host.json]]\nfrom = \"a.json\"\n", "host.json.to"},
		{"json does nothing", "[persist]\npaths = [\"/home/dev/.codex\"]\n[[host.json]]\nto = \"a.json\"\n", "does nothing"},
		{"json escapes", "[persist]\npaths = [\"/home/dev/.codex\"]\n[[host.json]]\nto = \"../a.json\"\nset = {\"a\" = \"b\"}\n", "host.json.to"},
		{"empty dotted segment", "[persist]\npaths = [\"/home/dev/.codex\"]\n[[host.json]]\nto = \"a.json\"\nset = {\"a..b\" = \"c\"}\n", "empty segment"},
		{"environment name", "[[host.environment]]\nname = \"PRISON_X\"\nfrom = \"~/.codex.json\"\npath = \"a.b\"\npattern = \"^x$\"\n", "PRISON_X"},
		{"environment pattern", "[[host.environment]]\nname = \"CODEX_ID\"\nfrom = \"~/.codex.json\"\npath = \"a.b\"\npattern = \"^[a-\"\n", "regular expression"},
		{"environment escapes", "[[host.environment]]\nname = \"CODEX_ID\"\nfrom = \"~/../etc/passwd\"\npath = \"a.b\"\npattern = \"^x$\"\n", "host.environment.from"},
	}
	for _, c := range cases {
		_, err := ParseManifest([]byte(minimalManifest + c.fragment))
		if err == nil {
			t.Errorf("%s: accepted, want refusal", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.mentions) {
			t.Errorf("%s: %v, want a message mentioning %q", c.name, err, c.mentions)
		}
	}
}

// TestParseManifestRefusesIdentity checks the name, command, and
// syntax rules on full manifests.
func TestParseManifestRefusesIdentity(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		mentions string
	}{
		{"bad name", "[inmate]\nname = \"Codex\"\n[command]\nrun = \"codex\"\n", "inmate name"},
		{"no command", "[inmate]\nname = \"codex\"\n", "command.run"},
		{"not toml", "[inmate\n", "not valid TOML"},
	}
	for _, c := range cases {
		_, err := ParseManifest([]byte(c.text))
		if err == nil {
			t.Errorf("%s: accepted, want refusal", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.mentions) {
			t.Errorf("%s: %v, want a message mentioning %q", c.name, err, c.mentions)
		}
	}
}
