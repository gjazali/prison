package plugin

import (
	"io/fs"
	"testing"
	"testing/fstest"
)

// pluginTree returns a small filesystem for the hash tests.
func pluginTree() fstest.MapFS {
	return fstest.MapFS{
		"inmate.toml":             &fstest.MapFile{Data: []byte(minimalManifest)},
		"Dockerfile":              &fstest.MapFile{Data: []byte("FROM base\n")},
		"scripts/setup":           &fstest.MapFile{Data: []byte("#!/bin/sh\n"), Mode: 0o755},
		".git/config":             &fstest.MapFile{Data: []byte("[core]\n")},
		".DS_Store":               &fstest.MapFile{Data: []byte("junk")},
		"__pycache__/x.pyc":       &fstest.MapFile{Data: []byte("bytes")},
		"node_modules/left-pad/x": &fstest.MapFile{Data: []byte("pad")},
	}
}

// hashOf returns the tree hash of fsys. It fails the test on error.
func hashOf(t *testing.T, fsys fs.FS) string {
	t.Helper()
	hash, err := TreeHash(fsys)
	if err != nil {
		t.Fatalf("TreeHash: %v", err)
	}
	return hash
}

// TestTreeHashIsStable checks that identical trees hash the same
// way and that ignored names have no effect.
func TestTreeHashIsStable(t *testing.T) {
	tree := pluginTree()
	first := hashOf(t, tree)
	if second := hashOf(t, pluginTree()); first != second {
		t.Errorf("two hashes of the same tree differ: %s and %s", first, second)
	}
	bare := fstest.MapFS{
		"inmate.toml":   pluginTree()["inmate.toml"],
		"Dockerfile":    pluginTree()["Dockerfile"],
		"scripts/setup": pluginTree()["scripts/setup"],
	}
	if hashOf(t, bare) != first {
		t.Error("an ignored name changed the hash")
	}
}

// TestTreeHashNoticesChanges checks that contents, names, the
// execute bit, and symlink targets each change the hash.
func TestTreeHashNoticesChanges(t *testing.T) {
	original := hashOf(t, pluginTree())

	changed := pluginTree()
	changed["Dockerfile"] = &fstest.MapFile{Data: []byte("FROM other\n")}
	if hashOf(t, changed) == original {
		t.Error("changed contents did not change the hash")
	}

	renamed := pluginTree()
	renamed["Containerfile"] = renamed["Dockerfile"]
	delete(renamed, "Dockerfile")
	if hashOf(t, renamed) == original {
		t.Error("a renamed file did not change the hash")
	}

	unmarked := pluginTree()
	unmarked["scripts/setup"] = &fstest.MapFile{Data: []byte("#!/bin/sh\n")}
	if hashOf(t, unmarked) == original {
		t.Error("dropping the execute bit did not change the hash")
	}

	linked := pluginTree()
	linked["link"] = &fstest.MapFile{Data: []byte("Dockerfile"), Mode: fs.ModeSymlink}
	withLink := hashOf(t, linked)
	if withLink == original {
		t.Error("adding a symlink did not change the hash")
	}
	linked["link"] = &fstest.MapFile{Data: []byte("inmate.toml"), Mode: fs.ModeSymlink}
	if hashOf(t, linked) == withLink {
		t.Error("a symlink pointing elsewhere did not change the hash")
	}
}

// TestTreeFilesListsWhatIsHashed checks that the file list matches
// the hashed entries in order.
func TestTreeFilesListsWhatIsHashed(t *testing.T) {
	files, err := TreeFiles(pluginTree())
	if err != nil {
		t.Fatalf("TreeFiles: %v", err)
	}
	want := []string{"Dockerfile", "inmate.toml", "scripts/setup"}
	if len(files) != len(want) {
		t.Fatalf("files = %v, want %v", files, want)
	}
	for index, name := range want {
		if files[index] != name {
			t.Errorf("files = %v, want %v", files, want)
			break
		}
	}
}
