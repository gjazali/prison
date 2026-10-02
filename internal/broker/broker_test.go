package broker

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"prison/internal/broker/brokerlog"
	"prison/internal/broker/control"
	"prison/internal/broker/protocol"
	"prison/internal/broker/relay"
	"prison/internal/state"
	"prison/internal/vault"
)

const testPassphrase = "correct horse"

type harness struct {
	t         *testing.T
	root      *state.Root
	project   *state.Project
	token     string
	client    *control.Client
	port      int
	pool      *x509.CertPool
	cert      tls.Certificate
	cancel    context.CancelFunc
	runDone   chan error
	logWriter *testLogWriter
}

// testLogWriter drops writes after the test ends because `t.Log`
// panics then.
type testLogWriter struct {
	mutex  sync.Mutex
	t      *testing.T
	closed bool
}

func (writer *testLogWriter) Write(data []byte) (int, error) {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	if !writer.closed {
		writer.t.Log(strings.TrimSpace(string(data)))
	}
	return len(data), nil
}

func (writer *testLogWriter) close() {
	writer.mutex.Lock()
	writer.closed = true
	writer.mutex.Unlock()
}

// shortTempDir keeps Unix socket paths under the length limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "pb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	return directory
}

func newTestCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "prison test upstream"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: parsed}, pool
}

func newHarness(t *testing.T, configure func(h *harness, options *Options)) *harness {
	t.Helper()
	directory := shortTempDir(t)
	root, err := state.OpenRoot(filepath.Join(directory, "root"))
	if err != nil {
		t.Fatal(err)
	}
	projectDirectory := filepath.Join(directory, "proj")
	if err := os.MkdirAll(projectDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	project, err := root.Project(projectDirectory)
	if err != nil {
		t.Fatal(err)
	}
	token, err := project.Token()
	if err != nil {
		t.Fatal(err)
	}
	cert, pool := newTestCertificate(t)
	h := &harness{
		t:         t,
		root:      root,
		project:   project,
		token:     token,
		pool:      pool,
		cert:      cert,
		runDone:   make(chan error, 1),
		logWriter: &testLogWriter{t: t},
	}
	options := Options{
		Root:              root,
		Version:           "test",
		BrokerPort:        0,
		UpstreamTLSConfig: &tls.Config{RootCAs: pool},
		Logger:            log.New(h.logWriter, "", 0),
	}
	if configure != nil {
		configure(h, &options)
	}
	h.start(options)
	return h
}

func (h *harness) start(options Options) {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.runDone <- Run(ctx, options) }()
	h.t.Cleanup(func() {
		cancel()
		select {
		case err := <-h.runDone:
			if err != nil {
				h.t.Errorf("Run returned %v", err)
			}
		case <-time.After(10 * time.Second):
			h.t.Error("the broker did not stop within ten seconds")
		}
		h.logWriter.close()
	})
	h.client = control.NewClient(h.root.BrokerSocket())
	waitFor(h.t, 5*time.Second, "the control socket", func() bool {
		return h.client.Alive(context.Background())
	})
	listener, err := h.client.Listen(context.Background(), "127.0.0.1")
	if err != nil {
		h.t.Fatal(err)
	}
	if !listener.Bound || listener.Port == 0 {
		h.t.Fatalf("listener not bound: %+v", listener)
	}
	h.port = listener.Port
}

func waitFor(t *testing.T, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (h *harness) proxyURL() *url.URL {
	return &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("127.0.0.1:%d", h.port),
		User:   url.UserPassword(protocol.ProxyUser, h.token),
	}
}

func (h *harness) proxiedClient(pool *x509.CertPool) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:             http.ProxyURL(h.proxyURL()),
			TLSClientConfig:   &tls.Config{RootCAs: pool},
			DisableKeepAlives: true,
		},
	}
}

func (h *harness) rawConnect(target, token string) (*http.Response, net.Conn) {
	h.t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", h.port), 5*time.Second)
	if err != nil {
		h.t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	authorization := ""
	if token != "" {
		authorization = "Proxy-Authorization: " + protocol.ProxyAuthorization(token) + "\r\n"
	}
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n%s\r\n", target, target, authorization)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		conn.Close()
		h.t.Fatalf("reading the CONNECT response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body = io.NopCloser(strings.NewReader(string(body)))
		conn.Close()
		return response, nil
	}
	conn.SetDeadline(time.Time{})
	return response, relay.NewBufferedConn(conn, reader)
}

func (h *harness) tunnelClient(zoneHost string) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(context.Context, string, string) (net.Conn, error) {
				response, conn := h.rawConnect(zoneHost+":80", h.token)
				if conn == nil {
					return nil, fmt.Errorf("CONNECT answered %s", response.Status)
				}
				return conn, nil
			},
		},
	}
}

func (h *harness) rawRequest(request string) (*http.Response, net.Conn, *bufio.Reader) {
	h.t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", h.port), 5*time.Second)
	if err != nil {
		h.t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(conn, request); err != nil {
		h.t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		h.t.Fatalf("reading the response: %v", err)
	}
	return response, conn, reader
}

func errorMessage(t *testing.T, response *http.Response) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("response body %q is not an error envelope: %v", data, err)
	}
	return envelope.Error.Message
}

func expectRefusal(t *testing.T, response *http.Response, status int, message string) {
	t.Helper()
	if response.StatusCode != status {
		t.Fatalf("status = %d, want %d", response.StatusCode, status)
	}
	if got := errorMessage(t, response); got != message {
		t.Fatalf("message = %q, want %q", got, message)
	}
}

func (h *harness) setEgressAllow(patterns ...string) {
	h.t.Helper()
	content := strings.Join(patterns, "\n") + "\n"
	if err := os.WriteFile(h.project.EgressAllowFile(), []byte(content), 0o600); err != nil {
		h.t.Fatal(err)
	}
	if h.client != nil {
		if err := h.client.Reload(context.Background()); err != nil {
			h.t.Fatal(err)
		}
	}
}

func (h *harness) createVault(secrets ...*vault.Secret) {
	h.t.Helper()
	created, err := vault.Create(h.root.VaultFile(), testPassphrase)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, secret := range secrets {
		if _, err := created.PutSecret(secret); err != nil {
			h.t.Fatalf("putting %s: %v", secret.Name, err)
		}
	}
	created.Lock()
}

func (h *harness) unlock() {
	h.t.Helper()
	if err := h.client.Unlock(context.Background(), testPassphrase); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) grant(names ...string) {
	h.t.Helper()
	if err := h.project.SetGrants(names); err != nil {
		h.t.Fatal(err)
	}
	if h.client != nil {
		if err := h.client.Reload(context.Background()); err != nil {
			h.t.Fatal(err)
		}
	}
}

func (h *harness) newTLSUpstream(handler http.Handler) *httptest.Server {
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{h.cert}}
	server.StartTLS()
	h.t.Cleanup(server.Close)
	return server
}

func hostPort(server *httptest.Server) string {
	return strings.TrimPrefix(strings.TrimPrefix(server.URL, "https://"), "http://")
}

func (h *harness) waitForEntry(kind string, match func(brokerlog.Entry) bool) brokerlog.Entry {
	h.t.Helper()
	var found brokerlog.Entry
	waitFor(h.t, 5*time.Second, "a "+kind+" log entry", func() bool {
		entries, err := h.client.Log(context.Background(), control.LogQuery{Kind: kind})
		if err != nil {
			return false
		}
		for _, entry := range entries {
			if match(entry) {
				found = entry
				return true
			}
		}
		return false
	})
	return found
}

func routeSecret(name, upstream string, spec vault.RouteSpec) *vault.Secret {
	if spec.PathPrefix == "" {
		spec.PathPrefix = "/"
	}
	if spec.Header == "" {
		spec.Header = "Authorization"
	}
	spec.Upstream = upstream
	return &vault.Secret{
		Name:  name,
		Mode:  vault.ModeRoute,
		Value: "s3cret-" + name,
		Route: &spec,
	}
}
