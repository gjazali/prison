package session

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"prison/internal/state"
)

func TestReadConfigDecodesTheBytesItHashed(t *testing.T) {
	path := filepath.Join(t.TempDir(), ProjectConfigFileName)
	content := []byte("[box]\ncpus = 4\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	trustedHash := hex.EncodeToString(digest[:])
	session := &Session{
		ConfigPath: path,
		Record:     &state.ProjectRecord{TrustedConfigSHA256: trustedHash},
	}
	if err := session.readConfig(); err != nil {
		t.Fatalf("readConfig: %v", err)
	}
	if !session.ConfigTrusted || session.ConfigHash != trustedHash {
		t.Fatalf("readConfig = trusted %v hash %s, want true %s",
			session.ConfigTrusted, session.ConfigHash, trustedHash)
	}
	if string(session.ConfigContent) != string(content) {
		t.Errorf("ConfigContent = %q, want %q", session.ConfigContent, content)
	}
	if cpus := session.Config.Box.CPUs; cpus == nil || *cpus != 4 {
		t.Errorf("Config.Box.CPUs = %v, want 4", cpus)
	}

	session = &Session{
		ConfigPath: path,
		Record:     &state.ProjectRecord{TrustedConfigSHA256: "other"},
	}
	if err := session.readConfig(); err != nil {
		t.Fatalf("readConfig: %v", err)
	}
	if session.ConfigTrusted || session.Config != nil {
		t.Errorf("readConfig = trusted %v config %v, want false nil",
			session.ConfigTrusted, session.Config)
	}
}
