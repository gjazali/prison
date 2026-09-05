package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTrustFixture writes content to a file in a temporary
// directory. Returns the path. Fails the test on error.
func writeTrustFixture(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("cannot write %s: %v", path, err)
	}
	return path
}

// TestUnifiedDifferenceLabelsTheTwoSidesForARenderer checks that
// the diff header names the two sides, not the file paths.
func TestUnifiedDifferenceLabelsTheTwoSidesForARenderer(t *testing.T) {
	trusted := writeTrustFixture(t, "trusted.toml", "[box]\nsudo = false\n")
	current := writeTrustFixture(t, "prison.toml", "[box]\nsudo = true\n")
	difference, err := unifiedDifference(trusted, current)
	if err != nil {
		t.Fatalf("unifiedDifference: %v", err)
	}
	header := "--- " + trustedConfigLabel + "\n+++ " + currentConfigLabel + "\n"
	if !strings.HasPrefix(difference, header) {
		t.Errorf("diff starts with %q, want the labelled header %q",
			strings.SplitAfterN(difference, "\n", 3)[0], header)
	}
	if !strings.Contains(difference, "+sudo = true") {
		t.Errorf("diff does not carry the change:\n%s", difference)
	}
}

// TestUnifiedDifferenceDropsTheHeaderPrisonPrintsItself checks that
// withoutFileHeader strips the file header, leaving only hunks.
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
		t.Errorf("diff still carries a file header:\n%s", difference)
	}
	if !strings.HasPrefix(difference, "@@") {
		t.Errorf("diff starts with %q, want a hunk",
			strings.SplitAfterN(difference, "\n", 2)[0])
	}
}

// TestUnifiedDifferenceReportsTwoIdenticalFiles checks that
// identical files return errNoDifference instead of an empty diff.
func TestUnifiedDifferenceReportsTwoIdenticalFiles(t *testing.T) {
	same := "[box]\nsudo = false\n"
	trusted := writeTrustFixture(t, "trusted.toml", same)
	current := writeTrustFixture(t, "prison.toml", same)
	if _, err := unifiedDifference(trusted, current); !errors.Is(
		err, errNoDifference) {
		t.Errorf("two identical files gave %v, want errNoDifference", err)
	}
}
