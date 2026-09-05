package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// codexManifest is a second inmate used alongside the bundled
// claude.
const codexManifest = `
[inmate]
name = "codex"
description = "a second inmate"
[command]
run = "codex"
[persist]
paths = ["/home/dev/.codex"]
`

// bundledFS returns a filesystem with one directory per bundled
// inmate.
func bundledFS() fstest.MapFS {
	return fstest.MapFS{
		"claude/inmate.toml": &fstest.MapFile{Data: []byte(claudeManifest)},
		"claude/Dockerfile":  &fstest.MapFile{Data: []byte("FROM base\n")},
		"notes.md":           &fstest.MapFile{Data: []byte("not an inmate\n")},
	}
}

// installInmate writes an inmate directory under installDir and
// returns its path.
func installInmate(t *testing.T, installDir, name, manifest string) string {
	t.Helper()
	directory := filepath.Join(installDir, name)
	write(t, filepath.Join(directory, ManifestFileName), manifest)
	write(t, filepath.Join(directory, "Dockerfile"), "FROM base\n")
	return directory
}

// testRegistry returns a registry with the bundled claude and an
// empty install root in a temporary directory.
func testRegistry(t *testing.T) (*Registry, string, string) {
	t.Helper()
	root := t.TempDir()
	installDir := filepath.Join(root, "inmates")
	trustDir := filepath.Join(root, "trust")
	return NewRegistry(bundledFS(), installDir, trustDir), installDir, trustDir
}

// approveInstalled records the current hash of an installed inmate.
func approveInstalled(t *testing.T, registry *Registry, name string) {
	t.Helper()
	inmate, err := registry.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := TreeHash(inmate.FS)
	if err != nil {
		t.Fatal(err)
	}
	err = registry.writeTrustRecord(name, &TrustRecord{
		Hash:      hash,
		OriginURL: "https://example.com/codex.git",
		OriginRef: "v1",
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestRegistryListsBundledAndInstalled checks both sources, sort
// order, and that bundled inmates need no approval.
func TestRegistryListsBundledAndInstalled(t *testing.T) {
	registry, installDir, _ := testRegistry(t)
	installInmate(t, installDir, "codex", codexManifest)

	inmates, err := registry.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(inmates) != 2 {
		t.Fatalf("List returned %d inmates, want 2", len(inmates))
	}
	if inmates[0].Name != "claude" || !inmates[0].Bundled || !inmates[0].Trusted {
		t.Errorf("first = %+v, want the bundled claude", inmates[0])
	}
	if inmates[0].Command.Run != "claude" {
		t.Errorf("the bundled manifest was not read: %+v", inmates[0].Manifest)
	}
	if inmates[1].Name != "codex" || inmates[1].Bundled || inmates[1].Trusted {
		t.Errorf("second = %+v, want an unapproved installed codex", inmates[1])
	}
	if inmates[1].Dir != filepath.Join(installDir, "codex") {
		t.Errorf("dir = %q, want the install directory", inmates[1].Dir)
	}
}

// TestRegistryTrustFollowsContents checks that trust holds until a
// file changes and that Load names the approval command.
func TestRegistryTrustFollowsContents(t *testing.T) {
	registry, installDir, _ := testRegistry(t)
	directory := installInmate(t, installDir, "codex", codexManifest)
	approveInstalled(t, registry, "codex")

	inmate, err := registry.Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	if !inmate.Trusted {
		t.Fatal("codex is not trusted after being approved")
	}
	if url, ref := inmate.Origin(); url == "" || ref != "v1" {
		t.Errorf("origin = %q at %q, want what was recorded", url, ref)
	}
	if _, err := registry.Load([]string{"codex", "codex"}); err != nil {
		t.Errorf("Load: %v", err)
	}

	write(t, filepath.Join(directory, "Dockerfile"), "FROM base\nRUN echo new\n")
	if _, err := registry.Load([]string{"codex"}); err == nil {
		t.Fatal("a changed inmate was loaded")
	} else if !strings.Contains(err.Error(), "prison inmate trust codex") {
		t.Errorf("Load: %v, want the message naming how to approve it", err)
	}
}

// TestRegistryLoadOrdersAndDeduplicates checks that Load keeps the
// requested order, drops duplicates, and refuses unknown names.
func TestRegistryLoadOrdersAndDeduplicates(t *testing.T) {
	registry, installDir, _ := testRegistry(t)
	installInmate(t, installDir, "codex", codexManifest)
	approveInstalled(t, registry, "codex")

	loaded, err := registry.Load([]string{"codex", "claude", "codex"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 2 || loaded[0].Name != "codex" || loaded[1].Name != "claude" {
		t.Errorf("Load returned %v", names(loaded))
	}
	_, err = registry.Load([]string{"nothing"})
	if err == nil || !strings.Contains(err.Error(), "claude, codex") {
		t.Errorf("Load of an unknown inmate: %v, want the available names", err)
	}
}

// names returns the names of a list of inmates.
func names(inmates []*Inmate) []string {
	listed := make([]string, 0, len(inmates))
	for _, inmate := range inmates {
		listed = append(listed, inmate.Name)
	}
	return listed
}

// TestRegistryShadowsInstalledCopy checks that a bundled inmate wins
// a name collision and the installed copy carries a problem.
func TestRegistryShadowsInstalledCopy(t *testing.T) {
	registry, installDir, _ := testRegistry(t)
	installInmate(t, installDir, "claude", claudeManifest)

	inmates, err := registry.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(inmates) != 2 {
		t.Fatalf("List returned %d inmates, want both copies", len(inmates))
	}
	if !inmates[0].Bundled || inmates[0].Problem != nil {
		t.Errorf("the bundled copy is not first and clean: %+v", inmates[0])
	}
	if inmates[1].Problem == nil || !strings.Contains(inmates[1].Problem.Error(), "ships an inmate") {
		t.Errorf("the shadowed copy has no problem set: %+v", inmates[1])
	}
	found, err := registry.Get("claude")
	if err != nil {
		t.Fatal(err)
	}
	if !found.Bundled {
		t.Error("Get returned the shadowed copy")
	}
}

// TestRegistryReportsBrokenManifests checks that unusable inmates
// are listed with their problem instead of stopping the listing.
func TestRegistryReportsBrokenManifests(t *testing.T) {
	registry, installDir, _ := testRegistry(t)
	installInmate(t, installDir, "broken", "[inmate]\nname = \"broken\"\n")
	installInmate(t, installDir, "misnamed", codexManifest)
	if err := os.MkdirAll(filepath.Join(installDir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	inmates, err := registry.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(inmates) != 3 {
		t.Fatalf("List returned %v, want the two broken inmates and claude", names(inmates))
	}
	broken, err := registry.Get("broken")
	if err != nil {
		t.Fatal(err)
	}
	if broken.Problem == nil || !strings.Contains(broken.Problem.Error(), "command.run") {
		t.Errorf("broken.Problem = %v, want the validation failure", broken.Problem)
	}
	misnamed, err := registry.Get("misnamed")
	if err != nil {
		t.Fatal(err)
	}
	if misnamed.Problem == nil || !strings.Contains(misnamed.Problem.Error(), "directory") {
		t.Errorf("misnamed.Problem = %v, want the name mismatch", misnamed.Problem)
	}
	if _, err := registry.Load([]string{"broken"}); err == nil {
		t.Error("a broken inmate was loaded")
	}
}
