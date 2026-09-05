package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadProjectAbsentFile checks that a missing file returns empty
// settings, not an error.
func TestLoadProjectAbsentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prison.toml")
	project, err := LoadProject(path)
	if err != nil {
		t.Fatalf("LoadProject on an absent file: %v", err)
	}
	if project.Prison.Inmates != nil || project.Box.CPUs != nil {
		t.Errorf("absent file gave %+v, want nothing declared", project)
	}
	if len(project.Environment) != 0 {
		t.Errorf("absent file gave environment %v", project.Environment)
	}
}

// TestLoadProjectAccepts reads a file with every table set and
// checks each parsed value.
func TestLoadProjectAccepts(t *testing.T) {
	path := writeConfigFile(t, "prison.toml", `
[prison]
inmates = ["claude"]

[box]
cpus = 8
memory = "16G"
ports = [3000, 5173]
sudo = true
shadow = ["build/", "node_modules"]

[setup]
commands = ["npm ci", "npm run build"]

[checkpoint]
ignore = ["*.log", "data/"]

[egress]
hosts = ["api.example.com", "*.example.com:8081"]

[secrets]
require = ["stripe", "commit-key"]

[environment]
RAILS_ENV = "development"
PORT = 3000
DEBUG = true
`)
	project, err := LoadProject(path)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if project.Box.CPUs == nil || *project.Box.CPUs != 8 {
		t.Errorf("cpus = %v, want 8", project.Box.CPUs)
	}
	if project.Box.Memory == nil || *project.Box.Memory != "16G" {
		t.Errorf("memory = %v, want 16G", project.Box.Memory)
	}
	if project.Box.Ports == nil || len(*project.Box.Ports) != 2 {
		t.Errorf("ports = %v, want two of them", project.Box.Ports)
	}
	if project.Box.Sudo == nil || !*project.Box.Sudo {
		t.Errorf("sudo = %v, want true", project.Box.Sudo)
	}
	if strings.Join(project.Box.Shadow, ",") != "build,node_modules" {
		t.Errorf("shadow = %v, want the slashes trimmed", project.Box.Shadow)
	}
	if strings.Join(project.Egress.Hosts, ",") !=
		"api.example.com,*.example.com:8081" {
		t.Errorf("hosts = %v, want them as written", project.Egress.Hosts)
	}
	wanted := map[string]string{
		"RAILS_ENV": "development", "PORT": "3000", "DEBUG": "true",
	}
	for name, value := range wanted {
		if project.Environment[name] != value {
			t.Errorf("environment %s = %q, want %q",
				name, project.Environment[name], value)
		}
	}
}

// TestLoadProjectRules checks each validation rule with one
// accepted and one refused file.
func TestLoadProjectRules(t *testing.T) {
	cases := []struct {
		rule   string
		accept string
		reject string
	}{
		{
			"inmate names",
			"[prison]\ninmates = [\"claude\"]\n",
			"[prison]\ninmates = [\"Claude\"]\n",
		},
		{
			"cpu count",
			"[box]\ncpus = 32\n",
			"[box]\ncpus = 33\n",
		},
		{
			"memory size",
			"[box]\nmemory = \"512M\"\n",
			"[box]\nmemory = \"512\"\n",
		},
		{
			"port range",
			"[box]\nports = [65535]\n",
			"[box]\nports = [65536]\n",
		},
		{
			"sudo is a boolean",
			"[box]\nsudo = false\n",
			"[box]\nsudo = \"yes\"\n",
		},
		{
			"shadow stays in the project",
			"[box]\nshadow = [\".venv\"]\n",
			"[box]\nshadow = [\"../elsewhere\"]\n",
		},
		{
			"shadow is relative",
			"[box]\nshadow = [\"target/debug\"]\n",
			"[box]\nshadow = [\"/tmp/target\"]\n",
		},
		{
			"shadow leaves .git alone",
			"[box]\nshadow = [\".gitlab\"]\n",
			"[box]\nshadow = [\".git/hooks\"]\n",
		},
		{
			"setup commands are single lines",
			"[setup]\ncommands = [\"npm ci\"]\n",
			"[setup]\ncommands = [\"npm ci\\nnpm test\"]\n",
		},
		{
			"setup commands say something",
			"[setup]\ncommands = [\"make\"]\n",
			"[setup]\ncommands = [\"  \"]\n",
		},
		{
			"checkpoint patterns are relative",
			"[checkpoint]\nignore = [\"data/*.log\"]\n",
			"[checkpoint]\nignore = [\"../data\"]\n",
		},
		{
			"egress hosts are host patterns",
			"[egress]\nhosts = [\"example.com:*\"]\n",
			"[egress]\nhosts = [\"https://example.com/path\"]\n",
		},
		{
			"secret names",
			"[secrets]\nrequire = [\"stripe\"]\n",
			"[secrets]\nrequire = [\"Stripe\"]\n",
		},
		{
			"the broker's own secret name",
			"[secrets]\nrequire = [\"prison-key\"]\n",
			"[secrets]\nrequire = [\"prison\"]\n",
		},
		{
			"environment names",
			"[environment]\n_RAILS_ENV2 = \"development\"\n",
			"[environment]\n2RAILS = \"development\"\n",
		},
		{
			"environment names prison owns",
			"[environment]\nPRISONER = \"1\"\n",
			"[environment]\nPRISON_ROOT = \"/tmp\"\n",
		},
		{
			"environment names confinement owns",
			"[environment]\nSSL_CERT_DIR = \"/etc\"\n",
			"[environment]\nSSL_CERT_FILE = \"/tmp/ca.pem\"\n",
		},
		{
			"environment values",
			"[environment]\nPORT = 3000\n",
			"[environment]\nPORT = [3000, 3001]\n",
		},
		{
			"values are single lines",
			"[environment]\nGREETING = \"hello there\"\n",
			"[environment]\nGREETING = \"hello\\nthere\"\n",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.rule, func(t *testing.T) {
			accepted := writeConfigFile(t, "prison.toml", testCase.accept)
			if _, err := LoadProject(accepted); err != nil {
				t.Errorf("LoadProject refused %q: %v", testCase.accept, err)
			}
			refused := writeConfigFile(t, "prison.toml", testCase.reject)
			project, err := LoadProject(refused)
			requireUnusableShape(t, refused, err)
			if project != nil {
				t.Errorf("a refused file still gave settings: %+v", project)
			}
		})
	}
}

// TestLoadProjectEmptyListsAreDeclared checks that an empty list is
// distinct from an absent key.
func TestLoadProjectEmptyListsAreDeclared(t *testing.T) {
	path := writeConfigFile(t, "prison.toml",
		"[prison]\ninmates = []\n\n[box]\nports = []\n")
	project, err := LoadProject(path)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if project.Prison.Inmates == nil || len(*project.Prison.Inmates) != 0 {
		t.Errorf("inmates = %v, want a declared empty list",
			project.Prison.Inmates)
	}
	if project.Box.Ports == nil || len(*project.Box.Ports) != 0 {
		t.Errorf("ports = %v, want a declared empty list", project.Box.Ports)
	}
}

// TestLoadProjectUnknownKeyNamesWhatIsAccepted checks that a typo's
// error lists the accepted keys for that table.
func TestLoadProjectUnknownKeyNamesWhatIsAccepted(t *testing.T) {
	path := writeConfigFile(t, "prison.toml", "[box]\ncpu = 4\n")
	_, err := LoadProject(path)
	if err == nil {
		t.Fatalf("a typo in [box] was accepted")
	}
	message := err.Error()
	wanted := []string{`"cpu"`, "[box]", "cpus, memory, ports, shadow, sudo"}
	for _, want := range wanted {
		if !strings.Contains(message, want) {
			t.Errorf("message %q does not mention %q", message, want)
		}
	}
}

// TestLoadProjectUnknownTableNamesWhatIsAccepted checks that an
// unknown table's error lists the accepted tables.
func TestLoadProjectUnknownTableNamesWhatIsAccepted(t *testing.T) {
	path := writeConfigFile(t, "prison.toml", "[image]\nname = \"debian\"\n")
	_, err := LoadProject(path)
	if err == nil {
		t.Fatalf("an unknown table was accepted")
	}
	want := "box, checkpoint, egress, environment, prison, secrets, setup"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("message %q does not name the tables it accepts", err)
	}
}
