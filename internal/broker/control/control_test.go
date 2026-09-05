package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSpawnDetached starts a shell and checks that its stdout and
// stderr appear in the log file.
func TestSpawnDetached(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "broker.out")
	err := SpawnDetached("/bin/sh", []string{"-c", "echo spawned; echo err >&2"}, logPath)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(logPath)
		if strings.Contains(string(data), "spawned") && strings.Contains(string(data), "err") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("log holds %q", data)
		}
		time.Sleep(20 * time.Millisecond)
	}
	info, err := os.Stat(logPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("log mode = %v, %v", info.Mode(), err)
	}
}

// TestErrorMessage checks the Error string with and without a
// message field.
func TestErrorMessage(t *testing.T) {
	if got := (&Error{Status: 404, Message: "gone"}).Error(); got != "gone" {
		t.Fatalf("Error() = %q", got)
	}
	if got := (&Error{Status: 500}).Error(); got != "the broker answered 500" {
		t.Fatalf("Error() = %q", got)
	}
}
