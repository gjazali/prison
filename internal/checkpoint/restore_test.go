package checkpoint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreRemovesAndRecreates(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "keep.txt", "one\n", 0o644)
	writeProjectFile(t, store, "run.sh", "#!/bin/sh\n", 0o755)
	writeProjectFile(t, store, "src/main.go", "package main\n", 0o644)
	if err := os.Symlink("keep.txt",
		filepath.Join(store.Project, "alias.txt")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if _, err := store.Take("original"); err != nil {
		t.Fatalf("Take: %v", err)
	}

	writeProjectFile(t, store, "keep.txt", "two\n", 0o644)
	writeProjectFile(t, store, "later/extra.txt", "extra\n", 0o644)
	if err := os.Chmod(
		filepath.Join(store.Project, "run.sh"), 0o644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if err := os.RemoveAll(
		filepath.Join(store.Project, "src")); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if err := os.Remove(
		filepath.Join(store.Project, "alias.txt")); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	result, err := store.Restore("1")
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if result.Automatic != "0002" {
		t.Errorf("Automatic = %q, want 0002", result.Automatic)
	}
	if result.Written != 5 {
		t.Errorf("Written = %d, want 5", result.Written)
	}
	if result.Removed != 2 {
		t.Errorf("Removed = %d, want 2", result.Removed)
	}

	automatic, err := store.Metadata(result.Automatic)
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if automatic.Label != "automatic, before restoring 0001" {
		t.Errorf("Label = %q, want %q", automatic.Label,
			"automatic, before restoring 0001")
	}

	content, err := os.ReadFile(filepath.Join(store.Project, "keep.txt"))
	if err != nil || string(content) != "one\n" {
		t.Errorf("keep.txt = %q, %v, want one", content, err)
	}
	information, err := os.Lstat(filepath.Join(store.Project, "run.sh"))
	if err != nil || information.Mode().Perm() != 0o755 {
		t.Errorf("run.sh mode = %v, %v, want 0755", information.Mode(), err)
	}
	if _, err := os.Stat(
		filepath.Join(store.Project, "src/main.go")); err != nil {
		t.Errorf("Stat(src/main.go) = %v, want nil", err)
	}
	if _, err := os.Lstat(
		filepath.Join(store.Project, "later")); !os.IsNotExist(err) {
		t.Error("later exists, want removed")
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

func mustScan(t *testing.T, store *Store) Manifest {
	t.Helper()
	manifest, _, err := store.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return manifest
}

func TestRestoreWithoutAutomaticTakesNothing(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "a.txt", "one\n", 0o644)
	if _, err := store.Take("first"); err != nil {
		t.Fatalf("Take: %v", err)
	}
	writeProjectFile(t, store, "a.txt", "two\n", 0o644)
	if _, err := store.Take("second"); err != nil {
		t.Fatalf("Take: %v", err)
	}

	result, err := store.RestoreWithoutAutomatic("0001")
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if result.Automatic != "" {
		t.Errorf("Automatic = %q, want empty", result.Automatic)
	}
	checkpoints, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(checkpoints) != 2 {
		t.Errorf("List = %d checkpoints, want 2", len(checkpoints))
	}
	content, err := os.ReadFile(filepath.Join(store.Project, "a.txt"))
	if err != nil || string(content) != "one\n" {
		t.Errorf("a.txt = %q, %v, want one", content, err)
	}
}

func TestRestoreReportsMissingObject(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "a.txt", "one\n", 0o644)
	if _, err := store.Take(""); err != nil {
		t.Fatalf("Take: %v", err)
	}
	manifest := mustManifest(t, store, "0001").Index()
	if err := os.Remove(
		store.BlobPath(manifest["a.txt"].Digest)); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	_, err := store.RestoreWithoutAutomatic("0001")
	if err == nil || !strings.Contains(err.Error(), "a.txt") {
		t.Errorf("RestoreWithoutAutomatic = %v, want an error that names a.txt", err)
	}
}

func TestStepOrderingWithGaps(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "a.txt", "a\n", 0o644)
	for _, identifier := range []string{"0001", "0005", "0009", "0012"} {
		writeSnapshotByHand(t, store, identifier)
	}

	if err := store.SetHead("0009"); err != nil {
		t.Fatalf("SetHead: %v", err)
	}
	if target, err := store.Step(-1); err != nil || target != "0005" {
		t.Errorf("Step(-1) from 0009 = %q, %v, want 0005", target, err)
	}
	if target, err := store.Step(+1); err != nil || target != "0012" {
		t.Errorf("Step(+1) from 0009 = %q, %v, want 0012", target, err)
	}

	if err := store.SetHead("0001"); err != nil {
		t.Fatalf("SetHead: %v", err)
	}
	_, err := store.Step(-1)
	if err == nil || !strings.Contains(err.Error(),
		"checkpoint 0001 is the earliest one") {
		t.Errorf("Step(-1) from 0001 = %v, want an error", err)
	}

	if err := store.SetHead("0012"); err != nil {
		t.Fatalf("SetHead: %v", err)
	}
	_, err = store.Step(+1)
	if err == nil || !strings.Contains(err.Error(),
		"checkpoint 0012 is the latest one") {
		t.Errorf("Step(+1) from 0012 = %v, want an error", err)
	}

	if err := store.SetHead("0007"); err != nil {
		t.Fatalf("SetHead: %v", err)
	}
	if target, err := store.Step(-1); err != nil || target != "0009" {
		t.Errorf("Step(-1) with a stale HEAD = %q, %v, want 0009", target, err)
	}

	empty := newTestStore(t)
	if _, err := empty.Step(-1); err == nil ||
		!strings.Contains(err.Error(), "no checkpoints for this project") {
		t.Errorf("Step on an empty store = %v, want an error", err)
	}
}

func TestStepThenRestoreMovesHead(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "a.txt", "one\n", 0o644)
	if _, err := store.Take(""); err != nil {
		t.Fatalf("Take: %v", err)
	}
	writeProjectFile(t, store, "a.txt", "two\n", 0o644)
	if _, err := store.Take(""); err != nil {
		t.Fatalf("Take: %v", err)
	}

	target, err := store.Step(-1)
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if _, err := store.RestoreWithoutAutomatic(target); err != nil {
		t.Fatalf("RestoreWithoutAutomatic(%s): %v", target, err)
	}
	if head, ok := store.Head(); !ok || head != "0001" {
		t.Errorf("Head = %q, %v, want 0001", head, ok)
	}
	if _, err := store.Manifest("0003"); err == nil {
		t.Error("Manifest(0003) = nil error, want no checkpoint")
	}
}

func TestWriteEntry(t *testing.T) {
	project := t.TempDir()
	if err := WriteEntry(project,
		Entry{Kind: KindDirectory, Path: "nested/inner"}, nil); err != nil {
		t.Fatalf("WriteEntry: %v", err)
	}
	if information, err := os.Stat(
		filepath.Join(project, "nested/inner")); err != nil ||
		!information.IsDir() {
		t.Errorf("nested/inner = not a directory, %v, want a directory", err)
	}

	entry := Entry{Kind: KindFile, Mode: 0o600, Path: "nested/secret.txt"}
	if err := WriteEntry(project, entry, []byte("hush\n")); err != nil {
		t.Fatalf("WriteEntry: %v", err)
	}
	information, err := os.Lstat(filepath.Join(project, "nested/secret.txt"))
	if err != nil || information.Mode().Perm() != 0o600 {
		t.Errorf("secret.txt mode = %v, %v, want 0600", information.Mode(), err)
	}
	if _, err := os.Lstat(filepath.Join(project,
		"nested/secret.txt"+restoreTemporarySuffix)); !os.IsNotExist(err) {
		t.Error("temporary file exists, want removed")
	}

	if err := WriteEntry(project,
		Entry{Kind: KindFile, Path: "nested/secret.txt"},
		[]byte("again\n")); err != nil {
		t.Fatalf("WriteEntry: %v", err)
	}
	information, err = os.Lstat(filepath.Join(project, "nested/secret.txt"))
	if err != nil || information.Mode().Perm() != 0o644 {
		t.Errorf("secret.txt mode = %v, %v, want 0644",
			information.Mode(), err)
	}

	if err := WriteEntry(project,
		Entry{Kind: KindLink, Path: "nested/secret.txt"},
		[]byte("elsewhere.txt")); err != nil {
		t.Fatalf("WriteEntry: %v", err)
	}
	target, err := os.Readlink(filepath.Join(project, "nested/secret.txt"))
	if err != nil || target != "elsewhere.txt" {
		t.Errorf("Readlink = %q, %v, want elsewhere.txt", target, err)
	}
}
