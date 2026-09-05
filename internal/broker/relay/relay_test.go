package relay

import (
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// TestUsageScannerAcrossChunks checks that token counters split
// across chunks are found and the last value wins.
func TestUsageScannerAcrossChunks(t *testing.T) {
	scanner := &UsageScanner{}
	scanner.Feed([]byte(`{"usage":{"input_tokens": 10,"output_to`))
	scanner.Feed([]byte(`kens":7}}`))
	scanner.Feed([]byte(`data: {"usage":{"output_tokens":99}}`))
	if scanner.InputTokens == nil || *scanner.InputTokens != 10 {
		t.Fatalf("InputTokens = %v", scanner.InputTokens)
	}
	if scanner.OutputTokens == nil || *scanner.OutputTokens != 99 {
		t.Fatalf("OutputTokens = %v", scanner.OutputTokens)
	}
	if got := ModelFromPreview([]byte(`{"model" : "claude-x", "model":"other"}`)); got != "claude-x" {
		t.Fatalf("ModelFromPreview = %q", got)
	}
}

// TestDropHopByHopHeaders checks that hop-by-hop headers are removed
// and upgrade headers are kept when requested.
func TestDropHopByHopHeaders(t *testing.T) {
	header := http.Header{}
	header.Set("Connection", "keep-alive, X-Custom")
	header.Set("X-Custom", "1")
	header.Set("Keep-Alive", "timeout=5")
	header.Set("Transfer-Encoding", "chunked")
	header.Set("X-Api-Key", "k")
	header.Set("Accept", "*/*")
	DropHopByHopHeaders(header, false)
	for _, name := range []string{"Connection", "X-Custom", "Keep-Alive", "Transfer-Encoding"} {
		if header.Get(name) != "" {
			t.Fatalf("%s survived", name)
		}
	}
	if header.Get("Accept") != "*/*" || header.Get("X-Api-Key") != "k" {
		t.Fatal("an end-to-end header was dropped")
	}
	DropCredentialHeaders(header)
	if header.Get("X-Api-Key") != "" {
		t.Fatal("X-Api-Key survived DropCredentialHeaders")
	}

	upgrade := http.Header{}
	upgrade.Set("Connection", "Upgrade")
	upgrade.Set("Upgrade", "websocket")
	if !IsUpgradeRequest(upgrade) {
		t.Fatal("IsUpgradeRequest = false")
	}
	DropHopByHopHeaders(upgrade, true)
	if upgrade.Get("Upgrade") != "websocket" || upgrade.Get("Connection") != "Upgrade" {
		t.Fatalf("upgrade headers dropped: %v", upgrade)
	}
}

// TestSpliceMovesBothWays checks bidirectional data transfer,
// half-close handling, and total byte count.
func TestSpliceMovesBothWays(t *testing.T) {
	clientSide, clientPeer := tcpPair(t)
	upstreamSide, upstreamPeer := tcpPair(t)
	done := make(chan int64, 1)
	go func() { done <- Splice(clientSide, upstreamSide, time.Second) }()

	io.WriteString(clientPeer, "ping")
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(upstreamPeer, buffer); err != nil || string(buffer) != "ping" {
		t.Fatalf("upstream read %q, %v", buffer, err)
	}
	io.WriteString(upstreamPeer, "pong!")
	buffer = make([]byte, 5)
	if _, err := io.ReadFull(clientPeer, buffer); err != nil || string(buffer) != "pong!" {
		t.Fatalf("client read %q, %v", buffer, err)
	}
	clientPeer.(*net.TCPConn).CloseWrite()
	rest := make([]byte, 1)
	if _, err := upstreamPeer.Read(rest); err != io.EOF {
		t.Fatalf("upstream did not see the half-close: %v", err)
	}
	upstreamPeer.Close()
	select {
	case moved := <-done:
		if moved != 9 {
			t.Fatalf("moved = %d, want 9", moved)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Splice did not return")
	}
}

// TestSpliceIdleTimeout checks that a quiet connection is closed
// after the idle timeout.
func TestSpliceIdleTimeout(t *testing.T) {
	clientSide, clientPeer := tcpPair(t)
	upstreamSide, upstreamPeer := tcpPair(t)
	defer clientPeer.Close()
	defer upstreamPeer.Close()
	done := make(chan int64, 1)
	go func() { done <- Splice(clientSide, upstreamSide, 200*time.Millisecond) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Splice did not time out")
	}
}

// tcpPair returns both ends of a loopback TCP connection. Closed
// on cleanup.
func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	dialed, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server := <-accepted
	t.Cleanup(func() { dialed.Close(); server.Close() })
	return server, dialed
}
