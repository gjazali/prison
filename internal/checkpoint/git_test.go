package checkpoint

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func runTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

func initTestRepo(t *testing.T) (*Store, string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	store := newTestStore(t, DefaultIgnores...)
	dir := store.Project
	runTestGit(t, dir, "init", "-b", "main")
	writeProjectFile(t, store, "app.txt", "v1\n", 0o644)
	runTestGit(t, dir, "add", "app.txt")
	runTestGit(t, dir, "commit", "-m", "initial")
	firstSHA := runTestGit(t, dir, "rev-parse", "HEAD")
	return store, dir, firstSHA
}

func TestCaptureAndDiffGitState(t *testing.T) {
	store, dir, firstSHA := initTestRepo(t)
	runTestGit(t, dir, "tag", "v1")
	runTestGit(t, dir, "pack-refs", "--all")
	// Add a remote-tracking ref to verify it is ignored.
	runTestGit(t, dir, "update-ref", "refs/remotes/origin/main", firstSHA)

	taken, err := store.Take("base")
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if taken.Git == nil {
		t.Fatal("Metadata.Git = nil, want captured state")
	}
	if taken.Git.Head != "ref: refs/heads/main" {
		t.Fatalf("Head = %q, want ref: refs/heads/main", taken.Git.Head)
	}
	if taken.Git.Refs["refs/heads/main"] != firstSHA || taken.Git.Refs["refs/tags/v1"] != firstSHA {
		t.Fatalf("Refs = %v", taken.Git.Refs)
	}
	if _, hasRemote := taken.Git.Refs["refs/remotes/origin/main"]; hasRemote {
		t.Fatalf("Refs included remote ref: %v", taken.Git.Refs)
	}

	changes, err := store.GitDiff(taken.ID)
	if err != nil || len(changes) != 0 {
		t.Fatalf("GitDiff before changes = %v, %v", changes, err)
	}

	runTestGit(t, dir, "checkout", "-b", "feature")
	writeProjectFile(t, store, "app.txt", "v2\n", 0o644)
	runTestGit(t, dir, "add", "app.txt")
	runTestGit(t, dir, "commit", "-m", "second")
	runTestGit(t, dir, "tag", "-d", "v1")

	changes, err = store.GitDiff(taken.ID)
	if err != nil {
		t.Fatalf("GitDiff: %v", err)
	}
	var formatted []string
	for _, change := range changes {
		formatted = append(formatted, FormatGitChange(change))
	}
	joined := strings.Join(formatted, "\n")
	if !strings.Contains(joined, "HEAD moved main -> feature") ||
		!strings.Contains(joined, "refs/heads/feature added") ||
		!strings.Contains(joined, "refs/tags/v1 deleted") {
		t.Fatalf("GitDiff formatted =\n%s", joined)
	}

	var report bytes.Buffer
	treeChanges, _, _, _, err := store.Diff(taken.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteReport(&report, store, taken.ID, treeChanges, ReportOptions{
		GitChanges: changes,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report.String(), "HEAD moved main -> feature") {
		t.Fatalf("report missing git change:\n%s", report.String())
	}
}

func TestRestoreWithAndWithoutGit(t *testing.T) {
	store, dir, firstSHA := initTestRepo(t)
	runTestGit(t, dir, "tag", "v1")

	base, err := store.Take("base")
	if err != nil {
		t.Fatal(err)
	}

	// Simulate an agent switching branches, committing, moving main, and deleting a tag.
	writeProjectFile(t, store, "app.txt", "v2\n", 0o644)
	runTestGit(t, dir, "add", "app.txt")
	runTestGit(t, dir, "commit", "-m", "agent commit on main")
	secondSHA := runTestGit(t, dir, "rev-parse", "HEAD")

	runTestGit(t, dir, "checkout", "-b", "agent-branch")
	writeProjectFile(t, store, "extra.txt", "branch file\n", 0o644)
	runTestGit(t, dir, "add", "extra.txt")
	runTestGit(t, dir, "commit", "-m", "agent branch commit")
	agentBranchSHA := runTestGit(t, dir, "rev-parse", "HEAD")
	runTestGit(t, dir, "tag", "-d", "v1")

	// Default restore leaves git refs and HEAD untouched.
	if _, err := store.Restore(base.ID); err != nil {
		t.Fatalf("Restore (default): %v", err)
	}
	if got := runTestGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "agent-branch" {
		t.Fatalf("default restore changed HEAD to %q", got)
	}
	if got := runTestGit(t, dir, "rev-parse", "refs/heads/main"); got != secondSHA {
		t.Fatalf("default restore changed main to %q", got)
	}

	// Restore with --git brings back main, v1, HEAD, and syncs index, while keeping agent-branch.
	result, err := store.RestoreWithOptions(base.ID, RestoreOptions{Git: true})
	if err != nil {
		t.Fatalf("RestoreWithOptions(Git): %v", err)
	}
	if result.Git == nil {
		t.Fatal("result.Git = nil")
	}
	if !result.Git.HeadChanged || result.Git.RestoredRefs != 2 {
		t.Fatalf("result.Git = %+v", result.Git)
	}
	if !slices.Equal(result.Git.KeptNewRefs, []string{"refs/heads/agent-branch"}) {
		t.Fatalf("KeptNewRefs = %v, want [refs/heads/agent-branch]", result.Git.KeptNewRefs)
	}
	if got := runTestGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("HEAD = %q, want main", got)
	}
	if got := runTestGit(t, dir, "rev-parse", "refs/heads/main"); got != firstSHA {
		t.Fatalf("main = %q, want %q", got, firstSHA)
	}
	if got := runTestGit(t, dir, "rev-parse", "refs/tags/v1"); got != firstSHA {
		t.Fatalf("v1 = %q, want %q", got, firstSHA)
	}
	if got := runTestGit(t, dir, "rev-parse", "refs/heads/agent-branch"); got != agentBranchSHA {
		t.Fatalf("agent-branch = %q, want %q", got, agentBranchSHA)
	}
	if status := runTestGit(t, dir, "status", "--porcelain"); status != "" {
		t.Fatalf("git status after restore --git = %q, want clean", status)
	}

	// Automatic safety checkpoint captured the pre-restore git state too.
	autoMeta, err := store.Metadata(result.Automatic)
	if err != nil {
		t.Fatal(err)
	}
	if autoMeta.Git == nil || autoMeta.Git.Head != "ref: refs/heads/agent-branch" {
		t.Fatalf("automatic checkpoint git = %+v", autoMeta.Git)
	}
}

func TestRestoreGitDetachedHead(t *testing.T) {
	store, dir, firstSHA := initTestRepo(t)
	runTestGit(t, dir, "checkout", "--detach", firstSHA)

	detached, err := store.Take("detached")
	if err != nil {
		t.Fatal(err)
	}
	if detached.Git == nil || detached.Git.Head != firstSHA {
		t.Fatalf("detached Git = %+v, want Head=%s", detached.Git, firstSHA)
	}

	runTestGit(t, dir, "checkout", "main")
	writeProjectFile(t, store, "app.txt", "changed\n", 0o644)
	runTestGit(t, dir, "add", "app.txt")
	runTestGit(t, dir, "commit", "-m", "on main")

	if _, err := store.RestoreWithOptions(detached.ID, RestoreOptions{Git: true}); err != nil {
		t.Fatalf("RestoreWithOptions(detached): %v", err)
	}
	headData, err := os.ReadFile(filepath.Join(dir, ".git", "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(headData)) != firstSHA {
		t.Fatalf("HEAD = %q, want detached %s", strings.TrimSpace(string(headData)), firstSHA)
	}
}

func TestRestoreGitPreflightGuards(t *testing.T) {
	// Non-git project refuses --git.
	nonGit := newTestStore(t, DefaultIgnores...)
	writeProjectFile(t, nonGit, "a.txt", "hi\n", 0o644)
	snap, err := nonGit.Take("plain")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nonGit.RestoreWithOptions(snap.ID, RestoreOptions{Git: true}); err == nil ||
		!strings.Contains(err.Error(), "no recorded git state") {
		t.Fatalf("RestoreWithOptions(non-git) = %v, want error", err)
	}

	// Merge in progress refuses --git without modifying working tree.
	store, dir, _ := initTestRepo(t)
	base, err := store.Take("base")
	if err != nil {
		t.Fatal(err)
	}
	writeProjectFile(t, store, "app.txt", "dirty\n", 0o644)
	if err := os.WriteFile(filepath.Join(dir, ".git", "MERGE_HEAD"),
		[]byte(strings.Repeat("a", 40)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RestoreWithOptions(base.ID, RestoreOptions{Git: true}); err == nil ||
		!strings.Contains(err.Error(), "MERGE_HEAD") {
		t.Fatalf("RestoreWithOptions(MERGE_HEAD) = %v, want MERGE_HEAD error", err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "app.txt")); err != nil || string(data) != "dirty\n" {
		t.Fatalf("working tree changed on failed preflight: %q, %v", string(data), err)
	}
	os.Remove(filepath.Join(dir, ".git", "MERGE_HEAD"))

	// Missing object refuses --git before touching anything.
	missingTarget := &GitState{
		Head: "ref: refs/heads/main",
		Refs: map[string]string{"refs/heads/main": strings.Repeat("b", 40)},
	}
	if err := CheckGitRestorable(dir, missingTarget); err == nil ||
		!strings.Contains(err.Error(), "no longer in the repository") {
		t.Fatalf("CheckGitRestorable(missing) = %v, want missing object error", err)
	}
}
