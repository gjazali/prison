package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

func TestErrorMessage(t *testing.T) {
	if got := (&Error{Status: 404, Message: "gone"}).Error(); got != "gone" {
		t.Fatalf("Error() = %q", got)
	}
	got := (&Error{Status: 500}).Error()
	if got != "the broker returned status 500" {
		t.Fatalf("Error() = %q", got)
	}
}
