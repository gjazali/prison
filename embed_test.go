package prison

import (
	"io/fs"
	"path"
	"strings"
	"testing"

	"prison/internal/plugin"
)

// TestBundledInmateManifestsParse checks that every bundled inmate
// has a parseable manifest with a matching name.
func TestBundledInmateManifestsParse(t *testing.T) {
	entries, err := fs.ReadDir(Assets, BundledInmatesDir)
	if err != nil {
		t.Fatalf("read %s: %v", BundledInmatesDir, err)
	}
	found := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		found++
		manifestPath := path.Join(
			BundledInmatesDir, entry.Name(), plugin.ManifestFileName)
		data, err := fs.ReadFile(Assets, manifestPath)
		if err != nil {
			t.Errorf("read %s: %v", manifestPath, err)
			continue
		}
		manifest, err := plugin.ParseManifest(data)
		if err != nil {
			t.Errorf("%s: %v", manifestPath, err)
			continue
		}
		if manifest.Inmate.Name != entry.Name() {
			t.Errorf("%s: names the inmate %q but sits in %q",
				manifestPath, manifest.Inmate.Name, entry.Name())
		}
	}
	if found == 0 {
		t.Fatalf("%s holds no inmate directories", BundledInmatesDir)
	}
}

// TestBaseImageContextCarriesDockerfile checks that the base image
// Dockerfile uses the guest binary as its entrypoint.
func TestBaseImageContextCarriesDockerfile(t *testing.T) {
	dockerfilePath := path.Join(BaseImageDir, "Dockerfile")
	data, err := fs.ReadFile(Assets, dockerfilePath)
	if err != nil {
		t.Fatalf("read %s: %v", dockerfilePath, err)
	}
	entrypoint := `ENTRYPOINT ["/usr/local/bin/` + GuestBinaryName + `", "init"]`
	if !strings.Contains(string(data), entrypoint) {
		t.Errorf("%s does not carry %s", dockerfilePath, entrypoint)
	}
}
