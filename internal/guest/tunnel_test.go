package guest

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"prison/internal/broker/protocol"
	"prison/internal/policy"
)

// TestConnectRequestBytes checks the CONNECT request format and
// credential encoding.
func TestConnectRequestBytes(t *testing.T) {
	got := string(connectRequest("example.test", 9000, "secret-token"))
	want := "CONNECT example.test:9000 HTTP/1.1\r\n" +
		"Host: example.test:9000\r\n" +
		"Proxy-Authorization: " + protocol.ProxyAuthorization("secret-token") +
		"\r\n\r\n"
	if got != want {
		t.Fatalf("request:\n%q\nwant:\n%q", got, want)
	}
	if !strings.Contains(got, "Basic cHJpc29uOnNlY3JldC10b2tlbg==") {
		t.Fatalf("credentials not encoded as prison:token: %q", got)
	}
}

// TestReadConnectResponse checks status parsing, head consumption,
// and rejection of malformed or oversized heads.
func TestReadConnectResponse(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(
		"HTTP/1.1 200 Connection established\r\nX-Note: hi\r\n\r\npayload"))
	status, err := readConnectResponse(reader)
	if err != nil || status != 200 {
		t.Fatalf("status %d, %v", status, err)
	}
	rest, _ := io.ReadAll(reader)
	if string(rest) != "payload" {
		t.Fatalf("bytes after the head: %q", rest)
	}
	reader = bufio.NewReader(strings.NewReader("HTTP/1.0 407 Nope\r\n\r\n"))
	if status, err := readConnectResponse(reader); err != nil || status != 407 {
		t.Fatalf("407: status %d, %v", status, err)
	}
	for _, bad := range []string{
		"", "hello\r\n\r\n", "HTTP/1.1 abc\r\n\r\n", "HTTP/1.1 200 OK\r\n",
		"HTTP/1.1 200 OK\r\n" + strings.Repeat("X: y\r\n", 5000) + "\r\n",
	} {
		reader = bufio.NewReader(strings.NewReader(bad))
		if _, err := readConnectResponse(reader); err == nil {
			t.Fatalf("%.30q parsed without error", bad)
		}
	}
}

// fakeBroker accepts CONNECT requests on loopback, checks the
// token, and splices accepted targets to upstreamAddress or
// refuses with a status.
type fakeBroker struct {
	listener        net.Listener
	token           string
	upstreamAddress string
	allowedTarget   string
	refusalStatus   string
	seen            chan string
}

// startFakeBroker starts a fake broker on loopback. Stops when the
// test ends.
func startFakeBroker(t *testing.T, token, allowedTarget,
	upstreamAddress string) *fakeBroker {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	broker := &fakeBroker{
		listener:        listener,
		token:           token,
		upstreamAddress: upstreamAddress,
		allowedTarget:   allowedTarget,
		refusalStatus:   "403 Forbidden",
		seen:            make(chan string, 16),
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go broker.handle(conn)
		}
	}()
	return broker
}

// handle reads one CONNECT request and responds.
func (b *fakeBroker) handle(conn net.Conn) {
	reader := bufio.NewReader(conn)
	requestLine, err := reader.ReadString('\n')
	if err != nil {
		conn.Close()
		return
	}
	authorized := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			conn.Close()
			return
		}
		if line == "\r\n" {
			break
		}
		name, value, _ := strings.Cut(strings.TrimRight(line, "\r\n"), ": ")
		if strings.EqualFold(name, "Proxy-Authorization") {
			token, ok := protocol.TokenFromProxyAuthorization(value)
			authorized = ok && token == b.token
		}
	}
	fields := strings.Fields(requestLine)
	target := ""
	if len(fields) >= 2 && fields[0] == "CONNECT" {
		target = fields[1]
	}
	b.seen <- target
	switch {
	case !authorized:
		_, _ = io.WriteString(conn, "HTTP/1.1 407 Proxy Authentication"+
			" Required\r\nContent-Length: 0\r\n\r\n")
		conn.Close()
	case target != b.allowedTarget:
		_, _ = io.WriteString(conn, "HTTP/1.1 "+b.refusalStatus+
			"\r\nContent-Length: 0\r\n\r\n")
		conn.Close()
	default:
		upstream, err := net.Dial("tcp4", b.upstreamAddress)
		if err != nil {
			_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
			conn.Close()
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established"+
			"\r\n\r\n")
		splice(conn, upstream, 10*time.Second)
	}
}

// startEchoServer listens on loopback and echoes each connection
// with a prefix. Returns the listener address.
func startEchoServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				data, _ := io.ReadAll(conn)
				_, _ = conn.Write(append([]byte("echo:"), data...))
			}()
		}
	}()
	return listener.Addr().String()
}

// TestBrokerDialerConnect checks tunnel opening, credential
// handling, non-200 errors, and half-close through the splice.
func TestBrokerDialerConnect(t *testing.T) {
	echo := startEchoServer(t)
	broker := startFakeBroker(t, "tok", "example.test:9000", echo)
	dialer := &brokerDialer{
		brokerAddress: broker.listener.Addr().String(),
		token:         "tok",
		timeout:       5 * time.Second,
	}
	conn, err := dialer.connect(context.Background(), "example.test", 9000)
	if err != nil {
		t.Fatal(err)
	}
	if <-broker.seen != "example.test:9000" {
		t.Fatal("the broker saw a different target")
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	answer, err := io.ReadAll(conn)
	conn.Close()
	if err != nil || string(answer) != "echo:ping" {
		t.Fatalf("answer %q, %v", answer, err)
	}
	if _, err := dialer.connect(context.Background(), "other.test", 443); err ==
		nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("refusal error = %v, want one naming 403", err)
	}
	<-broker.seen
	wrong := &brokerDialer{brokerAddress: dialer.brokerAddress,
		token: "bad", timeout: 5 * time.Second}
	if _, err := wrong.connect(context.Background(), "example.test", 9000); err ==
		nil || !strings.Contains(err.Error(), "407") {
		t.Fatalf("bad token error = %v, want one naming 407", err)
	}
}

// TestTunnelHandle checks the tunnel accept path: allowed ports are
// spliced, refused ports and unknown sentinels are closed.
func TestTunnelHandle(t *testing.T) {
	echo := startEchoServer(t)
	broker := startFakeBroker(t, "tok", "example.test:9000", echo)
	patterns, _ := policy.ParseStrings([]string{"example.test:9000"})
	holder := newAllowListHolder()
	holder.Replace(policy.NewAllowList(patterns))
	sentinels := newSentinelAllocator()
	sentinel, _ := sentinels.Allocate("example.test")
	var destination netip.AddrPort
	server := &tunnel{
		sentinels:   sentinels,
		allowList:   holder,
		idleTimeout: 10 * time.Second,
		dialer: &brokerDialer{brokerAddress: broker.listener.Addr().String(),
			token: "tok", timeout: 5 * time.Second},
		destinationOf: func(*net.TCPConn) (netip.AddrPort, error) {
			return destination, nil
		},
		logger: log.New(io.Discard, "", 0),
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() { _ = server.serve(listener) }()

	roundTrip := func(target netip.AddrPort) ([]byte, error) {
		destination = target
		conn, err := net.Dial("tcp4", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = conn.Write([]byte("hello"))
		_ = conn.(*net.TCPConn).CloseWrite()
		return io.ReadAll(conn)
	}
	answer, err := roundTrip(netip.AddrPortFrom(sentinel, 9000))
	if err != nil || !bytes.Equal(answer, []byte("echo:hello")) {
		t.Fatalf("allowed port: %q, %v", answer, err)
	}
	<-broker.seen
	answer, _ = roundTrip(netip.AddrPortFrom(sentinel, 9001))
	if len(answer) != 0 {
		t.Fatalf("refused port answered %q", answer)
	}
	select {
	case target := <-broker.seen:
		t.Fatalf("refused port reached the broker as %s", target)
	case <-time.After(200 * time.Millisecond):
	}
	answer, _ = roundTrip(netip.AddrPortFrom(
		netip.MustParseAddr("127.99.200.200"), 9000))
	if len(answer) != 0 {
		t.Fatalf("unknown sentinel answered %q", answer)
	}
	if !server.permits(protocol.BrokerHost, 12345) {
		t.Fatal("zone names must be permitted on any port")
	}
}

// TestSpliceIdleTimeout checks that idle connections are torn down
// after the timeout and that traffic resets the deadline.
func TestSpliceIdleTimeout(t *testing.T) {
	clientSide, tunnelClient := net.Pipe()
	tunnelUpstream, upstreamSide := net.Pipe()
	defer clientSide.Close()
	defer upstreamSide.Close()
	done := make(chan struct{})
	go func() {
		splice(tunnelClient, tunnelUpstream, 300*time.Millisecond)
		close(done)
	}()
	for round := 0; round < 3; round++ {
		time.Sleep(150 * time.Millisecond)
		go func() { _, _ = clientSide.Write([]byte("x")) }()
		buffer := make([]byte, 1)
		if _, err := upstreamSide.Read(buffer); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
	}
	select {
	case <-done:
		t.Fatal("splice ended while traffic was flowing")
	default:
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("splice did not end after the idle timeout")
	}
}
