package cli

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
	"testing"
	"time"
)

func TestBrokerUpstreamTLSConfig(t *testing.T) {
	t.Run("Unset", func(t *testing.T) {
		t.Setenv("SSL_CERT_FILE", "")
		cfg, err := brokerUpstreamTLSConfig()
		if err != nil || cfg != nil {
			t.Fatalf("brokerUpstreamTLSConfig() = %v, %v; want nil, nil", cfg, err)
		}
	})

	t.Run("ValidCertFile", func(t *testing.T) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		tmpl := &x509.Certificate{
			SerialNumber:          big.NewInt(1),
			Subject:               pkix.Name{CommonName: "test-ca"},
			NotBefore:             time.Now().Add(-time.Hour),
			NotAfter:              time.Now().Add(time.Hour),
			IsCA:                  true,
			BasicConstraintsValid: true,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		certPath := filepath.Join(t.TempDir(), "ca.pem")
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		if err := os.WriteFile(certPath, pemBytes, 0o644); err != nil {
			t.Fatal(err)
		}

		t.Setenv("SSL_CERT_FILE", certPath)
		cfg, err := brokerUpstreamTLSConfig()
		if err != nil || cfg == nil || cfg.RootCAs == nil {
			t.Fatalf("brokerUpstreamTLSConfig() = %v, %v; want non-nil RootCAs", cfg, err)
		}
	})

	t.Run("InvalidCertFile", func(t *testing.T) {
		badPath := filepath.Join(t.TempDir(), "bad.pem")
		if err := os.WriteFile(badPath, []byte("not a cert"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SSL_CERT_FILE", badPath)
		if _, err := brokerUpstreamTLSConfig(); err == nil {
			t.Fatal("brokerUpstreamTLSConfig() = nil error, want error for invalid PEM")
		}
	})
}
