package plugin

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireGit skips the test when git is not on the path.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// makeRepository creates a one-commit git repository with an
// inmate. It returns the path.
func makeRepository(t *testing.T, manifest string) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "source")
	write(t, filepath.Join(directory, ManifestFileName), manifest)
	write(t, filepath.Join(directory, "Dockerfile"), "FROM base\n")
	runGit(t, directory, "-c", "init.defaultBranch=main", "init", "-q")
	commitAll(t, directory, "the first commit")
	return directory
}

// commitAll stages and commits everything in directory with a
// fixed identity.
func commitAll(t *testing.T, directory, message string) {
	t.Helper()
	runGit(t, directory, "add", "-A")
	runGit(t, directory,
		"-c", "user.email=test@example.com", "-c", "user.name=test",
		"-c", "commit.gpgsign=false", "commit", "-q", "-m", message)
}

// runGit runs one git command in directory. It fails the test on
// error.
func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
}

// alwaysApprove returns an approval function that accepts and
// records what it was shown.
func alwaysApprove(shown *string) func(string) bool {
	return func(description string) bool {
		*shown = description
		return true
	}
}

// TestInstallShowsAndRecords checks the approval text, file
// placement, and recorded origin of a first install.
func TestInstallShowsAndRecords(t *testing.T) {
	requireGit(t)
	registry, installDir, _ := testRegistry(t)
	url := makeRepository(t, codexManifest)
	shown := ""
	out := &strings.Builder{}

	inmate, err := registry.Install(context.Background(), url, "", alwaysApprove(&shown), out)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !inmate.Trusted || inmate.Bundled {
		t.Errorf("installed inmate = %+v, want a trusted installed one", inmate)
	}
	if inmate.Dir != filepath.Join(installDir, "codex") {
		t.Errorf("dir = %q", inmate.Dir)
	}
	if url2, _ := inmate.Origin(); url2 != url {
		t.Errorf("origin = %q, want %q", url2, url)
	}
	for _, want := range []string{"codex", "a second inmate", "files",
		"inmate.toml", "Dockerfile", "/home/dev/.codex", "built as root"} {
		if !strings.Contains(shown, want) {
			t.Errorf("the approval did not mention %q:\n%s", want, shown)
		}
	}
	if strings.Contains(shown, ".git/") {
		t.Errorf("the approval listed git's own files:\n%s", shown)
	}
	if !strings.Contains(out.String(), "installed the codex inmate") {
		t.Errorf("out = %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(inmate.Dir, ".git")); err == nil {
		t.Error("the clone's .git directory was copied in")
	}
}

// TestInstallRefusals checks declined, reserved, shipped, and
// invalid install sources.
func TestInstallRefusals(t *testing.T) {
	requireGit(t)
	registry, installDir, _ := testRegistry(t)

	declined := makeRepository(t, codexManifest)
	_, err := registry.Install(context.Background(), declined, "",
		func(string) bool { return false }, nil)
	if !errors.Is(err, ErrNotApproved) {
		t.Errorf("a declined install returned %v, want ErrNotApproved", err)
	}
	if _, err := os.Stat(filepath.Join(installDir, "codex")); err == nil {
		t.Error("a declined install wrote the inmate anyway")
	}

	reserved := makeRepository(t, strings.Replace(codexManifest, "codex", "shell", -1))
	shown := ""
	_, err = registry.Install(context.Background(), reserved, "", alwaysApprove(&shown), nil)
	if err == nil || !strings.Contains(err.Error(), "prison's own commands") {
		t.Errorf("a reserved name returned %v", err)
	}

	shipped := makeRepository(t, claudeManifest)
	_, err = registry.Install(context.Background(), shipped, "", alwaysApprove(&shown), nil)
	if err == nil || !strings.Contains(err.Error(), "ships an inmate") {
		t.Errorf("a shipped name returned %v", err)
	}
	if shown != "" {
		t.Error("a refused name was shown for approval first")
	}

	empty := filepath.Join(t.TempDir(), "not-a-repository")
	if _, err := registry.Install(context.Background(), empty, "", alwaysApprove(&shown), nil); err == nil {
		t.Error("cloning something that is not a repository succeeded")
	}
}

// TestUpdateReplacesWhatChanged checks that an unchanged repository
// is a no-op and a changed one replaces the installed copy.
func TestUpdateReplacesWhatChanged(t *testing.T) {
	requireGit(t)
	registry, _, _ := testRegistry(t)
	url := makeRepository(t, codexManifest)
	shown := ""
	if _, err := registry.Install(context.Background(), url, "", alwaysApprove(&shown), nil); err != nil {
		t.Fatalf("Install: %v", err)
	}

	out := &strings.Builder{}
	if _, err := registry.Update(context.Background(), "codex", "", alwaysApprove(&shown), out); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !strings.Contains(out.String(), "already up to date") {
		t.Errorf("out = %q, want the unchanged report", out.String())
	}

	write(t, filepath.Join(url, "Dockerfile"), "FROM base\nRUN echo two\n")
	commitAll(t, url, "the second commit")
	inmate, err := registry.Update(context.Background(), "codex", "", alwaysApprove(&shown), nil)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !inmate.Trusted {
		t.Error("the updated inmate is not approved")
	}
	data, err := os.ReadFile(filepath.Join(inmate.Dir, "Dockerfile"))
	if err != nil || !strings.Contains(string(data), "echo two") {
		t.Errorf("the installed copy was not replaced: %q, %v", data, err)
	}

	if _, err := registry.Update(context.Background(), "claude", "", alwaysApprove(&shown), nil); err == nil {
		t.Error("a bundled inmate was updated")
	}
}

// TestTrustReapprovesAChangedInmate checks that an edit revokes
// approval and that Trust restores it with the recorded origin.
func TestTrustReapprovesAChangedInmate(t *testing.T) {
	requireGit(t)
	registry, _, _ := testRegistry(t)
	url := makeRepository(t, codexManifest)
	shown := ""
	installed, err := registry.Install(context.Background(), url, "", alwaysApprove(&shown), nil)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	write(t, filepath.Join(installed.Dir, "Dockerfile"), "FROM base\nRUN echo edited\n")
	changed, err := registry.Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	if changed.Trusted {
		t.Fatal("an edited inmate is still approved")
	}

	shown = ""
	if _, err := registry.Trust("codex", func(string) bool { return false }, nil); !errors.Is(err, ErrNotApproved) {
		t.Errorf("a declined approval returned %v", err)
	}
	out := &strings.Builder{}
	approved, err := registry.Trust("codex", alwaysApprove(&shown), out)
	if err != nil {
		t.Fatalf("Trust: %v", err)
	}
	if !approved.Trusted {
		t.Error("the inmate is not approved after Trust")
	}
	if recorded, _ := approved.Origin(); recorded != url {
		t.Errorf("origin = %q, want the one already recorded", recorded)
	}
	if !strings.Contains(out.String(), "approved") {
		t.Errorf("out = %q", out.String())
	}

	out.Reset()
	if _, err := registry.Trust("codex", alwaysApprove(&shown), out); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	if !strings.Contains(out.String(), "already approved") {
		t.Errorf("out = %q, want the already-approved report", out.String())
	}
	out.Reset()
	if _, err := registry.Trust("claude", alwaysApprove(&shown), out); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	if !strings.Contains(out.String(), "needs no approval") {
		t.Errorf("out = %q, want the bundled report", out.String())
	}
}

// TestRemoveDeletesInmateAndApproval checks that removal deletes
// both the files and the trust record, and refuses bundled inmates.
func TestRemoveDeletesInmateAndApproval(t *testing.T) {
	requireGit(t)
	registry, _, trustDir := testRegistry(t)
	url := makeRepository(t, codexManifest)
	shown := ""
	installed, err := registry.Install(context.Background(), url, "", alwaysApprove(&shown), nil)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if err := registry.Remove("codex"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(installed.Dir); err == nil {
		t.Error("the inmate directory is still there")
	}
	if _, err := os.Stat(filepath.Join(trustDir, "inmates", "codex.json")); err == nil {
		t.Error("the approval is still recorded")
	}
	if _, err := registry.Get("codex"); err == nil {
		t.Error("the removed inmate is still found")
	}
	if err := registry.Remove("claude"); err == nil {
		t.Error("a bundled inmate was removed")
	}
}
