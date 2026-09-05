package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unusableSuffix is the tail of every bad-file error message.
const unusableSuffix = "; fix it, or delete it to fall back to defaults"

// writeConfigFile writes text to a file in a temporary directory.
// Returns the path. Fails the test on error.
func writeConfigFile(t *testing.T, name, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// mapGetenv returns a lookup function over the given variable map.
func mapGetenv(variables map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, set := variables[name]
		return value, set
	}
}

// pointerTo returns a pointer to a copy of value.
func pointerTo[Value any](value Value) *Value {
	return &value
}

// requireUnusableShape checks that err starts with the path and
// ends with the unusable suffix.
func requireUnusableShape(t *testing.T, path string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s was accepted, want a refusal", path)
	}
	if !strings.HasPrefix(err.Error(), path) {
		t.Errorf("error %q does not start with %q", err, path)
	}
	if !strings.HasSuffix(err.Error(), unusableSuffix) {
		t.Errorf("error %q does not end with %q", err, unusableSuffix)
	}
}
