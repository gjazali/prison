package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

const codexManifest = `
[inmate]
name = "codex"
description = "a second inmate"
aliases = ["cx"]
[command]
run = "codex"
[persist]
paths = ["/home/dev/.codex"]
`

func bundledFS() fstest.MapFS {
	return fstest.MapFS{
		"claude/inmate.toml": &fstest.MapFile{Data: []byte(claudeManifest)},
		"claude/Dockerfile":  &fstest.MapFile{Data: []byte("FROM base\n")},
		"notes.md":           &fstest.MapFile{Data: []byte("not an inmate\n")},
	}
}

func installInmate(t *testing.T, installDir, name, manifest string) string {
	t.Helper()
	directory := filepath.Join(installDir, name)
	write(t, filepath.Join(directory, ManifestFileName), manifest)
	write(t, filepath.Join(directory, "Dockerfile"), "FROM base\n")
	return directory
}

func testRegistry(t *testing.T) (*Registry, string, string) {
	t.Helper()
	root := t.TempDir()
	installDir := filepath.Join(root, "inmates")
	trustDir := filepath.Join(root, "trust")
	return NewRegistry(bundledFS(), installDir, trustDir), installDir, trustDir
}

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

func TestRegistryListsBundledAndInstalled(t *testing.T) {
	registry, installDir, _ := testRegistry(t)
	installInmate(t, installDir, "codex", codexManifest)

	inmates, err := registry.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(inmates) != 2 {
		t.Fatalf("len(List) = %d, want 2", len(inmates))
	}
	if inmates[0].Name != "claude" || !inmates[0].Bundled || !inmates[0].Trusted {
		t.Errorf("List[0] = %+v, want the bundled claude", inmates[0])
	}
	if inmates[0].Command.Run != "claude" {
		t.Errorf("command.run = %q, want claude", inmates[0].Command.Run)
	}
	if inmates[1].Name != "codex" || inmates[1].Bundled || inmates[1].Trusted {
		t.Errorf("List[1] = %+v, want an unapproved installed codex",
			inmates[1])
	}
	if inmates[1].Dir != filepath.Join(installDir, "codex") {
		t.Errorf("Dir = %q, want %q", inmates[1].Dir,
			filepath.Join(installDir, "codex"))
	}
}

func TestRegistryTrustFollowsContents(t *testing.T) {
	registry, installDir, _ := testRegistry(t)
	directory := installInmate(t, installDir, "codex", codexManifest)
	approveInstalled(t, registry, "codex")

	inmate, err := registry.Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	if !inmate.Trusted {
		t.Fatal("Trusted = false, want true")
	}
	if url, ref := inmate.Origin(); url == "" || ref != "v1" {
		t.Errorf("Origin = %q, %q, want a URL and v1", url, ref)
	}
	if _, err := registry.Load([]string{"codex", "codex"}); err != nil {
		t.Errorf("Load: %v", err)
	}

	write(t, filepath.Join(directory, "Dockerfile"), "FROM base\nRUN echo new\n")
	if _, err := registry.Load([]string{"codex"}); err == nil {
		t.Fatal("Load = nil, want an error")
	} else if !strings.Contains(err.Error(), "prison inmate trust codex") {
		t.Errorf("Load = %v, want \"prison inmate trust codex\"", err)
	}
}

func TestRegistryLoadOrdersAndDeduplicates(t *testing.T) {
	registry, installDir, _ := testRegistry(t)
	installInmate(t, installDir, "codex", codexManifest)
	approveInstalled(t, registry, "codex")

	loaded, err := registry.Load([]string{"codex", "claude", "codex"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 2 || loaded[0].Name != "codex" || loaded[1].Name != "claude" {
		t.Errorf("Load = %v, want [codex claude]", names(loaded))
	}
	_, err = registry.Load([]string{"nothing"})
	if err == nil || !strings.Contains(err.Error(), "claude, codex") {
		t.Errorf("Load = %v, want \"claude, codex\"", err)
	}
}

func names(inmates []*Inmate) []string {
	listed := make([]string, 0, len(inmates))
	for _, inmate := range inmates {
		listed = append(listed, inmate.Name)
	}
	return listed
}

func TestRegistryShadowsInstalledCopy(t *testing.T) {
	registry, installDir, _ := testRegistry(t)
	installInmate(t, installDir, "claude", claudeManifest)

	inmates, err := registry.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(inmates) != 2 {
		t.Fatalf("len(List) = %d, want 2", len(inmates))
	}
	if !inmates[0].Bundled || inmates[0].Problem != nil {
		t.Errorf("List[0] = %+v, want the bundled copy with no problem",
			inmates[0])
	}
	if inmates[1].Problem == nil || !strings.Contains(inmates[1].Problem.Error(), "bundled inmate") {
		t.Errorf("List[1].Problem = %v, want \"bundled inmate\"",
			inmates[1].Problem)
	}
	found, err := registry.Get("claude")
	if err != nil {
		t.Fatal(err)
	}
	if !found.Bundled {
		t.Error("Get(claude).Bundled = false, want true")
	}
}

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
		t.Fatalf("List = %v, want broken, claude, and misnamed",
			names(inmates))
	}
	broken, err := registry.Get("broken")
	if err != nil {
		t.Fatal(err)
	}
	if broken.Problem == nil || !strings.Contains(broken.Problem.Error(), "command.run") {
		t.Errorf("broken.Problem = %v, want \"command.run\"", broken.Problem)
	}
	misnamed, err := registry.Get("misnamed")
	if err != nil {
		t.Fatal(err)
	}
	if misnamed.Problem == nil || !strings.Contains(misnamed.Problem.Error(), "directory") {
		t.Errorf("misnamed.Problem = %v, want \"directory\"",
			misnamed.Problem)
	}
	if _, err := registry.Load([]string{"broken"}); err == nil {
		t.Error("Load(broken) = nil, want an error")
	}
}

func TestRegistryResolvesAliases(t *testing.T) {
	registry, installDir, _ := testRegistry(t)
	installInmate(t, installDir, "codex", codexManifest)
	approveInstalled(t, registry, "codex")

	inmate, err := registry.Get("cx")
	if err != nil {
		t.Fatalf("Get(cx): %v", err)
	}
	if inmate.Name != "codex" {
		t.Errorf("Get(\"cx\") = %q, want codex", inmate.Name)
	}
	loaded, err := registry.Load([]string{"cx", "codex"})
	if err != nil || len(loaded) != 1 {
		t.Errorf("Load(cx, codex) = %v, %v, want [codex]",
			names(loaded), err)
	}

	installInmate(t, installDir, "cursor",
		strings.ReplaceAll(codexManifest, "codex", "cursor"))
	_, err = registry.Get("cx")
	if err == nil || !strings.Contains(err.Error(), "codex and cursor") {
		t.Errorf("Get(cx) = %v, want \"codex and cursor\"", err)
	}

	installInmate(t, installDir, "cx", strings.Replace(
		strings.ReplaceAll(codexManifest, "codex", "cx"),
		"aliases = [\"cx\"]\n", "", 1))
	inmate, err = registry.Get("cx")
	if err != nil {
		t.Fatalf("Get(cx): %v", err)
	}
	if inmate.Name != "cx" || inmate.Problem != nil {
		t.Errorf("Get(\"cx\") = %q, %v, want cx, nil",
			inmate.Name, inmate.Problem)
	}
}
