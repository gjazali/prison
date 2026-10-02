// Package ca manages per-host certificate authorities for intercepting
// routes. Name constraints limit each authority to one DNS name.
package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"prison/internal/vault"
)

var ErrInvalidHost = errors.New("invalid host")

const (
	authorityLifetime = 10 * 365 * 24 * time.Hour
	leafLifetime      = 825 * 24 * time.Hour
	leafReuse         = time.Hour
	authoritySubject  = "prison certificate authority for %s"
	certificatePEM    = "CERTIFICATE"
	privateKeyPEM     = "PRIVATE KEY"
)

type Store interface {
	Authority(host string) (*vault.Authority, bool)
	SetAuthority(host string, authority *vault.Authority) error
}

type loadedAuthority struct {
	record      *vault.Authority
	certificate *x509.Certificate
	key         *ecdsa.PrivateKey
}

type cachedLeaf struct {
	certificate *tls.Certificate
	minted      time.Time
}

// Authorities is safe for concurrent use.
type Authorities struct {
	store   Store
	now     func() time.Time
	mutex   sync.Mutex
	loaded  map[string]*loadedAuthority
	leaves  map[string]*cachedLeaf
	leafKey *ecdsa.PrivateKey
}

func New(store Store, now func() time.Time) *Authorities {
	if now == nil {
		now = time.Now
	}
	return &Authorities{
		store:  store,
		now:    now,
		loaded: map[string]*loadedAuthority{},
		leaves: map[string]*cachedLeaf{},
	}
}

func (a *Authorities) Authority(host string) (*vault.Authority, error) {
	normalized, err := normalizeHost(host)
	if err != nil {
		return nil, err
	}
	a.mutex.Lock()
	defer a.mutex.Unlock()
	loaded, err := a.authorityLocked(normalized)
	if err != nil {
		return nil, err
	}
	return loaded.record, nil
}

func (a *Authorities) TLSConfig(host string) (*tls.Config, error) {
	normalized, err := normalizeHost(host)
	if err != nil {
		return nil, err
	}
	if _, err := a.Authority(normalized); err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return a.leaf(normalized)
		},
	}, nil
}

func (a *Authorities) Bundle(hosts []string) ([]byte, error) {
	seen := map[string]bool{}
	normalizedHosts := make([]string, 0, len(hosts))
	for _, host := range hosts {
		normalized, err := normalizeHost(host)
		if err != nil {
			return nil, err
		}
		if !seen[normalized] {
			seen[normalized] = true
			normalizedHosts = append(normalizedHosts, normalized)
		}
	}
	sort.Strings(normalizedHosts)

	a.mutex.Lock()
	defer a.mutex.Unlock()
	var bundle strings.Builder
	for _, host := range normalizedHosts {
		loaded, err := a.authorityLocked(host)
		if err != nil {
			return nil, err
		}
		block := &pem.Block{Type: certificatePEM, Bytes: loaded.certificate.Raw}
		if err := pem.Encode(&bundle, block); err != nil {
			return nil, fmt.Errorf("encoding authority for %s: %w", host, err)
		}
	}
	return []byte(bundle.String()), nil
}

// authorityLocked requires the mutex.
func (a *Authorities) authorityLocked(host string) (*loadedAuthority, error) {
	if loaded, ok := a.loaded[host]; ok {
		return loaded, nil
	}
	record, ok := a.store.Authority(host)
	if !ok {
		created, err := a.createAuthority(host)
		if err != nil {
			return nil, err
		}
		if err := a.store.SetAuthority(host, created); err != nil {
			return nil, fmt.Errorf("storing authority for %s: %w", host, err)
		}
		record = created
	}
	loaded, err := parseAuthority(record)
	if err != nil {
		return nil, fmt.Errorf("loading authority for %s: %w", host, err)
	}
	a.loaded[host] = loaded
	return loaded, nil
}

func (a *Authorities) createAuthority(host string) (*vault.Authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating authority key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	subjectKeyIdentifier, err := subjectKeyIdentifierFor(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	now := a.now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: fmt.Sprintf(authoritySubject, host),
		},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(authorityLifetime),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		SubjectKeyId:          subjectKeyIdentifier,

		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         []string{host},
		ExcludedIPRanges: []*net.IPNet{
			{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
			{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)},
		},
	}
	certificateDER, err := x509.CreateCertificate(
		rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("creating authority certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("encoding authority key: %w", err)
	}
	return &vault.Authority{
		KeyPEM: string(pem.EncodeToMemory(
			&pem.Block{Type: privateKeyPEM, Bytes: keyDER})),
		CertificatePEM: string(pem.EncodeToMemory(
			&pem.Block{Type: certificatePEM, Bytes: certificateDER})),
		Created: now,
	}, nil
}

func (a *Authorities) leaf(host string) (*tls.Certificate, error) {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	now := a.now()
	if cached, ok := a.leaves[host]; ok {
		age := now.Sub(cached.minted)
		if age >= 0 && age < leafReuse {
			return cached.certificate, nil
		}
	}
	authority, err := a.authorityLocked(host)
	if err != nil {
		return nil, err
	}
	if a.leafKey == nil {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generating leaf key: %w", err)
		}
		a.leafKey = key
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host},
		DNSNames:              []string{host},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(leafLifetime),
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(
		rand.Reader, template, authority.certificate, &a.leafKey.PublicKey,
		authority.key)
	if err != nil {
		return nil, fmt.Errorf("signing leaf for %s: %w", host, err)
	}
	parsed, err := x509.ParseCertificate(leafDER)
	if err != nil {
		return nil, fmt.Errorf("parsing leaf for %s: %w", host, err)
	}
	certificate := &tls.Certificate{
		Certificate: [][]byte{leafDER},
		PrivateKey:  a.leafKey,
		Leaf:        parsed,
	}
	a.leaves[host] = &cachedLeaf{certificate: certificate, minted: now}
	return certificate, nil
}

func normalizeHost(host string) (string, error) {
	normalized := strings.TrimSuffix(strings.ToLower(host), ".")
	switch {
	case normalized == "":
		return "", fmt.Errorf("%w: host is empty", ErrInvalidHost)
	case strings.Contains(normalized, "://"), strings.Contains(normalized, "/"):
		return "", fmt.Errorf(
			"%w: %q must not have a scheme or path",
			ErrInvalidHost, host)
	case strings.Contains(normalized, ":"):
		return "", fmt.Errorf(
			"%w: %q must not have a port",
			ErrInvalidHost, host)
	case strings.ContainsAny(normalized, " \t\r\n"):
		return "", fmt.Errorf(
			"%w: %q contains whitespace", ErrInvalidHost, host)
	}
	return normalized, nil
}

func parseAuthority(record *vault.Authority) (*loadedAuthority, error) {
	certificateBlock, _ := pem.Decode([]byte(record.CertificatePEM))
	if certificateBlock == nil {
		return nil, errors.New("certificate is not PEM")
	}
	certificate, err := x509.ParseCertificate(certificateBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing certificate: %w", err)
	}
	keyBlock, _ := pem.Decode([]byte(record.KeyPEM))
	if keyBlock == nil {
		return nil, errors.New("key is not PEM")
	}
	key, err := parseECDSAPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing key: %w", err)
	}
	if !key.PublicKey.Equal(certificate.PublicKey) {
		return nil, errors.New("key does not match certificate")
	}
	return &loadedAuthority{
		record:      record,
		certificate: certificate,
		key:         key,
	}, nil
}

func parseECDSAPrivateKey(der []byte) (*ecdsa.PrivateKey, error) {
	if parsed, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		key, ok := parsed.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("key type %T is not ECDSA", parsed)
		}
		return key, nil
	}
	key, err := x509.ParseECPrivateKey(der)
	if err != nil {
		return nil, err
	}
	return key, nil
}

func randomSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 63))
	if err != nil {
		return nil, fmt.Errorf("generating serial number: %w", err)
	}
	return serial, nil
}

// subjectKeyIdentifierFor uses RFC 5280 method 1.
func subjectKeyIdentifierFor(key *ecdsa.PublicKey) ([]byte, error) {
	ecdhKey, err := key.ECDH()
	if err != nil {
		return nil, fmt.Errorf("deriving subject key identifier: %w", err)
	}
	digest := sha1.Sum(ecdhKey.Bytes())
	return digest[:], nil
}
