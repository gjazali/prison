package plugin

import (
	"io/fs"
	"testing"
	"testing/fstest"
)

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

func hashOf(t *testing.T, fsys fs.FS) string {
	t.Helper()
	hash, err := TreeHash(fsys)
	if err != nil {
		t.Fatalf("TreeHash: %v", err)
	}
	return hash
}

func TestTreeHashIsStable(t *testing.T) {
	tree := pluginTree()
	first := hashOf(t, tree)
	if second := hashOf(t, pluginTree()); first != second {
		t.Errorf("TreeHash = %s, want %s", second, first)
	}
	bare := fstest.MapFS{
		"inmate.toml":   pluginTree()["inmate.toml"],
		"Dockerfile":    pluginTree()["Dockerfile"],
		"scripts/setup": pluginTree()["scripts/setup"],
	}
	if hashOf(t, bare) != first {
		t.Errorf("TreeHash without ignored names = %s, want %s",
			hashOf(t, bare), first)
	}
}

func TestTreeHashNoticesChanges(t *testing.T) {
	original := hashOf(t, pluginTree())

	changed := pluginTree()
	changed["Dockerfile"] = &fstest.MapFile{Data: []byte("FROM other\n")}
	if hashOf(t, changed) == original {
		t.Error("TreeHash after a content change = original hash")
	}

	renamed := pluginTree()
	renamed["Containerfile"] = renamed["Dockerfile"]
	delete(renamed, "Dockerfile")
	if hashOf(t, renamed) == original {
		t.Error("TreeHash after a rename = original hash")
	}

	unmarked := pluginTree()
	unmarked["scripts/setup"] = &fstest.MapFile{Data: []byte("#!/bin/sh\n")}
	if hashOf(t, unmarked) == original {
		t.Error("TreeHash without the execute bit = original hash")
	}

	linked := pluginTree()
	linked["link"] = &fstest.MapFile{Data: []byte("Dockerfile"), Mode: fs.ModeSymlink}
	withLink := hashOf(t, linked)
	if withLink == original {
		t.Error("TreeHash with a symlink = original hash")
	}
	linked["link"] = &fstest.MapFile{Data: []byte("inmate.toml"), Mode: fs.ModeSymlink}
	if hashOf(t, linked) == withLink {
		t.Error("TreeHash with a new symlink target = old hash")
	}
}

func TestTreeFilesListsWhatIsHashed(t *testing.T) {
	files, err := TreeFiles(pluginTree())
	if err != nil {
		t.Fatalf("TreeFiles: %v", err)
	}
	want := []string{"Dockerfile", "inmate.toml", "scripts/setup"}
	if len(files) != len(want) {
		t.Fatalf("TreeFiles = %v, want %v", files, want)
	}
	for index, name := range want {
		if files[index] != name {
			t.Errorf("TreeFiles = %v, want %v", files, want)
			break
		}
	}
}
