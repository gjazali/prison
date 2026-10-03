package firecracker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type imageConfig struct {
	Environment []string `json:"Env"`
}

// readImageConfig reads `Env` from the OCI layout because the disk image
// keeps only the files.
func readImageConfig(layout string) (imageConfig, error) {
	digest, err := layoutDigest(layout)
	if err != nil {
		return imageConfig{}, err
	}
	var manifest struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := readBlob(layout, digest, &manifest); err != nil {
		return imageConfig{}, err
	}
	var document struct {
		Config imageConfig `json:"config"`
	}
	if err := readBlob(layout, manifest.Config.Digest, &document); err != nil {
		return imageConfig{}, err
	}
	return document.Config, nil
}

func readBlob(layout, digest string, target any) error {
	algorithm, hexadecimal, found := strings.Cut(digest, ":")
	if !found {
		return fmt.Errorf("digest %q is not valid", digest)
	}
	path := filepath.Join(layout, "blobs", algorithm, hexadecimal)
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot read the image blob %s: %w", digest, err)
	}
	if err := json.Unmarshal(content, target); err != nil {
		return fmt.Errorf("the image blob %s is not valid: %w", digest, err)
	}
	return nil
}
