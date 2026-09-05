package checkpoint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRestoreRemovesAndRecreates checks that restore replaces the
// tree with the checkpoint's content, records the old tree first,
// and sets HEAD.
func TestRestoreRemovesAndRecreates(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "keep.txt", "one\n", 0o644)
	writeProjectFile(t, store, "run.sh", "#!/bin/sh\n", 0o755)
	writeProjectFile(t, store, "src/main.go", "package main\n", 0o644)
	if err := os.Symlink("keep.txt",
		filepath.Join(store.Project, "alias.txt")); err != nil {
		t.Fatalf("creating a symlink: %v", err)
	}
	if _, err := store.Take("original"); err != nil {
		t.Fatalf("taking the first checkpoint: %v", err)
	}

	writeProjectFile(t, store, "keep.txt", "two\n", 0o644)
	writeProjectFile(t, store, "later/extra.txt", "extra\n", 0o644)
	if err := os.Chmod(
		filepath.Join(store.Project, "run.sh"), 0o644); err != nil {
		t.Fatalf("clearing the executable bit: %v", err)
	}
	if err := os.RemoveAll(
		filepath.Join(store.Project, "src")); err != nil {
		t.Fatalf("removing src: %v", err)
	}
	if err := os.Remove(
		filepath.Join(store.Project, "alias.txt")); err != nil {
		t.Fatalf("removing the symlink: %v", err)
	}

	result, err := store.Restore("1")
	if err != nil {
		t.Fatalf("restoring: %v", err)
	}
	if result.Automatic != "0002" {
		t.Errorf("the automatic checkpoint is %q, want 0002", result.Automatic)
	}
	if result.Written != 5 {
		t.Errorf("Restore wrote %d entries, want 5", result.Written)
	}
	if result.Removed != 2 {
		t.Errorf("Restore removed %d paths, want 2", result.Removed)
	}

	automatic, err := store.Metadata(result.Automatic)
	if err != nil {
		t.Fatalf("reading the automatic checkpoint: %v", err)
	}
	if automatic.Label != "automatic, before restoring 0001" {
		t.Errorf("the automatic label is %q", automatic.Label)
	}

	content, err := os.ReadFile(filepath.Join(store.Project, "keep.txt"))
	if err != nil || string(content) != "one\n" {
		t.Errorf("keep.txt = %q, %v, want the recorded contents", content, err)
	}
	information, err := os.Lstat(filepath.Join(store.Project, "run.sh"))
	if err != nil || information.Mode().Perm() != 0o755 {
		t.Errorf("run.sh mode = %v, %v, want 0755", information.Mode(), err)
	}
	if _, err := os.Stat(
		filepath.Join(store.Project, "src/main.go")); err != nil {
		t.Errorf("src/main.go was not put back: %v", err)
	}
	if _, err := os.Lstat(
		filepath.Join(store.Project, "later")); !os.IsNotExist(err) {
		t.Error("the directory added since the checkpoint survived")
	}
	target, err := os.Readlink(filepath.Join(store.Project, "alias.txt"))
	if err != nil || target != "keep.txt" {
		t.Errorf("alias.txt = %q, %v, want a link to keep.txt", target, err)
	}
	if head, ok := store.Head(); !ok || head != "0001" {
		t.Errorf("Head = %q, %v, want 0001", head, ok)
	}

	changes := Compare(mustManifest(t, store, "0001"), mustScan(t, store))
	if !changes.Empty() {
		t.Errorf("the tree still differs from the checkpoint: %+v", changes)
	}
}

// mustScan scans the project tree. Fails the test on error.
func mustScan(t *testing.T, store *Store) Manifest {
	t.Helper()
	manifest, _, err := store.Scan()
	if err != nil {
		t.Fatalf("scanning: %v", err)
	}
	return manifest
}

// TestRestoreWithoutAutomaticTakesNothing checks that restoring
// without an automatic checkpoint does not add one.
func TestRestoreWithoutAutomaticTakesNothing(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "a.txt", "one\n", 0o644)
	if _, err := store.Take("first"); err != nil {
		t.Fatalf("taking a checkpoint: %v", err)
	}
	writeProjectFile(t, store, "a.txt", "two\n", 0o644)
	if _, err := store.Take("second"); err != nil {
		t.Fatalf("taking a checkpoint: %v", err)
	}

	result, err := store.RestoreWithoutAutomatic("0001")
	if err != nil {
		t.Fatalf("restoring: %v", err)
	}
	if result.Automatic != "" {
		t.Errorf("an automatic checkpoint %q was taken", result.Automatic)
	}
	checkpoints, err := store.List()
	if err != nil {
		t.Fatalf("listing checkpoints: %v", err)
	}
	if len(checkpoints) != 2 {
		t.Errorf("the store holds %d checkpoints, want 2", len(checkpoints))
	}
	content, err := os.ReadFile(filepath.Join(store.Project, "a.txt"))
	if err != nil || string(content) != "one\n" {
		t.Errorf("a.txt = %q, %v, want the first contents", content, err)
	}
}

// TestRestoreReportsMissingObject checks that restoring a pruned
// checkpoint fails instead of producing an incomplete tree.
func TestRestoreReportsMissingObject(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "a.txt", "one\n", 0o644)
	if _, err := store.Take(""); err != nil {
		t.Fatalf("taking a checkpoint: %v", err)
	}
	manifest := mustManifest(t, store, "0001").Index()
	if err := os.Remove(
		store.BlobPath(manifest["a.txt"].Digest)); err != nil {
		t.Fatalf("removing the stored object: %v", err)
	}
	_, err := store.RestoreWithoutAutomatic("0001")
	if err == nil || !strings.Contains(err.Error(), "a.txt") {
		t.Errorf("restoring with a missing object gave %v", err)
	}
}

// TestStepOrderingWithGaps checks that back and forward skip
// removed checkpoints and refuse at each end.
func TestStepOrderingWithGaps(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "a.txt", "a\n", 0o644)
	for _, identifier := range []string{"0001", "0005", "0009", "0012"} {
		writeSnapshotByHand(t, store, identifier)
	}

	if err := store.SetHead("0009"); err != nil {
		t.Fatalf("setting HEAD: %v", err)
	}
	if target, err := store.Step(-1); err != nil || target != "0005" {
		t.Errorf("Step(-1) from 0009 = %q, %v, want 0005", target, err)
	}
	if target, err := store.Step(+1); err != nil || target != "0012" {
		t.Errorf("Step(+1) from 0009 = %q, %v, want 0012", target, err)
	}

	if err := store.SetHead("0001"); err != nil {
		t.Fatalf("setting HEAD: %v", err)
	}
	_, err := store.Step(-1)
	if err == nil || !strings.Contains(err.Error(),
		"checkpoint 0001 is the earliest one") {
		t.Errorf("Step(-1) from the earliest gave %v", err)
	}

	if err := store.SetHead("0012"); err != nil {
		t.Fatalf("setting HEAD: %v", err)
	}
	_, err = store.Step(+1)
	if err == nil || !strings.Contains(err.Error(),
		"checkpoint 0012 is the latest one") {
		t.Errorf("Step(+1) from the latest gave %v", err)
	}

	if err := store.SetHead("0007"); err != nil {
		t.Fatalf("setting a stale HEAD: %v", err)
	}
	if target, err := store.Step(-1); err != nil || target != "0009" {
		t.Errorf("Step(-1) with a stale HEAD = %q, %v, want 0009 counted "+
			"from the newest", target, err)
	}

	empty := newTestStore(t)
	if _, err := empty.Step(-1); err == nil ||
		!strings.Contains(err.Error(), "no checkpoints for this project") {
		t.Errorf("Step on an empty store gave %v", err)
	}
}

// TestStepThenRestoreMovesHead checks that Step picks the target
// and RestoreWithoutAutomatic applies it.
func TestStepThenRestoreMovesHead(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "a.txt", "one\n", 0o644)
	if _, err := store.Take(""); err != nil {
		t.Fatalf("taking a checkpoint: %v", err)
	}
	writeProjectFile(t, store, "a.txt", "two\n", 0o644)
	if _, err := store.Take(""); err != nil {
		t.Fatalf("taking a checkpoint: %v", err)
	}

	target, err := store.Step(-1)
	if err != nil {
		t.Fatalf("stepping back: %v", err)
	}
	if _, err := store.RestoreWithoutAutomatic(target); err != nil {
		t.Fatalf("restoring %s: %v", target, err)
	}
	if head, ok := store.Head(); !ok || head != "0001" {
		t.Errorf("Head = %q, %v, want 0001", head, ok)
	}
	if _, err := store.Manifest("0003"); err == nil {
		t.Error("stepping back recorded an automatic checkpoint")
	}
}

// TestWriteEntry checks writing a directory, a file with its
// recorded mode, and a link replacing a file.
func TestWriteEntry(t *testing.T) {
	project := t.TempDir()
	if err := WriteEntry(project,
		Entry{Kind: KindDirectory, Path: "nested/inner"}, nil); err != nil {
		t.Fatalf("writing a directory: %v", err)
	}
	if information, err := os.Stat(
		filepath.Join(project, "nested/inner")); err != nil ||
		!information.IsDir() {
		t.Errorf("the directory was not created: %v", err)
	}

	entry := Entry{Kind: KindFile, Mode: 0o600, Path: "nested/secret.txt"}
	if err := WriteEntry(project, entry, []byte("hush\n")); err != nil {
		t.Fatalf("writing a file: %v", err)
	}
	information, err := os.Lstat(filepath.Join(project, "nested/secret.txt"))
	if err != nil || information.Mode().Perm() != 0o600 {
		t.Errorf("the file mode is %v, %v, want 0600", information.Mode(), err)
	}
	if _, err := os.Lstat(filepath.Join(project,
		"nested/secret.txt"+restoreTemporarySuffix)); !os.IsNotExist(err) {
		t.Error("the temporary file was left behind")
	}

	if err := WriteEntry(project,
		Entry{Kind: KindFile, Path: "nested/secret.txt"},
		[]byte("again\n")); err != nil {
		t.Fatalf("overwriting a file: %v", err)
	}
	information, err = os.Lstat(filepath.Join(project, "nested/secret.txt"))
	if err != nil || information.Mode().Perm() != 0o644 {
		t.Errorf("an entry with no mode wrote %v, %v, want 0644",
			information.Mode(), err)
	}

	if err := WriteEntry(project,
		Entry{Kind: KindLink, Path: "nested/secret.txt"},
		[]byte("elsewhere.txt")); err != nil {
		t.Fatalf("replacing a file with a link: %v", err)
	}
	target, err := os.Readlink(filepath.Join(project, "nested/secret.txt"))
	if err != nil || target != "elsewhere.txt" {
		t.Errorf("the link points at %q, %v, want elsewhere.txt", target, err)
	}
}
