package checkpoint

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func newTestStore(t *testing.T, patterns ...string) *Store {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if len(patterns) == 0 {
		patterns = DefaultIgnores
	}
	store, err := Open(filepath.Join(root, "state"), project,
		NewIgnore(patterns))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store
}

func writeProjectFile(
	t *testing.T, store *Store, relativePath, content string,
	mode os.FileMode) {
	t.Helper()
	absolute := filepath.Join(store.Project, relativePath)
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", relativePath, err)
	}
	if err := os.WriteFile(absolute, []byte(content), mode); err != nil {
		t.Fatalf("WriteFile(%s): %v", relativePath, err)
	}
	if err := os.Chmod(absolute, mode); err != nil {
		t.Fatalf("Chmod(%s): %v", relativePath, err)
	}
}

func manifestPaths(manifest Manifest) []string {
	paths := make([]string, 0, len(manifest))
	for _, entry := range manifest {
		paths = append(paths, entry.Path)
	}
	return paths
}

func TestTakeListHead(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "README.md", "hello\n", 0o644)
	writeProjectFile(t, store, "src/main.go", "package main\n", 0o644)

	first, err := store.Take("first")
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if first.ID != "0001" {
		t.Errorf("Take = %q, want 0001", first.ID)
	}
	if first.Entries != 3 {
		t.Errorf("Entries = %d, want 3", first.Entries)
	}
	if first.Time.IsZero() {
		t.Error("Time = zero, want the current time")
	}

	writeProjectFile(t, store, "src/main.go", "package main // edited\n", 0o644)
	second, err := store.Take("")
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if second.ID != "0002" {
		t.Errorf("Take = %q, want 0002", second.ID)
	}

	checkpoints, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(checkpoints) != 2 || checkpoints[0].ID != "0001" ||
		checkpoints[1].ID != "0002" {
		t.Fatalf("List = %+v, want 0001 then 0002", checkpoints)
	}
	if checkpoints[0].Label != "first" || checkpoints[1].Label != "" {
		t.Errorf("labels = %q, %q, want first and empty",
			checkpoints[0].Label, checkpoints[1].Label)
	}

	head, ok := store.Head()
	if !ok || head != "0002" {
		t.Errorf("Head = %q, %v, want 0002", head, ok)
	}
	if err := store.SetHead("0001"); err != nil {
		t.Fatalf("SetHead: %v", err)
	}
	if head, ok := store.Head(); !ok || head != "0001" {
		t.Errorf("Head after SetHead = %q, %v, want 0001", head, ok)
	}

	changes := Compare(mustManifest(t, store, "0001"),
		mustManifest(t, store, "0002"))
	if changes.Count() != 1 || changes.Changed[0].After.Path != "src/main.go" {
		t.Errorf("Compare = %+v, want src/main.go changed",
			changes)
	}
}

func mustManifest(t *testing.T, store *Store, identifier string) Manifest {
	t.Helper()
	manifest, err := store.Manifest(identifier)
	if err != nil {
		t.Fatalf("Manifest(%s): %v", identifier, err)
	}
	return manifest
}

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
		t.Fatalf("Scan: %v", err)
	}
	if len(skipped) != 0 {
		t.Errorf("Scan skipped = %+v, want none", skipped)
	}
	want := []string{
		"docs", "docs/public.md", "keep.txt", "nested", "nested/docs",
		"nested/docs/private", "nested/docs/private/kept.txt", "pkg", "src",
	}
	if got := manifestPaths(manifest); !slices.Equal(got, want) {
		t.Errorf("Scan = %v, want %v", got, want)
	}
}

func TestScanSymlinksAndModes(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "run.sh", "#!/bin/sh\necho hi\n", 0o755)
	writeProjectFile(t, store, "plain.txt", "plain\n", 0o644)
	writeProjectFile(t, store, "outside/deep.txt", "deep\n", 0o644)
	if err := os.Symlink("plain.txt",
		filepath.Join(store.Project, "alias.txt")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if err := os.Symlink("outside",
		filepath.Join(store.Project, "shortcut")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	manifest, _, err := store.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	byPath := manifest.Index()
	if entry := byPath["shortcut"]; entry.Kind != KindLink {
		t.Errorf("shortcut = %q, want link", entry.Kind)
	}
	if _, descended := byPath["shortcut/deep.txt"]; descended {
		t.Error("Scan = shortcut/deep.txt, want no entries under a symlink")
	}
	if entry := byPath["run.sh"]; entry.Mode != 0o755 {
		t.Errorf("run.sh mode = %v, want 0755", entry.Mode)
	}
	if entry := byPath["alias.txt"]; entry.Kind != KindLink ||
		entry.Mode != 0 || entry.Digest == "" {
		t.Errorf("alias.txt = %+v, want a link with a digest", entry)
	}
	if byPath["outside"].Kind != KindDirectory {
		t.Errorf("outside = %q, want dir",
			byPath["outside"].Kind)
	}

	if _, err := store.Take("with links"); err != nil {
		t.Fatalf("Take: %v", err)
	}
	target, err := store.Blob(byPath["alias.txt"].Digest)
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	if string(target) != "plain.txt" {
		t.Errorf("Blob = %q, want plain.txt", target)
	}
}

func TestScanBinaryAndTextBlobs(t *testing.T) {
	store := newTestStore(t)
	binary := string([]byte{0x89, 'P', 'N', 'G', 0x00, 0x1a, 0x0a})
	writeProjectFile(t, store, "image.png", binary, 0o644)
	writeProjectFile(t, store, "notes.txt", "some text\n", 0o644)

	if _, err := store.Take("mixed"); err != nil {
		t.Fatalf("Take: %v", err)
	}
	manifest := mustManifest(t, store, "0001").Index()
	stored, err := store.Blob(manifest["image.png"].Digest)
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	if string(stored) != binary {
		t.Errorf("Blob = %q, want %q", stored, binary)
	}
	if IsText(stored) {
		t.Error("IsText(PNG header) = true, want false")
	}
	text, err := store.Blob(manifest["notes.txt"].Digest)
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	if !IsText(text) {
		t.Error("IsText(text) = false, want true")
	}
}

func TestScanReportsSkipped(t *testing.T) {
	store := newTestStore(t, "*.ignored")
	writeProjectFile(t, store, "kept.txt", "kept\n", 0o644)
	pipe := filepath.Join(store.Project, "channel")
	if err := syscall.Mkfifo(pipe, 0o644); err != nil {
		t.Skipf("Mkfifo: %v", err)
	}
	if err := syscall.Mkfifo(
		filepath.Join(store.Project, "quiet.ignored"), 0o644); err != nil {
		t.Skipf("Mkfifo: %v", err)
	}

	manifest, skipped, err := store.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(skipped) != 1 || skipped[0].Path != "channel" {
		t.Fatalf("Scan skipped = %+v, want channel", skipped)
	}
	if !strings.Contains(skipped[0].Reason, "regular") {
		t.Errorf("Reason = %q, want it to name regular files", skipped[0].Reason)
	}
	if got := manifestPaths(manifest); !slices.Equal(got, []string{"kept.txt"}) {
		t.Errorf("Scan = %v, want kept.txt", got)
	}
}

func TestResolveAcceptsBothForms(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Resolve("1"); err == nil ||
		!strings.Contains(err.Error(), "no checkpoints for this project") {
		t.Errorf("Resolve on an empty store = %v, want an error", err)
	}
	writeProjectFile(t, store, "a.txt", "a\n", 0o644)
	for range 3 {
		if _, err := store.Take(""); err != nil {
			t.Fatalf("Take: %v", err)
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
		t.Errorf("Resolve(\"9\") = %v, want an error that lists the IDs", err)
	}
}

func TestNumericOrderPastFourDigits(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "a.txt", "a\n", 0o644)
	for _, identifier := range []string{"0002", "0010", "9999"} {
		writeSnapshotByHand(t, store, identifier)
	}

	taken, err := store.Take("past the pad")
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if taken.ID != "10000" {
		t.Errorf("Take = %q, want 10000", taken.ID)
	}
	checkpoints, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
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
		t.Errorf("Resolve(\"10000\") = %q, %v, want 10000", resolved, err)
	}
	if err := store.Remove("9999"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := store.Resolve("9999"); err == nil {
		t.Error("Resolve(\"9999\") = nil error, want an error")
	}
	if head, ok := store.Head(); !ok || head != "10000" {
		t.Errorf("Head = %q, %v, want 10000",
			head, ok)
	}
}

func writeSnapshotByHand(t *testing.T, store *Store, identifier string) {
	t.Helper()
	manifest := store.snapshotPath(identifier, manifestSuffix)
	if err := os.WriteFile(manifest, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(%s manifest): %v", identifier, err)
	}
	metadata := store.snapshotPath(identifier, metadataSuffix)
	document := `{"time": "2026-01-01T00:00:00+00:00", "label": "", ` +
		`"entries": 0}` + "\n"
	if err := os.WriteFile(metadata, []byte(document), 0o644); err != nil {
		t.Fatalf("WriteFile(%s metadata): %v", identifier, err)
	}
}

func TestRemoveClearsHead(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "a.txt", "a\n", 0o644)
	if _, err := store.Take(""); err != nil {
		t.Fatalf("Take: %v", err)
	}
	if err := store.Remove("1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if head, ok := store.Head(); ok {
		t.Errorf("Head = %q, want none", head)
	}
	if _, err := store.Manifest("0001"); err == nil {
		t.Error("Manifest(0001) = nil error, want an error")
	}
}

func TestPruneCountsWhatItReclaims(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "shared.txt", "shared\n", 0o644)
	writeProjectFile(t, store, "doomed.txt", "0123456789\n", 0o644)
	if _, err := store.Take("first"); err != nil {
		t.Fatalf("Take: %v", err)
	}
	if err := os.Remove(
		filepath.Join(store.Project, "doomed.txt")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := store.Take("second"); err != nil {
		t.Fatalf("Take: %v", err)
	}

	if removed, freed, err := store.Prune(); err != nil ||
		removed != 0 || freed != 0 {
		t.Errorf("Prune = %d, %d, %v, want 0, 0, nil",
			removed, freed, err)
	}
	if err := store.Remove("1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	removed, freed, err := store.Prune()
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 1 || freed != 11 {
		t.Errorf("Prune = %d objects, %d bytes, want 1 and 11", removed, freed)
	}
	manifest := mustManifest(t, store, "0002").Index()
	if _, err := store.Blob(manifest["shared.txt"].Digest); err != nil {
		t.Errorf("Blob(shared.txt) = %v, want nil", err)
	}
	if removed, freed, err := store.Prune(); err != nil ||
		removed != 0 || freed != 0 {
		t.Errorf("second Prune = %d, %d, %v, want 0, 0, nil",
			removed, freed, err)
	}
}

func TestBlobPathShards(t *testing.T) {
	store := newTestStore(t)
	digest := strings.Repeat("a", 64)
	want := filepath.Join(store.Dir, "objects", "aa", strings.Repeat("a", 62))
	if got := store.BlobPath(digest); got != want {
		t.Errorf("BlobPath = %q, want %q", got, want)
	}
}

func TestScanRefusesAnOversizedTree(t *testing.T) {
	store := newTestStore(t)
	huge := filepath.Join(store.Project, "huge.bin")
	handle, err := os.Create(huge)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := handle.Truncate(maximumTotalBytes + 1); err != nil {
		handle.Close()
		t.Skipf("Truncate: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, _, err = store.Scan()
	if err == nil || !strings.Contains(err.Error(), "2GB") {
		t.Errorf("Scan = %v, want a 2GB error", err)
	}
}

func TestScanRefusesTooManyEntries(t *testing.T) {
	state := &scanState{store: newTestStore(t), entries: Manifest{}}
	for count := range maximumEntryCount {
		if err := state.append(Entry{Kind: KindFile}); err != nil {
			t.Fatalf("append(entry %d) = %v, want nil", count, err)
		}
	}
	err := state.append(Entry{Kind: KindFile})
	if err == nil || !strings.Contains(err.Error(), "100000 entries") {
		t.Errorf("append past the limit = %v, want an error", err)
	}
}
