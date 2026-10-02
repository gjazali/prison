package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const unusableSuffix = ". Fix it or delete it to use the defaults"

func writeConfigFile(t *testing.T, name, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func mapGetenv(variables map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, set := variables[name]
		return value, set
	}
}

func pointerTo[Value any](value Value) *Value {
	return &value
}

func requireUnusableShape(t *testing.T, path string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s error = nil, want an error", path)
	}
	if !strings.HasPrefix(err.Error(), path) {
		t.Errorf("error = %q, want prefix %q", err, path)
	}
	if !strings.HasSuffix(err.Error(), unusableSuffix) {
		t.Errorf("error = %q, want suffix %q", err, unusableSuffix)
	}
}
