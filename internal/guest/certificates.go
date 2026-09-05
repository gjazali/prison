package guest

import (
	"bytes"
	"encoding/pem"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Paths and names for route authority certificates.
const (
	certificateDirectory = "/usr/local/share/ca-certificates"
	certificatePrefix    = "prison-route-"
	javaStorePassword    = "changeit"
)

// certificateInstaller updates the box's trust stores from the
// broker's PEM bundle. It rebuilds only when the bundle changes.
type certificateInstaller struct {
	directory string
	runner    *commandRunner
	logger    *log.Logger
}

// splitCertificateBundle splits a PEM bundle into individual
// certificate blocks. It takes the raw bundle bytes and returns a
// slice of PEM-encoded certificates.
func splitCertificateBundle(bundle []byte) [][]byte {
	var certificates [][]byte
	rest := bundle
	for {
		block, remaining := pem.Decode(rest)
		if block == nil {
			return certificates
		}
		rest = remaining
		if block.Type != "CERTIFICATE" {
			continue
		}
		certificates = append(certificates, pem.EncodeToMemory(block))
	}
}

// certificateFileName returns the filename for the certificate at the
// given index (one-based).
func certificateFileName(index int) string {
	return fmt.Sprintf("%s%02d.crt", certificatePrefix, index)
}

// installedCertificateFiles returns the sorted paths of prison
// certificate files in directory. A missing directory returns none.
func installedCertificateFiles(directory string) ([]string, error) {
	entries, err := filepath.Glob(filepath.Join(directory,
		certificatePrefix+"*.crt"))
	if err != nil {
		return nil, err
	}
	sort.Strings(entries)
	return entries, nil
}

// writeCertificateFiles syncs the certificate files in directory with
// bundle. It returns whether anything changed, the base names of
// removed files, and the base names of files now present. Files are
// left alone when they already match.
func writeCertificateFiles(directory string, bundle []byte) (changed bool,
	removed, present []string, err error) {
	desired := splitCertificateBundle(bundle)
	existing, err := installedCertificateFiles(directory)
	if err != nil {
		return false, nil, nil, err
	}
	same := len(existing) == len(desired)
	for index := 0; same && index < len(existing); index++ {
		content, readErr := os.ReadFile(existing[index])
		wantedName := filepath.Join(directory, certificateFileName(index+1))
		same = readErr == nil && existing[index] == wantedName &&
			bytes.Equal(content, desired[index])
	}
	for index := range desired {
		present = append(present, strings.TrimSuffix(
			certificateFileName(index+1), ".crt"))
	}
	if same {
		return false, nil, present, nil
	}
	for _, path := range existing {
		removed = append(removed, strings.TrimSuffix(filepath.Base(path),
			".crt"))
		if err := os.Remove(path); err != nil {
			return false, nil, nil, err
		}
	}
	if len(desired) > 0 {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return false, nil, nil, err
		}
	}
	for index, certificate := range desired {
		path := filepath.Join(directory, certificateFileName(index+1))
		if err := os.WriteFile(path, certificate, 0o644); err != nil {
			return false, nil, nil, err
		}
	}
	return true, removed, present, nil
}

// install writes bundle to the trust store and rebuilds system
// certificates if anything changed. It returns whether a rebuild
// happened and any error.
func (c *certificateInstaller) install(bundle []byte) (bool, error) {
	changed, removed, present, err := writeCertificateFiles(c.directory,
		bundle)
	if err != nil {
		return false, fmt.Errorf("writing route authorities: %w", err)
	}
	if !changed {
		return false, nil
	}
	if _, err := c.runner.run("update-ca-certificates", "--fresh"); err != nil {
		return true, fmt.Errorf("rebuilding the trust store: %w", err)
	}
	c.refreshJavaKeystore(removed, present)
	return true, nil
}

// findJavaKeystore returns the path to the JVM trust store, or ""
// if keytool is absent or no cacerts file exists.
func findJavaKeystore() string {
	if _, err := exec.LookPath("keytool"); err != nil {
		return ""
	}
	candidates := []string{"/etc/ssl/certs/java/cacerts"}
	if javaHome := os.Getenv("JAVA_HOME"); javaHome != "" {
		candidates = append(candidates,
			filepath.Join(javaHome, "lib", "security", "cacerts"))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// refreshJavaKeystore updates the JVM trust store by removing old
// aliases and importing current ones. Failures are logged and
// ignored.
func (c *certificateInstaller) refreshJavaKeystore(removed, present []string) {
	keystore := findJavaKeystore()
	if keystore == "" {
		return
	}
	for _, alias := range append(append([]string{}, removed...), present...) {
		_, _ = c.runner.run("keytool", "-delete", "-alias", alias,
			"-keystore", keystore, "-storepass", javaStorePassword)
	}
	for _, alias := range present {
		path := filepath.Join(c.directory, alias+".crt")
		if _, err := c.runner.run("keytool", "-importcert", "-noprompt",
			"-trustcacerts", "-alias", alias, "-file", path,
			"-keystore", keystore, "-storepass", javaStorePassword); err != nil {
			c.logger.Printf("certificates: keytool import of %s: %v", alias,
				err)
		}
	}
}
