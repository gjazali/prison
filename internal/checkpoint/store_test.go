package checkpoint

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// newTestStore returns a store with separate project and state
// directories. Uses the given ignore patterns, or defaults.
func newTestStore(t *testing.T, patterns ...string) *Store {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("creating the project directory: %v", err)
	}
	if len(patterns) == 0 {
		patterns = DefaultIgnores
	}
	store, err := Open(filepath.Join(root, "state"), project,
		NewIgnore(patterns))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	return store
}

// writeProjectFile creates a file in the project tree, making
// parent directories as needed.
func writeProjectFile(
	t *testing.T, store *Store, relativePath, content string,
	mode os.FileMode) {
	t.Helper()
	absolute := filepath.Join(store.Project, relativePath)
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatalf("creating the directory holding %s: %v", relativePath, err)
	}
	if err := os.WriteFile(absolute, []byte(content), mode); err != nil {
		t.Fatalf("writing %s: %v", relativePath, err)
	}
	if err := os.Chmod(absolute, mode); err != nil {
		t.Fatalf("setting the permissions of %s: %v", relativePath, err)
	}
}

// manifestPaths returns all paths in a manifest as a slice.
func manifestPaths(manifest Manifest) []string {
	paths := make([]string, 0, len(manifest))
	for _, entry := range manifest {
		paths = append(paths, entry.Path)
	}
	return paths
}

// TestTakeListHead checks taking two checkpoints, listing them in
// order, and HEAD tracking the newest.
func TestTakeListHead(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "README.md", "hello\n", 0o644)
	writeProjectFile(t, store, "src/main.go", "package main\n", 0o644)

	first, err := store.Take("first")
	if err != nil {
		t.Fatalf("taking the first checkpoint: %v", err)
	}
	if first.ID != "0001" {
		t.Errorf("first checkpoint is %q, want 0001", first.ID)
	}
	if first.Entries != 3 {
		t.Errorf("first checkpoint recorded %d entries, want 3", first.Entries)
	}
	if first.Time.IsZero() {
		t.Error("the first checkpoint recorded no time")
	}

	writeProjectFile(t, store, "src/main.go", "package main // edited\n", 0o644)
	second, err := store.Take("")
	if err != nil {
		t.Fatalf("taking the second checkpoint: %v", err)
	}
	if second.ID != "0002" {
		t.Errorf("second checkpoint is %q, want 0002", second.ID)
	}

	checkpoints, err := store.List()
	if err != nil {
		t.Fatalf("listing checkpoints: %v", err)
	}
	if len(checkpoints) != 2 || checkpoints[0].ID != "0001" ||
		checkpoints[1].ID != "0002" {
		t.Fatalf("List = %+v, want 0001 then 0002", checkpoints)
	}
	if checkpoints[0].Label != "first" || checkpoints[1].Label != "" {
		t.Errorf("labels = %q, %q", checkpoints[0].Label, checkpoints[1].Label)
	}

	head, ok := store.Head()
	if !ok || head != "0002" {
		t.Errorf("Head = %q, %v, want 0002", head, ok)
	}
	if err := store.SetHead("0001"); err != nil {
		t.Fatalf("setting HEAD: %v", err)
	}
	if head, ok := store.Head(); !ok || head != "0001" {
		t.Errorf("Head after SetHead = %q, %v, want 0001", head, ok)
	}

	changes := Compare(mustManifest(t, store, "0001"),
		mustManifest(t, store, "0002"))
	if changes.Count() != 1 || changes.Changed[0].After.Path != "src/main.go" {
		t.Errorf("Compare between the two = %+v, want src/main.go changed",
			changes)
	}
}

// mustManifest reads a checkpoint's manifest. Fails the test on
// error.
func mustManifest(t *testing.T, store *Store, identifier string) Manifest {
	t.Helper()
	manifest, err := store.Manifest(identifier)
	if err != nil {
		t.Fatalf("reading the manifest of %s: %v", identifier, err)
	}
	return manifest
}

// TestScanIgnoresAndPrunes checks that ignored paths are excluded
// from the manifest at every depth and that rooted patterns only
// match at the root.
func TestScanIgnoresAndPrunes(t *testing.T) {
	store := newTestStore(t, ".git/", "build/", "*.pyc", "docs/private")
	writeProjectFile(t, store, "keep.txt", "keep\n", 0o644)
	writeProjectFile(t, store, ".git/config", "junk\n", 0o644)
	writeProjectFile(t, store, "build/artifact.bin", "junk\n", 0o644)
	writeProjectFile(t, store, "src/build/dropped.txt", "junk\n", 0o644)
	writeProjectFile(t, store, "pkg/module.pyc", "junk\n", 0o644)
	writeProjectFile(t, store, "docs/private/secret.txt", "junk\n", 0o644)
	writeProjectFile(t, store, "docs/public.md", "public\n", 0o644)
	writeProjectFile(t, store, "nested/docs/private/kept.txt", "kept\n", 0o644)

	manifest, skipped, err := store.Scan()
	if err != nil {
		t.Fatalf("scanning: %v", err)
	}
	if len(skipped) != 0 {
		t.Errorf("Scan skipped %+v, want nothing", skipped)
	}
	want := []string{
		"docs", "docs/public.md", "keep.txt", "nested", "nested/docs",
		"nested/docs/private", "nested/docs/private/kept.txt", "pkg", "src",
	}
	if got := manifestPaths(manifest); !slices.Equal(got, want) {
		t.Errorf("Scan recorded %v, want %v", got, want)
	}
}

// TestScanSymlinksAndModes checks that symlinks are recorded by
// target, not followed, and that the executable bit round-trips.
func TestScanSymlinksAndModes(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "run.sh", "#!/bin/sh\necho hi\n", 0o755)
	writeProjectFile(t, store, "plain.txt", "plain\n", 0o644)
	writeProjectFile(t, store, "outside/deep.txt", "deep\n", 0o644)
	if err := os.Symlink("plain.txt",
		filepath.Join(store.Project, "alias.txt")); err != nil {
		t.Fatalf("creating a file symlink: %v", err)
	}
	if err := os.Symlink("outside",
		filepath.Join(store.Project, "shortcut")); err != nil {
		t.Fatalf("creating a directory symlink: %v", err)
	}

	manifest, _, err := store.Scan()
	if err != nil {
		t.Fatalf("scanning: %v", err)
	}
	byPath := manifest.Index()
	if entry := byPath["shortcut"]; entry.Kind != KindLink {
		t.Errorf("shortcut recorded as %q, want a link", entry.Kind)
	}
	if _, descended := byPath["shortcut/deep.txt"]; descended {
		t.Error("the scan descended into a symlinked directory")
	}
	if entry := byPath["run.sh"]; entry.Mode != 0o755 {
		t.Errorf("run.sh recorded mode %v, want 0755", entry.Mode)
	}
	if entry := byPath["alias.txt"]; entry.Kind != KindLink ||
		entry.Mode != 0 || entry.Digest == "" {
		t.Errorf("alias.txt recorded as %+v, want a link with a digest", entry)
	}
	if byPath["outside"].Kind != KindDirectory {
		t.Errorf("outside recorded as %q, want a directory",
			byPath["outside"].Kind)
	}

	if _, err := store.Take("with links"); err != nil {
		t.Fatalf("taking a checkpoint: %v", err)
	}
	target, err := store.Blob(byPath["alias.txt"].Digest)
	if err != nil {
		t.Fatalf("reading the stored link target: %v", err)
	}
	if string(target) != "plain.txt" {
		t.Errorf("the stored link target is %q, want plain.txt", target)
	}
}

// TestScanBinaryAndTextBlobs checks that binary files are stored
// whole and detected as non-text.
func TestScanBinaryAndTextBlobs(t *testing.T) {
	store := newTestStore(t)
	binary := string([]byte{0x89, 'P', 'N', 'G', 0x00, 0x1a, 0x0a})
	writeProjectFile(t, store, "image.png", binary, 0o644)
	writeProjectFile(t, store, "notes.txt", "some text\n", 0o644)

	if _, err := store.Take("mixed"); err != nil {
		t.Fatalf("taking a checkpoint: %v", err)
	}
	manifest := mustManifest(t, store, "0001").Index()
	stored, err := store.Blob(manifest["image.png"].Digest)
	if err != nil {
		t.Fatalf("reading the stored binary: %v", err)
	}
	if string(stored) != binary {
		t.Errorf("the stored binary is %q, want %q", stored, binary)
	}
	if IsText(stored) {
		t.Error("IsText called a PNG header text")
	}
	text, err := store.Blob(manifest["notes.txt"].Digest)
	if err != nil {
		t.Fatalf("reading the stored text: %v", err)
	}
	if !IsText(text) {
		t.Error("IsText called a text file binary")
	}
}

// TestScanReportsSkipped checks that unsupported file types are
// reported as skipped, and ignored ones are not.
func TestScanReportsSkipped(t *testing.T) {
	store := newTestStore(t, "*.ignored")
	writeProjectFile(t, store, "kept.txt", "kept\n", 0o644)
	pipe := filepath.Join(store.Project, "channel")
	if err := syscall.Mkfifo(pipe, 0o644); err != nil {
		t.Skipf("this filesystem will not hold a named pipe: %v", err)
	}
	if err := syscall.Mkfifo(
		filepath.Join(store.Project, "quiet.ignored"), 0o644); err != nil {
		t.Skipf("this filesystem will not hold a named pipe: %v", err)
	}

	manifest, skipped, err := store.Scan()
	if err != nil {
		t.Fatalf("scanning: %v", err)
	}
	if len(skipped) != 1 || skipped[0].Path != "channel" {
		t.Fatalf("Scan skipped %+v, want only channel", skipped)
	}
	if !strings.Contains(skipped[0].Reason, "regular") {
		t.Errorf("the reason for skipping is %q, want it to mention "+
			"regular files", skipped[0].Reason)
	}
	if got := manifestPaths(manifest); !slices.Equal(got, []string{"kept.txt"}) {
		t.Errorf("Scan recorded %v, want only kept.txt", got)
	}
}

// TestResolveAcceptsBothForms checks short and padded identifier
// forms and the error listing available checkpoints.
func TestResolveAcceptsBothForms(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Resolve("1"); err == nil ||
		!strings.Contains(err.Error(), "no checkpoints for this project") {
		t.Errorf("Resolve on an empty store gave %v", err)
	}
	writeProjectFile(t, store, "a.txt", "a\n", 0o644)
	for range 3 {
		if _, err := store.Take(""); err != nil {
			t.Fatalf("taking a checkpoint: %v", err)
		}
	}
	for _, wanted := range []string{"3", "0003", " 3 "} {
		got, err := store.Resolve(wanted)
		if err != nil || got != "0003" {
			t.Errorf("Resolve(%q) = %q, %v, want 0003", wanted, got, err)
		}
	}
	_, err := store.Resolve("9")
	if err == nil || !strings.Contains(err.Error(), "0001, 0002, 0003") {
		t.Errorf("Resolve(\"9\") gave %v, want the existing ids listed", err)
	}
}

// TestNumericOrderPastFourDigits checks that identifiers sort
// numerically and that operations work past 9999.
func TestNumericOrderPastFourDigits(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "a.txt", "a\n", 0o644)
	for _, identifier := range []string{"0002", "0010", "9999"} {
		writeSnapshotByHand(t, store, identifier)
	}

	taken, err := store.Take("past the pad")
	if err != nil {
		t.Fatalf("taking a checkpoint: %v", err)
	}
	if taken.ID != "10000" {
		t.Errorf("the new checkpoint is %q, want 10000", taken.ID)
	}
	checkpoints, err := store.List()
	if err != nil {
		t.Fatalf("listing checkpoints: %v", err)
	}
	want := []string{"0002", "0010", "9999", "10000"}
	got := make([]string, 0, len(checkpoints))
	for _, checkpoint := range checkpoints {
		got = append(got, checkpoint.ID)
	}
	if !slices.Equal(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}
	if resolved, err := store.Resolve("10000"); err != nil ||
		resolved != "10000" {
		t.Errorf("Resolve(\"10000\") = %q, %v", resolved, err)
	}
	if err := store.Remove("9999"); err != nil {
		t.Fatalf("removing 9999: %v", err)
	}
	if _, err := store.Resolve("9999"); err == nil {
		t.Error("9999 still resolves after being removed")
	}
	if head, ok := store.Head(); !ok || head != "10000" {
		t.Errorf("Head = %q, %v, want 10000 to survive removing 9999",
			head, ok)
	}
}

// writeSnapshotByHand creates an empty checkpoint with the given
// identifier directly, bypassing Take.
func writeSnapshotByHand(t *testing.T, store *Store, identifier string) {
	t.Helper()
	manifest := store.snapshotPath(identifier, manifestSuffix)
	if err := os.WriteFile(manifest, nil, 0o644); err != nil {
		t.Fatalf("writing the manifest of %s: %v", identifier, err)
	}
	metadata := store.snapshotPath(identifier, metadataSuffix)
	document := `{"time": "2026-01-01T00:00:00+00:00", "label": "", ` +
		`"entries": 0}` + "\n"
	if err := os.WriteFile(metadata, []byte(document), 0o644); err != nil {
		t.Fatalf("writing the metadata of %s: %v", identifier, err)
	}
}

// TestRemoveClearsHead checks that removing the current checkpoint
// clears HEAD.
func TestRemoveClearsHead(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "a.txt", "a\n", 0o644)
	if _, err := store.Take(""); err != nil {
		t.Fatalf("taking a checkpoint: %v", err)
	}
	if err := store.Remove("1"); err != nil {
		t.Fatalf("removing the checkpoint: %v", err)
	}
	if head, ok := store.Head(); ok {
		t.Errorf("Head = %q after removing the checkpoint it named", head)
	}
	if _, err := store.Manifest("0001"); err == nil {
		t.Error("the manifest survived Remove")
	}
}

// TestPruneCountsWhatItReclaims checks that unreferenced objects
// are removed, referenced ones stay, and a second prune finds
// nothing.
func TestPruneCountsWhatItReclaims(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "shared.txt", "shared\n", 0o644)
	writeProjectFile(t, store, "doomed.txt", "0123456789\n", 0o644)
	if _, err := store.Take("first"); err != nil {
		t.Fatalf("taking the first checkpoint: %v", err)
	}
	if err := os.Remove(
		filepath.Join(store.Project, "doomed.txt")); err != nil {
		t.Fatalf("removing doomed.txt: %v", err)
	}
	if _, err := store.Take("second"); err != nil {
		t.Fatalf("taking the second checkpoint: %v", err)
	}

	if removed, freed, err := store.Prune(); err != nil ||
		removed != 0 || freed != 0 {
		t.Errorf("Prune with both checkpoints = %d, %d, %v, want nothing",
			removed, freed, err)
	}
	if err := store.Remove("1"); err != nil {
		t.Fatalf("removing the first checkpoint: %v", err)
	}
	removed, freed, err := store.Prune()
	if err != nil {
		t.Fatalf("pruning: %v", err)
	}
	if removed != 1 || freed != 11 {
		t.Errorf("Prune = %d objects, %d bytes, want 1 and 11", removed, freed)
	}
	manifest := mustManifest(t, store, "0002").Index()
	if _, err := store.Blob(manifest["shared.txt"].Digest); err != nil {
		t.Errorf("Prune removed a still-referenced object: %v", err)
	}
	if removed, freed, err := store.Prune(); err != nil ||
		removed != 0 || freed != 0 {
		t.Errorf("a second Prune = %d, %d, %v, want nothing",
			removed, freed, err)
	}
}

// TestBlobPathShards checks the two-character shard directory
// layout.
func TestBlobPathShards(t *testing.T) {
	store := newTestStore(t)
	digest := strings.Repeat("a", 64)
	want := filepath.Join(store.Dir, "objects", "aa", strings.Repeat("a", 62))
	if got := store.BlobPath(digest); got != want {
		t.Errorf("BlobPath = %q, want %q", got, want)
	}
}

// TestScanRefusesAnOversizedTree checks the 2 GiB file size limit
// using a sparse file. Skips if the filesystem does not support
// sparse files.
func TestScanRefusesAnOversizedTree(t *testing.T) {
	store := newTestStore(t)
	huge := filepath.Join(store.Project, "huge.bin")
	handle, err := os.Create(huge)
	if err != nil {
		t.Fatalf("creating the large file: %v", err)
	}
	if err := handle.Truncate(maximumTotalBytes + 1); err != nil {
		handle.Close()
		t.Skipf("this filesystem will not hold a sparse file: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("closing the large file: %v", err)
	}

	_, _, err = store.Scan()
	if err == nil || !strings.Contains(err.Error(), "2GB") {
		t.Errorf("scanning an oversized tree gave %v, want a refusal", err)
	}
}

// TestScanRefusesTooManyEntries checks the entry-count limit using
// a manifest built directly.
func TestScanRefusesTooManyEntries(t *testing.T) {
	state := &scanState{store: newTestStore(t), entries: Manifest{}}
	for count := range maximumEntryCount {
		if err := state.append(Entry{Kind: KindFile}); err != nil {
			t.Fatalf("entry %d was refused early: %v", count, err)
		}
	}
	err := state.append(Entry{Kind: KindFile})
	if err == nil || !strings.Contains(err.Error(), "100000 entries") {
		t.Errorf("the entry past the limit gave %v, want a refusal", err)
	}
}
