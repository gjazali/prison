package ca_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"prison/internal/broker/ca"
	"prison/internal/vault"
)

// fakeStore is an in-memory Store that counts persists per host.
type fakeStore struct {
	mutex       sync.Mutex
	authorities map[string]*vault.Authority
	setCounts   map[string]int
	setError    error
}

// newFakeStore returns an empty fakeStore.
func newFakeStore() *fakeStore {
	return &fakeStore{
		authorities: map[string]*vault.Authority{},
		setCounts:   map[string]int{},
	}
}

// Authority returns the stored record for host, or false.
func (s *fakeStore) Authority(host string) (*vault.Authority, bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	authority, ok := s.authorities[host]
	return authority, ok
}

// SetAuthority stores the authority and increments the call count.
// Returns setError if set.
func (s *fakeStore) SetAuthority(
	host string, authority *vault.Authority,
) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.setError != nil {
		return s.setError
	}
	s.setCounts[host]++
	s.authorities[host] = authority
	return nil
}

// fakeClock is a controllable clock for cache expiry tests.
type fakeClock struct {
	mutex sync.Mutex
	now   time.Time
}

// Now returns the current fake time.
func (c *fakeClock) Now() time.Time {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.now
}

// Advance moves the fake time forward by d.
func (c *fakeClock) Advance(d time.Duration) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.now = c.now.Add(d)
}

// parseAuthority decodes a vault Authority into its certificate and
// private key. Fails the test on any error.
func parseAuthority(
	t *testing.T, record *vault.Authority,
) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	certificateBlock, _ := pem.Decode([]byte(record.CertificatePEM))
	if certificateBlock == nil {
		t.Fatal("authority certificate is not PEM")
	}
	certificate, err := x509.ParseCertificate(certificateBlock.Bytes)
	if err != nil {
		t.Fatalf("parsing authority certificate: %v", err)
	}
	keyBlock, _ := pem.Decode([]byte(record.KeyPEM))
	if keyBlock == nil {
		t.Fatal("authority key is not PEM")
	}
	parsedKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatalf("parsing authority key: %v", err)
	}
	key, ok := parsedKey.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatalf("authority key is %T, want ECDSA", parsedKey)
	}
	return certificate, key
}

// mintLeaf gets a leaf certificate for host via TLSConfig and
// returns it parsed.
func mintLeaf(
	t *testing.T, authorities *ca.Authorities, host string,
) *x509.Certificate {
	t.Helper()
	config, err := authorities.TLSConfig(host)
	if err != nil {
		t.Fatalf("TLSConfig(%q): %v", host, err)
	}
	certificate, err := config.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatalf("parsing leaf: %v", err)
	}
	return leaf
}

// signWithAuthority signs a leaf certificate from the template using
// the given authority. Used to craft certificates the package would
// not normally produce.
func signWithAuthority(
	t *testing.T, template *x509.Certificate,
	authority *x509.Certificate, authorityKey *ecdsa.PrivateKey,
) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	der, err := x509.CreateCertificate(
		rand.Reader, template, authority, &key.PublicKey, authorityKey)
	if err != nil {
		t.Fatalf("signing leaf: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing leaf: %v", err)
	}
	return leaf
}

// verifyOptions returns verify options that trust only the given
// authority, check the DNS name, and use the given time.
func verifyOptions(
	authority *x509.Certificate, dnsName string, now time.Time,
) x509.VerifyOptions {
	roots := x509.NewCertPool()
	roots.AddCert(authority)
	return x509.VerifyOptions{
		Roots:       roots,
		DNSName:     dnsName,
		CurrentTime: now,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
}

// expectNameConstraintFailure asserts err is a name constraint
// violation.
func expectNameConstraintFailure(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("verification succeeded, want name constraint failure")
	}
	var invalid x509.CertificateInvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("error %v is %T, want CertificateInvalidError", err, err)
	}
	if invalid.Reason != x509.CANotAuthorizedForThisName {
		t.Fatalf("reason %v, want CANotAuthorizedForThisName", invalid.Reason)
	}
}

func TestLeafVerifiesAgainstAuthority(t *testing.T) {
	const host = "api.example.com"
	clock := &fakeClock{now: time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)}
	store := newFakeStore()
	authorities := ca.New(store, clock.Now)

	leaf := mintLeaf(t, authorities, host)
	record, err := authorities.Authority(host)
	if err != nil {
		t.Fatalf("Authority: %v", err)
	}
	authority, _ := parseAuthority(t, record)

	options := verifyOptions(authority, host, clock.Now())
	if _, err := leaf.Verify(options); err != nil {
		t.Fatalf("leaf does not verify: %v", err)
	}
	if leaf.Subject.CommonName != host {
		t.Errorf("leaf CN %q, want %q", leaf.Subject.CommonName, host)
	}
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != host {
		t.Errorf("leaf DNSNames %v, want [%s]", leaf.DNSNames, host)
	}
	if leaf.KeyUsage != x509.KeyUsageDigitalSignature {
		t.Errorf("leaf KeyUsage %v, want DigitalSignature", leaf.KeyUsage)
	}
	if leaf.IsCA {
		t.Error("leaf is a CA")
	}
	maxLeafLifetime := 825 * 24 * time.Hour
	if leaf.NotAfter.Sub(leaf.NotBefore) > maxLeafLifetime+time.Minute {
		t.Errorf("leaf lifetime %v exceeds 825 days",
			leaf.NotAfter.Sub(leaf.NotBefore))
	}
}

func TestAuthorityCertificateShape(t *testing.T) {
	const host = "api.example.com"
	authorities := ca.New(newFakeStore(), nil)
	record, err := authorities.Authority(host)
	if err != nil {
		t.Fatalf("Authority: %v", err)
	}
	authority, key := parseAuthority(t, record)

	if key.Curve != elliptic.P256() {
		t.Errorf("curve %v, want P-256", key.Curve.Params().Name)
	}
	wantSubject := "prison certificate authority for " + host
	if authority.Subject.CommonName != wantSubject {
		t.Errorf("CN %q, want %q", authority.Subject.CommonName, wantSubject)
	}
	if !authority.IsCA || !authority.BasicConstraintsValid {
		t.Error("authority is not a CA")
	}
	if authority.MaxPathLen != 0 || !authority.MaxPathLenZero {
		t.Errorf("MaxPathLen %d (zero=%v), want 0 with MaxPathLenZero",
			authority.MaxPathLen, authority.MaxPathLenZero)
	}
	wantUsage := x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	if authority.KeyUsage != wantUsage {
		t.Errorf("KeyUsage %v, want CertSign|CRLSign", authority.KeyUsage)
	}
	if len(authority.SubjectKeyId) == 0 {
		t.Error("SubjectKeyId is empty")
	}
	if !authority.PermittedDNSDomainsCritical {
		t.Error("name constraints are not critical")
	}
	permitted := authority.PermittedDNSDomains
	if len(permitted) != 1 || permitted[0] != host {
		t.Errorf("PermittedDNSDomains %v, want [%s]", permitted, host)
	}
	if len(authority.ExcludedIPRanges) != 2 {
		t.Fatalf("ExcludedIPRanges %v, want two ranges", authority.ExcludedIPRanges)
	}
	for _, ipRange := range authority.ExcludedIPRanges {
		if ones, _ := ipRange.Mask.Size(); ones != 0 {
			t.Errorf("excluded range %v is not a whole address family", ipRange)
		}
	}
	lifetime := authority.NotAfter.Sub(authority.NotBefore)
	if lifetime < 10*365*24*time.Hour {
		t.Errorf("authority lifetime %v is shorter than ten years", lifetime)
	}
	if record.Created.IsZero() {
		t.Error("record Created is zero")
	}
}

func TestLeafForOtherNameIsRejected(t *testing.T) {
	const host = "api.example.com"
	const otherHost = "other.example.net"
	now := time.Now()
	authorities := ca.New(newFakeStore(), nil)
	record, err := authorities.Authority(host)
	if err != nil {
		t.Fatalf("Authority: %v", err)
	}
	authority, authorityKey := parseAuthority(t, record)

	badLeaf := signWithAuthority(t, &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: otherHost},
		DNSNames:     []string{otherHost},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, authority, authorityKey)

	_, err = badLeaf.Verify(verifyOptions(authority, otherHost, now))
	expectNameConstraintFailure(t, err)

	// DNS constraints allow subdomains, so the broker holds one
	// authority per exact host.
	_, err = badLeaf.Verify(verifyOptions(authority, host, now))
	if err == nil {
		t.Fatal("leaf for another name verified for the permitted host")
	}
}

func TestLeafWithIPAddressIsRejected(t *testing.T) {
	const host = "api.example.com"
	now := time.Now()
	authorities := ca.New(newFakeStore(), nil)
	record, err := authorities.Authority(host)
	if err != nil {
		t.Fatalf("Authority: %v", err)
	}
	authority, authorityKey := parseAuthority(t, record)

	addresses := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	for _, address := range addresses {
		badLeaf := signWithAuthority(t, &x509.Certificate{
			SerialNumber: big.NewInt(2),
			Subject:      pkix.Name{CommonName: host},
			DNSNames:     []string{host},
			IPAddresses:  []net.IP{address},
			NotBefore:    now.Add(-time.Minute),
			NotAfter:     now.Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}, authority, authorityKey)
		_, err := badLeaf.Verify(verifyOptions(authority, host, now))
		expectNameConstraintFailure(t, err)
	}
}

func TestTLSRoundTrip(t *testing.T) {
	const host = "api.example.com"
	authorities := ca.New(newFakeStore(), nil)
	serverConfig, err := authorities.TLSConfig(host)
	if err != nil {
		t.Fatalf("TLSConfig: %v", err)
	}
	bundle, err := authorities.Bundle([]string{host})
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(bundle) {
		t.Fatal("bundle holds no certificates")
	}

	listener, err := tls.Listen("tcp", "127.0.0.1:0", serverConfig)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	serverErrors := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverErrors <- err
			return
		}
		defer connection.Close()
		_, err = io.Copy(connection, connection)
		serverErrors <- err
	}()

	client, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{
		RootCAs:    roots,
		ServerName: host,
		NextProtos: []string{"h2", "http/1.1"},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	state := client.ConnectionState()
	if state.NegotiatedProtocol != "" {
		t.Errorf("negotiated ALPN %q, want none", state.NegotiatedProtocol)
	}
	if state.Version < tls.VersionTLS12 {
		t.Errorf("TLS version %x is older than 1.2", state.Version)
	}

	message := []byte("hello through the tunnel")
	if _, err := client.Write(message); err != nil {
		t.Fatalf("write: %v", err)
	}
	echoed := make([]byte, len(message))
	if _, err := io.ReadFull(client, echoed); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(echoed, message) {
		t.Fatalf("echoed %q, want %q", echoed, message)
	}
	client.Close()
	if err := <-serverErrors; err != nil {
		t.Fatalf("server: %v", err)
	}
}

func TestClientRefusesLeafFromWrongAuthority(t *testing.T) {
	const host = "api.example.com"
	authorities := ca.New(newFakeStore(), nil)
	serverConfig, err := authorities.TLSConfig(host)
	if err != nil {
		t.Fatalf("TLSConfig: %v", err)
	}
	bundle, err := authorities.Bundle([]string{"unrelated.example.org"})
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(bundle)

	listener, err := tls.Listen("tcp", "127.0.0.1:0", serverConfig)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			io.Copy(io.Discard, connection)
			connection.Close()
		}
	}()

	client, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{
		RootCAs:    roots,
		ServerName: host,
	})
	if err == nil {
		client.Close()
		t.Fatal("dial succeeded with an unrelated authority")
	}
}

func TestLeafIsCachedForOneHour(t *testing.T) {
	const host = "api.example.com"
	clock := &fakeClock{now: time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)}
	authorities := ca.New(newFakeStore(), clock.Now)

	first := mintLeaf(t, authorities, host)
	clock.Advance(59 * time.Minute)
	second := mintLeaf(t, authorities, host)
	if first.SerialNumber.Cmp(second.SerialNumber) != 0 {
		t.Fatal("leaf was re-minted within the hour")
	}

	clock.Advance(2 * time.Minute)
	third := mintLeaf(t, authorities, host)
	if first.SerialNumber.Cmp(third.SerialNumber) == 0 {
		t.Fatal("leaf was reused after the hour")
	}
	if !first.PublicKey.(*ecdsa.PublicKey).Equal(third.PublicKey) {
		t.Error("leaf key changed between mints; want one per process")
	}
	if !third.NotBefore.After(first.NotBefore) {
		t.Error("re-minted leaf does not use the advanced clock")
	}
}

func TestBundleIsStableAndPersistsOnce(t *testing.T) {
	store := newFakeStore()
	authorities := ca.New(store, nil)

	first, err := authorities.Bundle([]string{
		"B.Example.com.", "a.example.com", "b.example.com"})
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	second, err := authorities.Bundle([]string{"a.example.com", "b.example.com"})
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("bundle differs between calls")
	}

	var subjects []string
	rest := first
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			t.Fatalf("block type %q, want CERTIFICATE", block.Type)
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("parsing bundle certificate: %v", err)
		}
		subjects = append(subjects, certificate.PermittedDNSDomains[0])
	}
	wantSubjects := []string{"a.example.com", "b.example.com"}
	if len(subjects) != len(wantSubjects) {
		t.Fatalf("bundle hosts %v, want %v", subjects, wantSubjects)
	}
	for index := range wantSubjects {
		if subjects[index] != wantSubjects[index] {
			t.Fatalf("bundle hosts %v, want %v", subjects, wantSubjects)
		}
	}
	if len(rest) != 0 {
		t.Errorf("bundle has %d trailing bytes", len(rest))
	}

	for host, count := range store.setCounts {
		if count != 1 {
			t.Errorf("host %s persisted %d times, want 1", host, count)
		}
	}
	if len(store.setCounts) != 2 {
		t.Errorf("persisted %d hosts, want 2", len(store.setCounts))
	}

	empty, err := authorities.Bundle(nil)
	if err != nil {
		t.Fatalf("Bundle(nil): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("Bundle(nil) returned %d bytes", len(empty))
	}
}

func TestAuthorityIsReusedFromStore(t *testing.T) {
	const host = "api.example.com"
	store := newFakeStore()
	firstRecord, err := ca.New(store, nil).Authority(host)
	if err != nil {
		t.Fatalf("Authority: %v", err)
	}
	secondRecord, err := ca.New(store, nil).Authority(host)
	if err != nil {
		t.Fatalf("Authority: %v", err)
	}
	if firstRecord.CertificatePEM != secondRecord.CertificatePEM {
		t.Fatal("a second Authorities over the same store minted a new authority")
	}
	if store.setCounts[host] != 1 {
		t.Fatalf("persisted %d times, want 1", store.setCounts[host])
	}
}

func TestConcurrentCreationYieldsOneAuthority(t *testing.T) {
	const host = "api.example.com"
	store := newFakeStore()
	authorities := ca.New(store, nil)

	var group sync.WaitGroup
	records := make([]*vault.Authority, 16)
	for index := range records {
		group.Add(1)
		go func() {
			defer group.Done()
			record, err := authorities.Authority(host)
			if err != nil {
				t.Errorf("Authority: %v", err)
				return
			}
			records[index] = record
		}()
	}
	group.Wait()

	for _, record := range records[1:] {
		if record.CertificatePEM != records[0].CertificatePEM {
			t.Fatal("concurrent callers received different authorities")
		}
	}
	if store.setCounts[host] != 1 {
		t.Fatalf("persisted %d times, want 1", store.setCounts[host])
	}
}

func TestStoreFailureIsNotCached(t *testing.T) {
	const host = "api.example.com"
	store := newFakeStore()
	store.setError = errors.New("vault is locked")
	authorities := ca.New(store, nil)

	if _, err := authorities.Authority(host); err == nil {
		t.Fatal("Authority succeeded with a failing store")
	}
	if _, err := authorities.TLSConfig(host); err == nil {
		t.Fatal("TLSConfig succeeded with a failing store")
	}
	store.setError = nil
	if _, err := authorities.Authority(host); err != nil {
		t.Fatalf("Authority after store recovered: %v", err)
	}
}

func TestHostNormalization(t *testing.T) {
	cases := []struct {
		input string
		want  string
		valid bool
	}{
		{"API.Example.com", "api.example.com", true},
		{"api.example.com.", "api.example.com", true},
		{"localhost", "localhost", true},
		{"", "", false},
		{".", "", false},
		{"api.example.com:443", "", false},
		{"https://api.example.com", "", false},
		{"api.example.com/v1", "", false},
		{"api example.com", "", false},
		{"[::1]", "", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.input, func(t *testing.T) {
			store := newFakeStore()
			_, err := ca.New(store, nil).Authority(testCase.input)
			if !testCase.valid {
				if !errors.Is(err, ca.ErrInvalidHost) {
					t.Fatalf("error %v, want ErrInvalidHost", err)
				}
				if len(store.setCounts) != 0 {
					t.Fatal("an invalid host was persisted")
				}
				return
			}
			if err != nil {
				t.Fatalf("Authority: %v", err)
			}
			if store.setCounts[testCase.want] != 1 {
				t.Fatalf("persisted under %v, want %q", store.setCounts, testCase.want)
			}
		})
	}
}
