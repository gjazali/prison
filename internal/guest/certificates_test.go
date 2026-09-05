package guest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// selfSignedPEM generates a self-signed certificate in PEM form.
func selfSignedPEM(t *testing.T, name string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template,
		&key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// TestWriteCertificateFiles checks per-certificate file writing,
// change detection, and removal of stale files.
func TestWriteCertificateFiles(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "ca-certificates")
	first := selfSignedPEM(t, "one")
	second := selfSignedPEM(t, "two")
	bundle := append(append([]byte("# comment\n"), first...), second...)
	if got := len(splitCertificateBundle(bundle)); got != 2 {
		t.Fatalf("split found %d certificates", got)
	}
	changed, removed, present, err := writeCertificateFiles(directory, bundle)
	if err != nil || !changed || len(removed) != 0 ||
		!reflect.DeepEqual(present, []string{"prison-route-01",
			"prison-route-02"}) {
		t.Fatalf("first install: %v %v %v %v", changed, removed, present, err)
	}
	content, _ := os.ReadFile(filepath.Join(directory, "prison-route-02.crt"))
	if string(content) != string(second) {
		t.Fatal("the second file does not hold the second certificate")
	}
	changed, _, _, err = writeCertificateFiles(directory, bundle)
	if err != nil || changed {
		t.Fatalf("identical bundle reported changed=%v, %v", changed, err)
	}
	changed, removed, present, err = writeCertificateFiles(directory, second)
	if err != nil || !changed ||
		!reflect.DeepEqual(removed, []string{"prison-route-01",
			"prison-route-02"}) ||
		!reflect.DeepEqual(present, []string{"prison-route-01"}) {
		t.Fatalf("shrunk bundle: %v %v %v %v", changed, removed, present, err)
	}
	files, _ := installedCertificateFiles(directory)
	if len(files) != 1 {
		t.Fatalf("files after shrink: %v", files)
	}
	changed, _, present, err = writeCertificateFiles(directory, nil)
	if err != nil || !changed || len(present) != 0 {
		t.Fatalf("empty bundle: %v %v %v", changed, present, err)
	}
	files, _ = installedCertificateFiles(directory)
	if len(files) != 0 {
		t.Fatalf("files after empty bundle: %v", files)
	}
	changed, _, _, err = writeCertificateFiles(directory, nil)
	if err != nil || changed {
		t.Fatalf("empty over empty reported changed=%v, %v", changed, err)
	}
}
