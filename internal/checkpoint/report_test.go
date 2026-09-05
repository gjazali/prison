package checkpoint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// linkProject creates a symlink at relativePath pointing to target.
func linkProject(t *testing.T, store *Store, relativePath, target string) {
	t.Helper()
	absolute := filepath.Join(store.Project, relativePath)
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatalf("creating the directory holding %s: %v", relativePath, err)
	}
	if err := os.Symlink(target, absolute); err != nil {
		t.Fatalf("creating the link %s: %v", relativePath, err)
	}
}

// mkdirProject creates a directory at relativePath in the project.
func mkdirProject(t *testing.T, store *Store, relativePath string) {
	t.Helper()
	absolute := filepath.Join(store.Project, relativePath)
	if err := os.MkdirAll(absolute, 0o755); err != nil {
		t.Fatalf("creating the directory %s: %v", relativePath, err)
	}
}

// removeProject deletes relativePath and everything under it.
func removeProject(t *testing.T, store *Store, relativePath string) {
	t.Helper()
	absolute := filepath.Join(store.Project, relativePath)
	if err := os.RemoveAll(absolute); err != nil {
		t.Fatalf("removing %s: %v", relativePath, err)
	}
}

// chmodProject sets the permissions of relativePath to mode.
func chmodProject(
	t *testing.T, store *Store, relativePath string, mode os.FileMode) {
	t.Helper()
	absolute := filepath.Join(store.Project, relativePath)
	if err := os.Chmod(absolute, mode); err != nil {
		t.Fatalf("setting the permissions of %s: %v", relativePath, err)
	}
}

// readProject returns the contents of relativePath as a string.
func readProject(t *testing.T, store *Store, relativePath string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(store.Project, relativePath))
	if err != nil {
		t.Fatalf("reading %s: %v", relativePath, err)
	}
	return string(data)
}

// mustTake takes a checkpoint with the given label. Fails the test
// on error.
func mustTake(t *testing.T, store *Store, label string) Checkpoint {
	t.Helper()
	taken, err := store.Take(label)
	if err != nil {
		t.Fatalf("taking a checkpoint: %v", err)
	}
	return taken
}

// shortDigest returns the first seven hex characters of the SHA-256
// of content.
func shortDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:7]
}

// runDiff diffs the tree against a checkpoint. Returns the resolved
// identifier and changes. Fails the test on error.
func runDiff(t *testing.T, store *Store, wanted string) (string, Changes) {
	t.Helper()
	identifier, err := store.DiffTarget(wanted)
	if err != nil {
		t.Fatalf("resolving the diff target: %v", err)
	}
	changes, _, _, _, err := store.Diff(wanted)
	if err != nil {
		t.Fatalf("diffing: %v", err)
	}
	return identifier, changes
}

// TestWriteReportRowsAndNotes checks the human report for every row
// shape: added, removed, and changed paths with their notes.
func TestWriteReportRowsAndNotes(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "gone.bin", "\x00\x01", 0o644)
	writeProjectFile(t, store, "gone.txt", "bye\n", 0o644)
	mkdirProject(t, store, "gonedir")
	linkProject(t, store, "gonelink", "gone.txt")
	writeProjectFile(t, store, "image.bin", "\x89PNG\x00", 0o644)
	writeProjectFile(t, store, "kind.txt", "was a file\n", 0o644)
	linkProject(t, store, "link", "plain.txt")
	writeProjectFile(t, store, "plain.txt", "one\ntwo\nthree\n", 0o644)
	writeProjectFile(t, store, "run.sh", "#!/bin/sh\n", 0o755)
	writeProjectFile(t, store, "script.sh", "#!/bin/sh\n", 0o644)
	mustTake(t, store, "before")

	for _, path := range []string{"gone.bin", "gone.txt", "gonedir",
		"gonelink", "kind.txt", "link"} {
		removeProject(t, store, path)
	}
	writeProjectFile(t, store, "image.bin", "\x89PNG\x01", 0o644)
	mkdirProject(t, store, "kind.txt")
	linkProject(t, store, "link", "other.txt")
	writeProjectFile(t, store, "plain.txt", "one\n2\nthree\n", 0o644)
	chmodProject(t, store, "run.sh", 0o644)
	chmodProject(t, store, "script.sh", 0o755)
	writeProjectFile(t, store, "new.bin", "\x00\x02", 0o644)
	writeProjectFile(t, store, "new.sh", "#!/bin/sh\n", 0o755)
	writeProjectFile(t, store, "new.txt", "hi\n", 0o644)
	mkdirProject(t, store, "newdir")
	linkProject(t, store, "newlink", "plain.txt")

	identifier, changes := runDiff(t, store, "")
	var output bytes.Buffer
	if err := WriteReport(&output, store, identifier, changes,
		ReportOptions{}); err != nil {
		t.Fatalf("writing the report: %v", err)
	}
	want := strings.Join([]string{
		"15 path(s) changed since checkpoint 0001.",
		"The project is mounted writable, so these are on your disk now.",
		"",
		"  added          new.bin (binary)",
		"  added          new.sh (executable)",
		"  added          new.txt",
		"  added          newdir/",
		"  added          newlink (symlink)",
		"  removed        gone.bin (binary)",
		"  removed        gone.txt",
		"  removed        gonedir/",
		"  removed        gonelink (symlink)",
		"  changed        image.bin",
		"    (binary file, contents differ)",
		"  changed        kind.txt (file became dir)",
		"  changed        link",
		"    (symlink: plain.txt -> other.txt)",
		"  changed        plain.txt",
		"    @@ -1,3 +1,3 @@",
		"     one",
		"    -two",
		"    +2",
		"     three",
		"  changed        run.sh (no longer executable)",
		"  changed        script.sh (became executable)",
		"",
		"`prison checkpoint` records this as a new checkpoint.",
		"",
	}, "\n")
	if output.String() != want {
		t.Errorf("report:\n%s\nwant:\n%s", output.String(), want)
	}
}

// TestDiffTargetFallsBack checks target resolution: explicit
// identifier, then HEAD, then newest, then error when empty.
func TestDiffTargetFallsBack(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.DiffTarget(""); err == nil ||
		!strings.Contains(err.Error(), "no checkpoints for this project yet") {
		t.Errorf("DiffTarget on an empty store gave %v", err)
	}
	writeProjectFile(t, store, "a.txt", "a\n", 0o644)
	mustTake(t, store, "")
	mustTake(t, store, "")
	if got, err := store.DiffTarget(""); err != nil || got != "0002" {
		t.Errorf("DiffTarget with HEAD on 0002 = %q, %v", got, err)
	}
	if err := store.clearHead(); err != nil {
		t.Fatalf("clearing HEAD: %v", err)
	}
	if got, err := store.DiffTarget(""); err != nil || got != "0002" {
		t.Errorf("DiffTarget without HEAD = %q, %v, want the newest", got, err)
	}
	if got, err := store.DiffTarget("1"); err != nil || got != "0001" {
		t.Errorf("DiffTarget(\"1\") = %q, %v", got, err)
	}
}

// TestWriteReportCaps checks the per-file line cap, the total cap
// with its footer, and that Full lifts both.
func TestWriteReportCaps(t *testing.T) {
	store := newTestStore(t)
	var before, after strings.Builder
	for number := range 100 {
		fmt.Fprintf(&before, "line %d\n", number)
		fmt.Fprintf(&after, "LINE %d\n", number)
	}
	for index := range 8 {
		writeProjectFile(t, store, fmt.Sprintf("c%d.txt", index),
			before.String(), 0o644)
	}
	mustTake(t, store, "before")
	for index := range 8 {
		writeProjectFile(t, store, fmt.Sprintf("c%d.txt", index),
			after.String(), 0o644)
	}
	identifier, changes := runDiff(t, store, "")

	var capped bytes.Buffer
	if err := WriteReport(&capped, store, identifier, changes,
		ReportOptions{}); err != nil {
		t.Fatalf("writing the capped report: %v", err)
	}
	text := capped.String()
	if got := strings.Count(text, "    ... 141 more lines\n"); got != 7 {
		t.Errorf("the capped report cut %d files, want 7", got)
	}
	if !strings.Contains(text, "  changed        c7.txt\n\n"+
		"1 more changed path(s) are listed above without their contents,\n"+
		"since the diff had already run to 400 lines. "+
		"`prison checkpoint diff --full`\n"+
		"prints all of it, and `[checkpoint] pager` in "+
		"~/.prison/config.toml pages it.\n\n"+
		"`prison checkpoint` records this as a new checkpoint.\n") {
		t.Errorf("the capped report's tail is wrong:\n%s", text)
	}
	bodyLines := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "    ") {
			bodyLines++
		}
	}
	if bodyLines != 7*(maximumDiffLinesPerFile+1) {
		t.Errorf("the capped report printed %d body lines, want %d",
			bodyLines, 7*(maximumDiffLinesPerFile+1))
	}

	var full bytes.Buffer
	if err := WriteReport(&full, store, identifier, changes,
		ReportOptions{Full: true}); err != nil {
		t.Fatalf("writing the full report: %v", err)
	}
	text = full.String()
	if strings.Contains(text, "more lines") ||
		strings.Contains(text, "more changed path") {
		t.Errorf("the full report still mentions a cap:\n%s", text)
	}
	if got := strings.Count(text, "    @@ -1,100 +1,100 @@\n"); got != 8 {
		t.Errorf("the full report has %d bodies, want 8", got)
	}
}

// buildUnifiedFixture creates a tree, calls record, then applies
// changes covering every unified diff event. The withBinary flag
// controls whether a binary file is included.
func buildUnifiedFixture(
	t *testing.T, store *Store, withBinary bool, record func()) {
	t.Helper()
	writeProjectFile(t, store, "keep.txt", "a\nb\nc\n", 0o644)
	writeProjectFile(t, store, "gone.txt", "bye\n", 0o644)
	writeProjectFile(t, store, "mode.sh", "#!/bin/sh\n", 0o644)
	writeProjectFile(t, store, "tail.txt", "a\nb", 0o644)
	linkProject(t, store, "link", "keep.txt")
	if withBinary {
		writeProjectFile(t, store, "bin.dat", "\x00\x01", 0o644)
	}
	record()
	writeProjectFile(t, store, "keep.txt", "a\nB\nc\n", 0o644)
	removeProject(t, store, "gone.txt")
	chmodProject(t, store, "mode.sh", 0o755)
	writeProjectFile(t, store, "tail.txt", "a\nc", 0o644)
	removeProject(t, store, "link")
	linkProject(t, store, "link", "other.txt")
	if withBinary {
		writeProjectFile(t, store, "bin.dat", "\x00\x02", 0o644)
	}
	writeProjectFile(t, store, "new.txt", "new\n", 0o644)
	mkdirProject(t, store, "newdir")
	linkProject(t, store, "newlink", "keep.txt")
}

// TestWriteUnifiedStream checks the unified diff output and notes
// against exact expected text for all change types.
func TestWriteUnifiedStream(t *testing.T) {
	store := newTestStore(t)
	buildUnifiedFixture(t, store, true, func() {
		mustTake(t, store, "before")
	})
	identifier, changes := runDiff(t, store, "")
	var output, notes bytes.Buffer
	if err := WriteUnified(&output, &notes, store, identifier,
		changes); err != nil {
		t.Fatalf("writing the unified stream: %v", err)
	}

	want := strings.Join([]string{
		"diff --git a/new.txt b/new.txt",
		"new file mode 100644",
		"index 0000000.." + shortDigest("new\n"),
		"--- /dev/null",
		"+++ b/new.txt",
		"@@ -0,0 +1 @@",
		"+new",
		"diff --git a/newlink b/newlink",
		"new file mode 120000",
		"index 0000000.." + shortDigest("keep.txt"),
		"--- /dev/null",
		"+++ b/newlink",
		"@@ -0,0 +1 @@",
		"+keep.txt",
		`\ No newline at end of file`,
		"diff --git a/gone.txt b/gone.txt",
		"deleted file mode 100644",
		"index " + shortDigest("bye\n") + "..0000000",
		"--- a/gone.txt",
		"+++ /dev/null",
		"@@ -1 +0,0 @@",
		"-bye",
		"diff --git a/bin.dat b/bin.dat",
		"index " + shortDigest("\x00\x01") + ".." + shortDigest("\x00\x02") +
			" 100644",
		"Binary files a/bin.dat and b/bin.dat differ",
		"diff --git a/keep.txt b/keep.txt",
		"index " + shortDigest("a\nb\nc\n") + ".." + shortDigest("a\nB\nc\n") +
			" 100644",
		"--- a/keep.txt",
		"+++ b/keep.txt",
		"@@ -1,3 +1,3 @@",
		" a",
		"-b",
		"+B",
		" c",
		"diff --git a/link b/link",
		"index " + shortDigest("keep.txt") + ".." + shortDigest("other.txt") +
			" 120000",
		"--- a/link",
		"+++ b/link",
		"@@ -1 +1 @@",
		"-keep.txt",
		`\ No newline at end of file`,
		"+other.txt",
		`\ No newline at end of file`,
		"diff --git a/mode.sh b/mode.sh",
		"old mode 100644",
		"new mode 100755",
		"index " + shortDigest("#!/bin/sh\n") + ".." +
			shortDigest("#!/bin/sh\n"),
		"diff --git a/tail.txt b/tail.txt",
		"index " + shortDigest("a\nb") + ".." + shortDigest("a\nc") + " 100644",
		"--- a/tail.txt",
		"+++ b/tail.txt",
		"@@ -1,2 +1,2 @@",
		" a",
		"-b",
		`\ No newline at end of file`,
		"+c",
		`\ No newline at end of file`,
		"",
	}, "\n")
	if output.String() != want {
		t.Errorf("stream:\n%s\nwant:\n%s", output.String(), want)
	}
	wantNotes := "9 path(s) changed since checkpoint 0001\n" +
		"directory added: newdir\n"
	if notes.String() != wantNotes {
		t.Errorf("notes:\n%s\nwant:\n%s", notes.String(), wantNotes)
	}
}

// TestUnifiedStreamAppliesWithGit feeds the unified stream to
// `git apply --check` to verify it parses. Skipped when git is
// not on PATH.
func TestUnifiedStreamAppliesWithGit(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	store := newTestStore(t)
	home := t.TempDir()
	git := func(arguments ...string) *exec.Cmd {
		command := exec.Command(gitPath, arguments...)
		command.Dir = store.Project
		command.Env = append(os.Environ(),
			"HOME="+home,
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@example.invalid",
		)
		return command
	}
	buildUnifiedFixture(t, store, false, func() {
		for _, arguments := range [][]string{
			{"init", "-q"},
			{"add", "-A"},
			{"commit", "-q", "-m", "before"},
		} {
			if output, err := git(arguments...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", arguments, err, output)
			}
		}
		mustTake(t, store, "before")
	})

	identifier, changes := runDiff(t, store, "")
	var stream, notes bytes.Buffer
	if err := WriteUnified(&stream, &notes, store, identifier,
		changes); err != nil {
		t.Fatalf("writing the unified stream: %v", err)
	}
	check := git("apply", "--check", "--cached")
	check.Stdin = bytes.NewReader(stream.Bytes())
	if output, err := check.CombinedOutput(); err != nil {
		t.Errorf("git apply --check rejected the stream: %v\n%s\n\n%s",
			err, output, stream.String())
	}
}
