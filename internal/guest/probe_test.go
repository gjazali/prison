package guest

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

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
		t.Fatalf("probe() = %+v, want %+v", report, want)
	}
	if report := probe(nil, "", lookPath, hasTerminfo); report.Terminfo {
		t.Fatal("probe(no terminal).Terminfo = true, want false")
	}
	encoded, _ := json.Marshal(probe(nil, "", lookPath, hasTerminfo))
	wantJSON := `{"commands":{},"terminfo":false}`
	if string(encoded) != wantJSON {
		t.Fatalf("json.Marshal(empty) = %s, want %s", encoded, wantJSON)
	}
	var stdout, stderr bytes.Buffer
	if code := runProbe([]string{"--command", "sh, definitely-missing-tool",
		"--term", "no-such-terminal-xyz"}, &stdout, &stderr); code != 0 {
		t.Fatalf("runProbe() = %d, want 0. Stderr: %s", code, stderr.String())
	}
	var decoded probeReport
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("json.Unmarshal(%q) = %v", stdout.String(), err)
	}
	if !decoded.Commands["sh"] || decoded.Commands["definitely-missing-tool"] ||
		decoded.Terminfo {
		t.Fatalf("runProbe() report = %+v, want only sh", decoded)
	}
	if code := runProbe([]string{"--bogus"}, &stdout, &stderr); code != 2 {
		t.Fatalf("runProbe(--bogus) = %d, want 2", code)
	}
}
