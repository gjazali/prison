package plugin

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func makeRepository(t *testing.T, manifest string) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "source")
	write(t, filepath.Join(directory, ManifestFileName), manifest)
	write(t, filepath.Join(directory, "Dockerfile"), "FROM base\n")
	runGit(t, directory, "-c", "init.defaultBranch=main", "init", "-q")
	commitAll(t, directory, "the first commit")
	return directory
}

func commitAll(t *testing.T, directory, message string) {
	t.Helper()
	runGit(t, directory, "add", "-A")
	runGit(t, directory,
		"-c", "user.email=test@example.com", "-c", "user.name=test",
		"-c", "commit.gpgsign=false", "commit", "-q", "-m", message)
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
}

func alwaysApprove(shown *string) func(string) bool {
	return func(description string) bool {
		*shown = description
		return true
	}
}

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
		t.Errorf("Install = %+v, want a trusted installed inmate", inmate)
	}
	if inmate.Dir != filepath.Join(installDir, "codex") {
		t.Errorf("Dir = %q, want %q", inmate.Dir,
			filepath.Join(installDir, "codex"))
	}
	if url2, _ := inmate.Origin(); url2 != url {
		t.Errorf("Origin = %q, want %q", url2, url)
	}
	for _, want := range []string{"codex", "a second inmate", "files",
		"inmate.toml", "Dockerfile", "/home/dev/.codex", "built as root"} {
		if !strings.Contains(shown, want) {
			t.Errorf("approval = %q, want %q", shown, want)
		}
	}
	if strings.Contains(shown, ".git/") {
		t.Errorf("approval = %q, want no .git files", shown)
	}
	if !strings.Contains(out.String(), "installed the codex inmate") {
		t.Errorf("out = %q, want \"installed the codex inmate\"",
			out.String())
	}
	if _, err := os.Stat(filepath.Join(inmate.Dir, ".git")); err == nil {
		t.Error("Stat(.git) = nil, want an error")
	}
}

func TestInstallRefusals(t *testing.T) {
	requireGit(t)
	registry, installDir, _ := testRegistry(t)

	declined := makeRepository(t, codexManifest)
	_, err := registry.Install(context.Background(), declined, "",
		func(string) bool { return false }, nil)
	if !errors.Is(err, ErrNotApproved) {
		t.Errorf("Install = %v, want ErrNotApproved", err)
	}
	if _, err := os.Stat(filepath.Join(installDir, "codex")); err == nil {
		t.Error("Stat(codex) = nil, want an error")
	}

	reserved := makeRepository(t, strings.Replace(codexManifest, "codex", "shell", -1))
	shown := ""
	_, err = registry.Install(context.Background(), reserved, "", alwaysApprove(&shown), nil)
	if err == nil || !strings.Contains(err.Error(), "prison command") {
		t.Errorf("Install = %v, want \"prison command\"", err)
	}

	shipped := makeRepository(t, claudeManifest)
	_, err = registry.Install(context.Background(), shipped, "", alwaysApprove(&shown), nil)
	if err == nil || !strings.Contains(err.Error(), "bundled inmate") {
		t.Errorf("Install = %v, want \"bundled inmate\"", err)
	}
	if shown != "" {
		t.Errorf("approval = %q, want none", shown)
	}

	aliased := makeRepository(t, strings.Replace(
		codexManifest, `aliases = ["cx"]`, `aliases = ["claude"]`, 1))
	_, err = registry.Install(context.Background(), aliased, "", alwaysApprove(&shown), nil)
	if err == nil || !strings.Contains(err.Error(), "bundled inmate") {
		t.Errorf("Install = %v, want \"bundled inmate\"", err)
	}

	empty := filepath.Join(t.TempDir(), "not-a-repository")
	if _, err := registry.Install(context.Background(), empty, "", alwaysApprove(&shown), nil); err == nil {
		t.Error("Install = nil, want an error")
	}
}

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
	if !strings.Contains(out.String(), "is up to date") {
		t.Errorf("out = %q, want \"is up to date\"", out.String())
	}

	write(t, filepath.Join(url, "Dockerfile"), "FROM base\nRUN echo two\n")
	commitAll(t, url, "the second commit")
	inmate, err := registry.Update(context.Background(), "codex", "", alwaysApprove(&shown), nil)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !inmate.Trusted {
		t.Error("Trusted = false, want true")
	}
	data, err := os.ReadFile(filepath.Join(inmate.Dir, "Dockerfile"))
	if err != nil || !strings.Contains(string(data), "echo two") {
		t.Errorf("Dockerfile = %q, %v, want \"echo two\"", data, err)
	}

	if _, err := registry.Update(context.Background(), "claude", "", alwaysApprove(&shown), nil); err == nil {
		t.Error("Update(claude) = nil, want an error")
	}
}

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
		t.Fatal("Trusted = true, want false")
	}

	shown = ""
	if _, err := registry.Trust("codex", func(string) bool { return false }, nil); !errors.Is(err, ErrNotApproved) {
		t.Errorf("Trust = %v, want ErrNotApproved", err)
	}
	out := &strings.Builder{}
	approved, err := registry.Trust("codex", alwaysApprove(&shown), out)
	if err != nil {
		t.Fatalf("Trust: %v", err)
	}
	if !approved.Trusted {
		t.Error("Trusted = false, want true")
	}
	if recorded, _ := approved.Origin(); recorded != url {
		t.Errorf("Origin = %q, want %q", recorded, url)
	}
	if !strings.Contains(out.String(), "approved the codex") {
		t.Errorf("out = %q, want \"approved the codex\"", out.String())
	}

	out.Reset()
	if _, err := registry.Trust("codex", alwaysApprove(&shown), out); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	if !strings.Contains(out.String(), "is approved") {
		t.Errorf("out = %q, want \"is approved\"", out.String())
	}
	out.Reset()
	if _, err := registry.Trust("claude", alwaysApprove(&shown), out); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	if !strings.Contains(out.String(), "needs no approval") {
		t.Errorf("out = %q, want \"needs no approval\"", out.String())
	}
}

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
		t.Error("Stat(Dir) = nil, want an error")
	}
	if _, err := os.Stat(filepath.Join(trustDir, "inmates", "codex.json")); err == nil {
		t.Error("Stat(codex.json) = nil, want an error")
	}
	if _, err := registry.Get("codex"); err == nil {
		t.Error("Get(codex) = nil, want an error")
	}
	if err := registry.Remove("claude"); err == nil {
		t.Error("Remove(claude) = nil, want an error")
	}
}

func TestInstallRefusesAnAliasABundledInmateAnswersTo(t *testing.T) {
	bundled := fstest.MapFS{
		"claude/inmate.toml": &fstest.MapFile{Data: []byte(
			strings.Replace(claudeManifest, `name = "claude"`,
				`name = "claude"`+"\naliases = [\"cc\"]", 1))},
	}
	registry := NewRegistry(bundled, t.TempDir(), t.TempDir())
	manifest, err := ParseManifest([]byte(strings.Replace(
		codexManifest, `aliases = ["cx"]`, `aliases = ["cc"]`, 1)))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	err = registry.refuseUnusableNames(manifest)
	if err == nil || !strings.Contains(err.Error(), "uses the alias") {
		t.Errorf("refuseUnusableNames = %v, want \"uses the alias\"", err)
	}
}
