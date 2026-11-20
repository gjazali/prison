package plugin

import (
	"strings"
	"testing"
)

const minimalManifest = `
[inmate]
name = "codex"
[command]
run = "codex"
`

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
		t.Errorf("description = %q, want codex", manifest.Inmate.Description)
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

func TestParseManifestAccepts(t *testing.T) {
	manifest, err := ParseManifest([]byte(claudeManifest))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if manifest.Command.Unsafe == "" {
		t.Error("command.unsafe = \"\", want a command")
	}
	if len(manifest.Auth.Credentials) != 2 {
		t.Errorf("credentials = %d, want 2", len(manifest.Auth.Credentials))
	}
	if got := manifest.EnvironmentAssignments(); len(got) != 1 ||
		got[0] != "CLAUDE_CONFIG_DIR=/home/dev/.claude" {
		t.Errorf("EnvironmentAssignments = %v, "+
			"want [CLAUDE_CONFIG_DIR=/home/dev/.claude]", got)
	}
	if len(manifest.Host.JSON) != 2 || len(manifest.Host.Environment) != 1 {
		t.Errorf("host = %+v, want 2 json and 1 environment", manifest.Host)
	}
	if manifest.Host.Root != "~/.claude" {
		t.Errorf("host.root = %q, want ~/.claude", manifest.Host.Root)
	}
}

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
		{"persist climbs", "[persist]\npaths = [\"/home/dev/../etc\"]\n", "must not contain"},
		{"reserved environment", "[environment]\nPRISON_TOKEN = \"x\"\n", "PRISON_TOKEN"},
		{"list environment", "[environment]\nA = [1, 2]\n", "environment.A"},
		{"control character", "unsafe = \"a\\u001b[2Jb\"\n", "command.unsafe"},
		{"control egress", "[egress]\nhosts = [\"a\\u001b.example\"]\n", "egress.hosts"},
		{"bad egress", "[egress]\nhosts = [\"https://api.example\"]\n", "egress.hosts"},
		{"copy escapes", "[persist]\npaths = [\"/home/dev/.codex\"]\n[host]\ncopy = [\"../../etc/passwd\"]\n", "host.copy"},
		{"root without persist", "[host]\ncopy = [\"a\"]\n", "host.root"},
		{"json without to", "[persist]\npaths = [\"/home/dev/.codex\"]\n[[host.json]]\nfrom = \"a.json\"\n", "host.json.to"},
		{"empty json", "[persist]\npaths = [\"/home/dev/.codex\"]\n[[host.json]]\nto = \"a.json\"\n", "is empty"},
		{"json escapes", "[persist]\npaths = [\"/home/dev/.codex\"]\n[[host.json]]\nto = \"../a.json\"\nset = {\"a\" = \"b\"}\n", "host.json.to"},
		{"empty dotted segment", "[persist]\npaths = [\"/home/dev/.codex\"]\n[[host.json]]\nto = \"a.json\"\nset = {\"a..b\" = \"c\"}\n", "empty segment"},
		{"environment name", "[[host.environment]]\nname = \"PRISON_X\"\nfrom = \"~/.codex.json\"\npath = \"a.b\"\npattern = \"^x$\"\n", "PRISON_X"},
		{"environment pattern", "[[host.environment]]\nname = \"CODEX_ID\"\nfrom = \"~/.codex.json\"\npath = \"a.b\"\npattern = \"^[a-\"\n", "regular expression"},
		{"environment escapes", "[[host.environment]]\nname = \"CODEX_ID\"\nfrom = \"~/../etc/passwd\"\npath = \"a.b\"\npattern = \"^x$\"\n", "host.environment.from"},
	}
	for _, c := range cases {
		_, err := ParseManifest([]byte(minimalManifest + c.fragment))
		if err == nil {
			t.Errorf("%s: ParseManifest = nil, want an error", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.mentions) {
			t.Errorf("%s: ParseManifest = %v, want %q", c.name, err, c.mentions)
		}
	}
}

func TestParseManifestRefusesIdentity(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		mentions string
	}{
		{"bad name", "[inmate]\nname = \"Codex\"\n[command]\nrun = \"codex\"\n", "inmate name"},
		{"no command", "[inmate]\nname = \"codex\"\n", "command.run"},
		{"not toml", "[inmate\n", "not valid TOML"},
		{"alias name", "[inmate]\nname = \"codex\"\naliases = [\"CX\"]\n[command]\nrun = \"codex\"\n", "inmate alias name"},
		{"reserved alias", "[inmate]\nname = \"codex\"\naliases = [\"shell\"]\n[command]\nrun = \"codex\"\n", "prison command"},
		{"repeated alias", "[inmate]\nname = \"codex\"\naliases = [\"cx\", \"cx\"]\n[command]\nrun = \"codex\"\n", "duplicate"},
		{"alias is the name", "[inmate]\nname = \"codex\"\naliases = [\"codex\"]\n[command]\nrun = \"codex\"\n", "duplicate"},
	}
	for _, c := range cases {
		_, err := ParseManifest([]byte(c.text))
		if err == nil {
			t.Errorf("%s: ParseManifest = nil, want an error", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.mentions) {
			t.Errorf("%s: ParseManifest = %v, want %q", c.name, err, c.mentions)
		}
	}
}

func TestParseManifestKeepsAliases(t *testing.T) {
	manifest, err := ParseManifest([]byte(`
[inmate]
name = "antigravity-cli"
aliases = ["agy"]
[command]
run = "agy"
`))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if len(manifest.Inmate.Aliases) != 1 ||
		manifest.Inmate.Aliases[0] != "agy" {
		t.Errorf("aliases = %v, want [agy]", manifest.Inmate.Aliases)
	}
}

func TestParseManifestInterceptAndOAuth(t *testing.T) {
	manifest, err := ParseManifest([]byte(minimalManifest + `
[auth]
upstream = "cloudcode-pa.googleapis.com"
intercept = true
intercept_hosts = ["*.rep.googleapis.com"]

[[auth.credentials]]
variable = "GOOGLE_OAUTH_REFRESH_TOKEN"
header = "Authorization"
prefix = "Bearer "
token_url = "https://oauth2.googleapis.com/token"
client_id = "client-123"
`))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if !manifest.Auth.Intercept || len(manifest.Auth.InterceptHosts) != 1 {
		t.Fatalf("auth = %+v", manifest.Auth)
	}
	if manifest.Auth.Credentials[0].TokenURL != "https://oauth2.googleapis.com/token" ||
		manifest.Auth.Credentials[0].ClientID != "client-123" {
		t.Fatalf("credentials = %+v", manifest.Auth.Credentials)
	}

	bad := []string{
		"[auth]\nintercept_hosts = [\"*.com\"]\n[[auth.credentials]]\nvariable = \"K\"\nheader = \"Authorization\"\n",
		"[auth]\nintercept_hosts = [\"api.example.com:*\"]\n[[auth.credentials]]\nvariable = \"K\"\nheader = \"Authorization\"\n",
		"[auth]\nupstream = \"api.example.com\"\nintercept = true\n[[auth.credentials]]\nvariable = \"K\"\nheader = \"Authorization\"\ntoken_url = \"http://oauth.example/token\"\n",
		"[auth]\nupstream = \"api.example.com\"\nintercept = true\n[[auth.credentials]]\nvariable = \"K\"\nheader = \"Authorization\"\nclient_id = \"id-without-url\"\n",
	}
	for index, fragment := range bad {
		if _, err := ParseManifest([]byte(minimalManifest + fragment)); err == nil {
			t.Errorf("bad case %d: ParseManifest = nil, want an error", index)
		}
	}
}

func TestParseManifestInBoxAuthAndWhenAuth(t *testing.T) {
	manifest, err := ParseManifest([]byte(minimalManifest + `
[auth]
upstream = "api.example.com"
base_url_variable = "EXAMPLE_BASE_URL"
token_variable = "EXAMPLE_KEY"

[[auth.credentials]]
variable = "EXAMPLE_KEY"
header = "x-api-key"

[auth.in_box]
hosts = ["accounts.example.com", "*.regional.example.com"]

[[host.json]]
to = "settings.json"
when_auth = "broker"
set = { mode = "api" }

[[host.json]]
to = "settings.json"
when_auth = "in-box"
remove = ["mode"]
`))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if manifest.Auth.InBox == nil || len(manifest.Auth.InBox.Hosts) != 2 {
		t.Fatalf("auth.in_box = %+v", manifest.Auth.InBox)
	}
	if manifest.Host.JSON[0].WhenAuth != AuthModeBroker ||
		manifest.Host.JSON[1].WhenAuth != AuthModeInBox {
		t.Fatalf("host.json = %+v", manifest.Host.JSON)
	}

	bad := []string{
		"[auth]\nupstream = \"api.example.com\"\nbase_url_variable = \"U\"\ntoken_variable = \"K\"\n[[auth.credentials]]\nvariable = \"K\"\nheader = \"Authorization\"\n[auth.in_box]\nhosts = [\"https://bad.example.com\"]\n",
		"[[host.json]]\nto = \"settings.json\"\nwhen_auth = \"other\"\nset = { a = \"b\" }\n",
		"[[host.json]]\nto = \"settings.json\"\nwhen_auth = \"in-box\"\nwhen_credential = [\"K\"]\nset = { a = \"b\" }\n",
	}
	for index, fragment := range bad {
		if _, err := ParseManifest([]byte(minimalManifest + fragment)); err == nil {
			t.Errorf("bad case %d: ParseManifest = nil, want an error", index)
		}
	}
}
