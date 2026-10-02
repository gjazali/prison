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

const (
	certificateDirectory = "/usr/local/share/ca-certificates"
	certificatePrefix    = "prison-route-"
	javaStorePassword    = "changeit"
)

type certificateInstaller struct {
	directory string
	runner    *commandRunner
	logger    *log.Logger
}

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

func certificateFileName(index int) string {
	return fmt.Sprintf("%s%02d.crt", certificatePrefix, index)
}

func installedCertificateFiles(directory string) ([]string, error) {
	entries, err := filepath.Glob(filepath.Join(directory,
		certificatePrefix+"*.crt"))
	if err != nil {
		return nil, err
	}
	sort.Strings(entries)
	return entries, nil
}

// writeCertificateFiles makes the files in `directory` match `bundle`.
// The `removed` and `present` names are also the Java keystore aliases.
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

func (c *certificateInstaller) install(bundle []byte) (bool, error) {
	changed, removed, present, err := writeCertificateFiles(c.directory,
		bundle)
	if err != nil {
		return false, fmt.Errorf("cannot write the route authorities: %w", err)
	}
	if !changed {
		return false, nil
	}
	if _, err := c.runner.run("update-ca-certificates", "--fresh"); err != nil {
		return true, fmt.Errorf("cannot rebuild the trust store: %w", err)
	}
	c.refreshJavaKeystore(removed, present)
	return true, nil
}

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
			c.logger.Printf("certificates: cannot import %s: %v", alias, err)
		}
	}
}
