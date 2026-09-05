package guest

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// TestProbe checks probe reports with fake lookups and verifies
// JSON encoding keeps the commands object when empty.
func TestProbe(t *testing.T) {
	lookPath := func(name string) (string, error) {
		if name == "git" {
			return "/usr/bin/git", nil
		}
		return "", errors.New("not found")
	}
	hasTerminfo := func(name string) bool { return name == "xterm-kitty" }
	report := probe([]string{"git", "node"}, "xterm-kitty", lookPath,
		hasTerminfo)
	want := probeReport{Commands: map[string]bool{"git": true, "node": false},
		Terminfo: true}
	if !reflect.DeepEqual(report, want) {
		t.Fatalf("report %+v, want %+v", report, want)
	}
	if report := probe(nil, "", lookPath, hasTerminfo); report.Terminfo {
		t.Fatal("no terminal asked for must report terminfo false")
	}
	encoded, _ := json.Marshal(probe(nil, "", lookPath, hasTerminfo))
	if string(encoded) != `{"commands":{},"terminfo":false}` {
		t.Fatalf("empty report encodes as %s", encoded)
	}
	var stdout, stderr bytes.Buffer
	if code := runProbe([]string{"--command", "sh, definitely-missing-tool",
		"--term", "no-such-terminal-xyz"}, &stdout, &stderr); code != 0 {
		t.Fatalf("runProbe exit %d: %s", code, stderr.String())
	}
	var decoded probeReport
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("output %q: %v", stdout.String(), err)
	}
	if !decoded.Commands["sh"] || decoded.Commands["definitely-missing-tool"] ||
		decoded.Terminfo {
		t.Fatalf("decoded %+v", decoded)
	}
	if code := runProbe([]string{"--bogus"}, &stdout, &stderr); code != 2 {
		t.Fatalf("bad flag exit %d", code)
	}
}
