package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadGlobalAbsentFile checks that a missing file returns empty
// settings, not an error.
func TestLoadGlobalAbsentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	global, err := LoadGlobal(path)
	if err != nil {
		t.Fatalf("LoadGlobal on an absent file: %v", err)
	}
	if global.Prison.Cage != nil || global.Prison.Inmates != nil {
		t.Errorf("absent file gave %+v, want nothing set", global.Prison)
	}
	if global.Checkpoint.Pager != nil || global.Checkpoint.DiffTool != nil {
		t.Errorf("absent file gave %+v, want nothing set", global.Checkpoint)
	}
}

// TestLoadGlobalAccepts reads a file with every key set and checks
// each value.
func TestLoadGlobalAccepts(t *testing.T) {
	path := writeConfigFile(t, "config.toml", `
[prison]
cage = "apple-container"
inmates = ["claude", "some-tool"]

[checkpoint]
pager = ""
diff_tool = "delta --side-by-side"
`)
	global, err := LoadGlobal(path)
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if global.Prison.Cage == nil || *global.Prison.Cage != "apple-container" {
		t.Errorf("cage = %v, want apple-container", global.Prison.Cage)
	}
	if global.Prison.Inmates == nil ||
		strings.Join(*global.Prison.Inmates, ",") != "claude,some-tool" {
		t.Errorf("inmates = %v, want claude,some-tool", global.Prison.Inmates)
	}
	if global.Checkpoint.Pager == nil || *global.Checkpoint.Pager != "" {
		t.Errorf("pager = %v, want an empty string", global.Checkpoint.Pager)
	}
	if global.Checkpoint.DiffTool == nil ||
		*global.Checkpoint.DiffTool != "delta --side-by-side" {
		t.Errorf("diff_tool = %v, want delta", global.Checkpoint.DiffTool)
	}
}

// TestLoadGlobalEmptyInmatesIsDeclared checks that an empty list
// is distinct from an absent key.
func TestLoadGlobalEmptyInmatesIsDeclared(t *testing.T) {
	declared := writeConfigFile(t, "config.toml", "[prison]\ninmates = []\n")
	global, err := LoadGlobal(declared)
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if global.Prison.Inmates == nil {
		t.Fatalf("inmates = [] read as absent")
	}
	if len(*global.Prison.Inmates) != 0 {
		t.Errorf("inmates = %v, want an empty list", *global.Prison.Inmates)
	}

	absent := writeConfigFile(t, "config.toml", "[prison]\ncage = \"fake\"\n")
	global, err = LoadGlobal(absent)
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if global.Prison.Inmates != nil {
		t.Errorf("an absent inmates key read as %v", *global.Prison.Inmates)
	}
}

// TestLoadGlobalRefuses checks one bad file per schema rule and
// verifies the error shape.
func TestLoadGlobalRefuses(t *testing.T) {
	cases := []struct {
		rule string
		text string
	}{
		{"unknown table", "[proson]\ncage = \"fake\"\n"},
		{"unknown key", "[prison]\ninmate = [\"claude\"]\n"},
		{"key outside a table", "cage = \"fake\"\n"},
		{"cage name", "[prison]\ncage = \"Apple Container\"\n"},
		{"inmate name", "[prison]\ninmates = [\"Claude\"]\n"},
		{"inmates type", "[prison]\ninmates = \"claude\"\n"},
		{"pager on one line", "[checkpoint]\npager = \"less\\n-R\"\n"},
		{"diff tool on one line", "[checkpoint]\ndiff_tool = \"a\\tb\"\n"},
		{"syntax", "[prison\n"},
	}
	for _, testCase := range cases {
		t.Run(testCase.rule, func(t *testing.T) {
			path := writeConfigFile(t, "config.toml", testCase.text)
			global, err := LoadGlobal(path)
			requireUnusableShape(t, path, err)
			if global != nil {
				t.Errorf("a refused file still gave settings: %+v", global)
			}
		})
	}
}

// TestLoadGlobalUnknownKeyNamesWhatIsAccepted checks that a typo's
// error lists the accepted keys.
func TestLoadGlobalUnknownKeyNamesWhatIsAccepted(t *testing.T) {
	path := writeConfigFile(t, "config.toml", "[checkpoint]\npagger = \"less\"\n")
	_, err := LoadGlobal(path)
	if err == nil {
		t.Fatalf("a typo in [checkpoint] was accepted")
	}
	message := err.Error()
	wanted := []string{`"pagger"`, "[checkpoint]", "diff_tool, pager"}
	for _, want := range wanted {
		if !strings.Contains(message, want) {
			t.Errorf("message %q does not mention %q", message, want)
		}
	}
}

// TestLoadGlobalUnknownTableNamesWhatIsAccepted checks that an
// unknown table's error lists the accepted tables.
func TestLoadGlobalUnknownTableNamesWhatIsAccepted(t *testing.T) {
	path := writeConfigFile(t, "config.toml", "[box]\ncpus = 4\n")
	_, err := LoadGlobal(path)
	if err == nil {
		t.Fatalf("an unknown table was accepted")
	}
	if !strings.Contains(err.Error(), "checkpoint, prison") {
		t.Errorf("message %q does not name the tables it accepts", err)
	}
}
