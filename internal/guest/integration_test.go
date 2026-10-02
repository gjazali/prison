//go:build integration

package guest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"prison/internal/broker/protocol"
)

const (
	integrationToken   = "integration-token"
	integrationNetwork = "prison"
	integrationHost    = "example.test"
	integrationPort    = 9000
)

func integrationBrokerPort(t *testing.T) int {
	t.Helper()
	text := os.Getenv("PRISON_GUEST_TEST_BROKER_PORT")
	if text == "" {
		return protocol.DefaultBrokerPort
	}
	port, err := strconv.Atoi(text)
	if err != nil {
		t.Fatalf("PRISON_GUEST_TEST_BROKER_PORT=%q is not a port", text)
	}
	return port
}

func TestIntegrationBox(t *testing.T) {
	if _, err := exec.LookPath("container"); err != nil {
		t.Skip("the container CLI is not on PATH")
	}
	gateway, err := networkGateway(integrationNetwork)
	if err != nil {
		t.Skipf("no %s network: %v", integrationNetwork, err)
	}
	started := time.Now()
	testdata, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	brokerPort := integrationBrokerPort(t)
	suffix := strconv.Itoa(os.Getpid())
	imageTag := "prison/guest-test:" + suffix
	boxName := "prison-guest-test-" + suffix

	buildGuestBinary(t, filepath.Join(testdata, "prison-guest"))
	runContainer(t, 3*time.Minute, "build", "--tag", imageTag, testdata)
	t.Cleanup(func() {
		_, _ = containerOutput(30*time.Second, "image", "delete", imageTag)
	})

	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		_, _ = io.WriteString(w, "echo-ok\n")
	}))
	defer echo.Close()
	broker := &integrationBroker{
		echoAddress: echo.Listener.Addr().String(),
		hostsServed: make(chan struct{}),
	}

	runContainer(t, time.Minute, "run", "--detach", "--name", boxName,
		"--cap-add", "CAP_NET_ADMIN", "--network", integrationNetwork,
		"-e", "PRISON_TOKEN="+integrationToken,
		"-e", "PRISON_BROKER_PORT="+strconv.Itoa(brokerPort),
		"-e", "PRISON_SUDO=no",
		"-e", "PRISON_PERSIST_PATHS=/home/dev",
		imageTag)
	t.Cleanup(func() {
		_, _ = containerOutput(30*time.Second, "stop", boxName)
		_, _ = containerOutput(30*time.Second, "delete", boxName)
	})

	brokerAddress := net.JoinHostPort(gateway, strconv.Itoa(brokerPort))
	listener := listenWithRetry(t, brokerAddress, time.Minute)
	server := &http.Server{Handler: broker}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	waitForReady(t, boxName, 90*time.Second)
	t.Logf("box ready after %s", time.Since(started).Round(time.Second))
	select {
	case <-broker.hostsServed:
	case <-time.After(60 * time.Second):
		logs, _ := containerOutput(10*time.Second, "logs", boxName)
		t.Fatalf("GET /v1/hosts = none, want a request\nlogs:\n%s", logs)
	}

	execDev := func(timeout time.Duration, command ...string) (string, error) {
		arguments := append([]string{"exec", "--uid", "501", boxName},
			command...)
		return containerOutput(timeout, arguments...)
	}

	output, err := execDev(10*time.Second, "getent", "hosts", integrationHost)
	if err != nil || !strings.HasPrefix(strings.TrimSpace(output), "127.99.") {
		t.Errorf("getent hosts %s = %q, %v, want a 127.99.x.y address",
			integrationHost, output, err)
	}
	if output, err := execDev(10*time.Second, "getent", "hosts",
		"nope.test"); err == nil {
		t.Errorf("getent hosts nope.test = %q, want an error", output)
	}
	output, err = execDev(15*time.Second, "curl", "--silent",
		"--show-error", "--max-time", "10",
		fmt.Sprintf("http://%s:%d/", integrationHost, integrationPort))
	if err != nil || strings.TrimSpace(output) != "echo-ok" {
		t.Errorf("curl through the tunnel = %q, %v, want echo-ok", output, err)
	}
	if output, err := execDev(15*time.Second, "curl", "--silent",
		"--max-time", "3", "https://1.1.1.1"); err == nil {
		t.Errorf("curl https://1.1.1.1 = %q, want an error", output)
	}
	refusedAt := time.Now()
	output, err = execDev(15*time.Second, "curl", "--silent", "--show-error",
		"--max-time", "10",
		fmt.Sprintf("http://%s:%d/", integrationHost, integrationPort+1))
	if err == nil {
		t.Errorf("curl to a refused port = %q, want an error", output)
	}
	if elapsed := time.Since(refusedAt); elapsed > 3*time.Second {
		t.Errorf("refused curl time = %s, want under 3s", elapsed)
	}
	output, err = execDev(10*time.Second, "id", "-u")
	if err != nil || strings.TrimSpace(output) != "501" {
		t.Errorf("id -u = %q, %v, want 501", output, err)
	}
	seen := broker.targets()
	if !containsString(seen, fmt.Sprintf("%s:%d", integrationHost,
		integrationPort)) {
		t.Errorf("broker targets = %v, want the echo target", seen)
	}
	if containsString(seen, fmt.Sprintf("%s:%d", integrationHost,
		integrationPort+1)) {
		t.Errorf("broker targets = %v, want no refused port", seen)
	}

	stopStarted := time.Now()
	if _, err := containerOutput(20*time.Second, "stop", boxName); err != nil {
		t.Errorf("container stop: %v", err)
	}
	if elapsed := time.Since(stopStarted); elapsed > 5*time.Second {
		t.Errorf("container stop time = %s, want under 5s", elapsed)
	}
	if logs, err := containerOutput(10*time.Second, "logs", boxName); err == nil {
		t.Logf("box logs:\n%s", logs)
	}
}

type integrationBroker struct {
	echoAddress string
	hostsServed chan struct{}
	hostsOnce   sync.Once
	mutex       sync.Mutex
	seen        []string
}

func (b *integrationBroker) targets() []string {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return append([]string(nil), b.seen...)
}

func (b *integrationBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "only CONNECT is supported", http.StatusBadRequest)
		return
	}
	if r.Header.Get("Proxy-Authorization") !=
		protocol.ProxyAuthorization(integrationToken) {
		http.Error(w, "bad token", http.StatusProxyAuthRequired)
		return
	}
	target := r.Host
	b.mutex.Lock()
	b.seen = append(b.seen, target)
	b.mutex.Unlock()
	echoTarget := fmt.Sprintf("%s:%d", integrationHost, integrationPort)
	switch target {
	case protocol.BrokerHost + ":80":
		conn, reader := hijackWithOK(w)
		if conn == nil {
			return
		}
		b.serveAPI(&tunnelConn{Conn: conn, reader: reader})
	case echoTarget:
		upstream, err := net.Dial("tcp", b.echoAddress)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		conn, reader := hijackWithOK(w)
		if conn == nil {
			upstream.Close()
			return
		}
		splice(&tunnelConn{Conn: conn, reader: reader}, upstream,
			30*time.Second)
	default:
		http.Error(w, "not in the allowlist", http.StatusForbidden)
	}
}

func (b *integrationBroker) serveAPI(conn net.Conn) {
	mux := http.NewServeMux()
	mux.HandleFunc(protocol.PathHosts, func(w http.ResponseWriter,
		r *http.Request) {
		fmt.Fprintf(w, "%s:%d\n", integrationHost, integrationPort)
		b.hostsOnce.Do(func() { close(b.hostsServed) })
	})
	mux.HandleFunc(protocol.PathCertificates, func(w http.ResponseWriter,
		r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	listener := newSingleConnListener(conn)
	server := &http.Server{
		Handler: mux,
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateClosed {
				listener.Close()
			}
		},
	}
	_ = server.Serve(listener)
}

func hijackWithOK(w http.ResponseWriter) (net.Conn, *bufio.Reader) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "cannot hijack", http.StatusInternalServerError)
		return nil, nil
	}
	conn, readWriter, err := hijacker.Hijack()
	if err != nil {
		return nil, nil
	}
	_, _ = readWriter.WriteString("HTTP/1.1 200 Connection established\r\n\r\n")
	if err := readWriter.Flush(); err != nil {
		conn.Close()
		return nil, nil
	}
	return conn, readWriter.Reader
}

type singleConnListener struct {
	conn   net.Conn
	accept chan net.Conn
	done   chan struct{}
	once   sync.Once
}

func newSingleConnListener(conn net.Conn) *singleConnListener {
	listener := &singleConnListener{
		conn:   conn,
		accept: make(chan net.Conn, 1),
		done:   make(chan struct{}),
	}
	listener.accept <- conn
	return listener
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.accept:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *singleConnListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *singleConnListener) Addr() net.Addr {
	return l.conn.LocalAddr()
}

func buildGuestBinary(t *testing.T, output string) {
	t.Helper()
	moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w",
		"-o", output, "./cmd/prison-guest")
	build.Dir = moduleRoot
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH,
		"CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("cannot build the guest: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = os.Remove(output) })
}

func networkGateway(network string) (string, error) {
	output, err := containerOutput(20*time.Second, "network", "inspect",
		network)
	if err != nil {
		return "", err
	}
	var entries []struct {
		Status struct {
			Gateway string `json:"ipv4Gateway"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(output), &entries); err != nil {
		return "", fmt.Errorf("cannot parse network inspect: %w", err)
	}
	if len(entries) == 0 || entries[0].Status.Gateway == "" {
		return "", errors.New("the network has no IPv4 gateway")
	}
	return entries[0].Status.Gateway, nil
}

func containerOutput(timeout time.Duration, arguments ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, "container",
		arguments...).CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("container %s: %w: %s",
			strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func runContainer(t *testing.T, timeout time.Duration, arguments ...string) {
	t.Helper()
	if _, err := containerOutput(timeout, arguments...); err != nil {
		t.Fatal(err)
	}
}

func listenWithRetry(t *testing.T, address string, timeout time.Duration) net.Listener {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		listener, err := net.Listen("tcp4", address)
		if err == nil {
			return listener
		}
		if errors.Is(err, syscall.EADDRINUSE) {
			t.Fatalf("%s is in use. Stop the broker or set"+
				" PRISON_GUEST_TEST_BROKER_PORT: %v", address, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("cannot bind the fake broker on %s: %v", address, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func waitForReady(t *testing.T, boxName string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		_, last = containerOutput(15*time.Second, "exec", boxName,
			guestBinaryPath, "ready")
		if last == nil {
			return
		}
		time.Sleep(time.Second)
	}
	logs, _ := containerOutput(10*time.Second, "logs", boxName)
	t.Fatalf("the box is not ready: %v\nlogs:\n%s", last, logs)
}

func containsString(items []string, wanted string) bool {
	for _, item := range items {
		if item == wanted {
			return true
		}
	}
	return false
}
