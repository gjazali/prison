package firecracker

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const kernelVersionFile = "version"

func kernelArchiveName() string {
	return "linux-" + runtime.GOARCH + ".gz"
}

// kernelPath unpacks the embedded kernel once for each version and content.
func (driver *Driver) kernelPath() (string, error) {
	if err := driver.requireStateDirectory(); err != nil {
		return "", err
	}
	compressed, err := driver.readKernelFile(kernelArchiveName())
	if errors.Is(err, fs.ErrNotExist) {
		return "", errors.New("this build has no guest kernel. " +
			"Run `make kernel`, then build prison again")
	}
	if err != nil {
		return "", fmt.Errorf("cannot read the embedded kernel: %w", err)
	}
	version, err := driver.readKernelFile(kernelVersionFile)
	if err != nil {
		return "", fmt.Errorf("cannot read the kernel version: %w", err)
	}
	digest := sha256.Sum256(compressed)
	directory := filepath.Join(driver.stateDirectory, "kernel",
		strings.TrimSpace(string(version))+"-"+
			hex.EncodeToString(digest[:6]))
	target := filepath.Join(directory, "linux")
	if _, err := os.Stat(target); err == nil {
		return target, nil
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("cannot create %s: %w", directory, err)
	}
	if err := unpackKernel(compressed, target); err != nil {
		return "", err
	}
	return target, nil
}

func (driver *Driver) readKernelFile(name string) ([]byte, error) {
	if driver.kernel == nil {
		return nil, fs.ErrNotExist
	}
	return fs.ReadFile(driver.kernel, name)
}

func unpackKernel(compressed []byte, target string) error {
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return fmt.Errorf("the embedded kernel is not valid: %w", err)
	}
	partial := target + partialSuffix
	writer, err := os.Create(partial)
	if err != nil {
		return fmt.Errorf("cannot write %s: %w", partial, err)
	}
	if _, err := io.Copy(writer, reader); err != nil {
		writer.Close()
		os.Remove(partial)
		return fmt.Errorf("cannot unpack the kernel: %w", err)
	}
	if err := writer.Close(); err != nil {
		os.Remove(partial)
		return fmt.Errorf("cannot write %s: %w", partial, err)
	}
	return os.Rename(partial, target)
}
