package session

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
)

func (s *Session) SetupCommands() []string {
	if s.Config == nil || len(s.Config.Setup.Commands) == 0 {
		return nil
	}
	return append([]string(nil), s.Config.Setup.Commands...)
}

func SetupSignature(commands []string) string {
	if len(commands) == 0 {
		return ""
	}
	digest := sha256.Sum256([]byte(strings.Join(commands, "\n")))
	return hex.EncodeToString(digest[:])[:16]
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
