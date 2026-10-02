package prison

import (
	"io/fs"
	"path"
	"strings"
	"testing"

	"prison/internal/plugin"
)

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
			t.Errorf("%s: Inmate.Name = %q, want %q",
				manifestPath, manifest.Inmate.Name, entry.Name())
		}
	}
	if found == 0 {
		t.Fatalf("%s has no inmate directories", BundledInmatesDir)
	}
}

func TestBaseImageContextCarriesDockerfile(t *testing.T) {
	dockerfilePath := path.Join(BaseImageDir, "Dockerfile")
	data, err := fs.ReadFile(Assets, dockerfilePath)
	if err != nil {
		t.Fatalf("read %s: %v", dockerfilePath, err)
	}
	entrypoint := `ENTRYPOINT ["/usr/local/bin/` + GuestBinaryName + `", "init"]`
	if !strings.Contains(string(data), entrypoint) {
		t.Errorf("%s does not contain %s", dockerfilePath, entrypoint)
	}
}
