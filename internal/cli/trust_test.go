package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTrustFixture(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("cannot write %s: %v", path, err)
	}
	return path
}

func TestUnifiedDifferenceLabelsTheTwoSidesForARenderer(t *testing.T) {
	trusted := writeTrustFixture(t, "trusted.toml", "[box]\nsudo = false\n")
	current := writeTrustFixture(t, "prison.toml", "[box]\nsudo = true\n")
	difference, err := unifiedDifference(trusted, current)
	if err != nil {
		t.Fatalf("unifiedDifference: %v", err)
	}
	header := "--- " + trustedConfigLabel + "\n+++ " + currentConfigLabel + "\n"
	if !strings.HasPrefix(difference, header) {
		t.Errorf("unifiedDifference header = %q, want %q",
			strings.SplitAfterN(difference, "\n", 3)[0], header)
	}
	if !strings.Contains(difference, "+sudo = true") {
		t.Errorf("unifiedDifference = %s, want +sudo = true", difference)
	}
}

func TestUnifiedDifferenceDropsTheHeaderPrisonPrintsItself(t *testing.T) {
	trusted := writeTrustFixture(t, "trusted.toml", "[box]\nsudo = false\n")
	current := writeTrustFixture(t, "prison.toml", "[box]\nsudo = true\n")
	difference, err := unifiedDifference(trusted, current)
	if err != nil {
		t.Fatalf("unifiedDifference: %v", err)
	}
	difference = withoutFileHeader(difference)
	if strings.Contains(difference, trustedConfigLabel) ||
		strings.Contains(difference, currentConfigLabel) {
		t.Errorf("withoutFileHeader = %s, want no file header", difference)
	}
	if !strings.HasPrefix(difference, "@@") {
		t.Errorf("withoutFileHeader starts with %q, want @@",
			strings.SplitAfterN(difference, "\n", 2)[0])
	}
}

func TestUnifiedDifferenceReportsTwoIdenticalFiles(t *testing.T) {
	same := "[box]\nsudo = false\n"
	trusted := writeTrustFixture(t, "trusted.toml", same)
	current := writeTrustFixture(t, "prison.toml", same)
	if _, err := unifiedDifference(trusted, current); !errors.Is(
		err, errNoDifference) {
		t.Errorf("unifiedDifference error = %v, want errNoDifference", err)
	}
}
