package guest

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"prison/internal/broker/protocol"
)

// Tunnel timing constants.
const (
	tunnelIdleTimeout = 300 * time.Second
	brokerDialTimeout = 15 * time.Second
	maximumHeadBytes  = 16 * 1024
)

// brokerDialer opens authenticated CONNECT tunnels to the broker.
type brokerDialer struct {
	brokerAddress string
	token         string
	timeout       time.Duration
}

// connectRequest builds a CONNECT request for host:port. It takes the
// host, port, and project token. Returns the request as bytes.
func connectRequest(host string, port int, token string) []byte {
	target := net.JoinHostPort(host, strconv.Itoa(port))
	var request strings.Builder
	request.WriteString("CONNECT " + target + " HTTP/1.1\r\n")
	request.WriteString("Host: " + target + "\r\n")
	request.WriteString("Proxy-Authorization: " +
		protocol.ProxyAuthorization(token) + "\r\n")
	request.WriteString("\r\n")
	return []byte(request.String())
}

// readConnectResponse reads one HTTP response head from reader.
// Returns the status code. Fails on malformed input or an oversized
// head.
func readConnectResponse(reader *bufio.Reader) (int, error) {
	statusLine, err := readHeadLine(reader)
	if err != nil {
		return 0, fmt.Errorf("reading the broker's status line: %w", err)
	}
	fields := strings.SplitN(statusLine, " ", 3)
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "HTTP/1.") {
		return 0, fmt.Errorf("the broker did not answer with HTTP: %q",
			statusLine)
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil || status < 100 || status > 999 {
		return 0, fmt.Errorf("the broker's status line is malformed: %q",
			statusLine)
	}
	consumed := len(statusLine)
	for {
		line, err := readHeadLine(reader)
		if err != nil {
			return 0, fmt.Errorf("reading the broker's headers: %w", err)
		}
		if line == "" {
			return status, nil
		}
		consumed += len(line)
		if consumed > maximumHeadBytes {
			return 0, fmt.Errorf("the broker's response head exceeds %d bytes",
				maximumHeadBytes)
		}
	}
}

// readHeadLine reads one CRLF-terminated line from reader. Returns
// the line without the terminator.
func readHeadLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	if len(line) > maximumHeadBytes {
		return "", fmt.Errorf("a header line exceeds %d bytes", maximumHeadBytes)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// connect dials the broker and opens a tunnel to host:port. Returns
// the connection on success. The returned connection supports
// CloseWrite.
func (d *brokerDialer) connect(ctx context.Context, host string,
	port int) (*tunnelConn, error) {
	dialer := net.Dialer{Timeout: d.timeout}
	raw, err := dialer.DialContext(ctx, "tcp4", d.brokerAddress)
	if err != nil {
		return nil, fmt.Errorf("reaching the broker at %s: %w",
			d.brokerAddress, err)
	}
	if err := raw.SetDeadline(time.Now().Add(d.timeout)); err != nil {
		raw.Close()
		return nil, err
	}
	if _, err := raw.Write(connectRequest(host, port, d.token)); err != nil {
		raw.Close()
		return nil, fmt.Errorf("sending CONNECT for %s:%d: %w", host, port,
			err)
	}
	reader := bufio.NewReader(raw)
	status, err := readConnectResponse(reader)
	if err != nil {
		raw.Close()
		return nil, err
	}
	if status != http.StatusOK {
		raw.Close()
		return nil, fmt.Errorf("the broker answered %d for %s:%d", status,
			host, port)
	}
	if err := raw.SetDeadline(time.Time{}); err != nil {
		raw.Close()
		return nil, err
	}
	return &tunnelConn{Conn: raw, reader: reader}, nil
}

// httpClient returns an HTTP client that routes every request through
// a CONNECT tunnel to the broker. Takes a timeout. Connections are
// not reused.
func (d *brokerDialer) httpClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network,
			address string) (net.Conn, error) {
			return d.connect(ctx, protocol.BrokerHost, 80)
		},
		DisableKeepAlives: true,
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

// tunnelConn wraps a net.Conn and drains any buffered bytes before
// reading from the socket.
type tunnelConn struct {
	net.Conn
	reader *bufio.Reader
}

// Read returns buffered bytes first, then reads from the socket.
func (c *tunnelConn) Read(buffer []byte) (int, error) {
	return c.reader.Read(buffer)
}

// CloseWrite half-closes the sending side of the connection. It is
// a no-op if the underlying connection does not support it.
func (c *tunnelConn) CloseWrite() error {
	return halfClose(c.Conn)
}

// writeCloser is the half-close interface for TCP and tunnel
// connections.
type writeCloser interface {
	CloseWrite() error
}

// halfClose shuts the sending side of conn if it supports it.
// Returns nil otherwise.
func halfClose(conn net.Conn) error {
	if closer, ok := conn.(writeCloser); ok {
		return closer.CloseWrite()
	}
	return nil
}

// idleWatch pushes the deadline of every connection in a splice
// forward on each read, so the pair is torn down only after timeout
// with no traffic in either direction.
type idleWatch struct {
	connections []net.Conn
	timeout     time.Duration
}

// touch resets every connection's deadline to timeout from now.
func (w *idleWatch) touch() {
	deadline := time.Now().Add(w.timeout)
	for _, conn := range w.connections {
		_ = conn.SetDeadline(deadline)
	}
}

// touchingReader is a reader that reports each successful read to an
// idleWatch.
type touchingReader struct {
	reader io.Reader
	watch  *idleWatch
}

// Read reads from the wrapped reader and touches the watch when bytes
// arrived.
func (r touchingReader) Read(buffer []byte) (int, error) {
	count, err := r.reader.Read(buffer)
	if count > 0 {
		r.watch.touch()
	}
	return count, err
}

// splice copies client to upstream and upstream to client until both
// directions end, half-closing the destination when its source hits
// EOF and closing both on any error, including the idle timeout. It
// returns once both connections are closed.
func splice(client, upstream net.Conn, idleTimeout time.Duration) {
	watch := &idleWatch{
		connections: []net.Conn{client, upstream},
		timeout:     idleTimeout,
	}
	watch.touch()
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		copyDirection(upstream, client, watch)
	}()
	go func() {
		defer group.Done()
		copyDirection(client, upstream, watch)
	}()
	group.Wait()
	client.Close()
	upstream.Close()
}

// copyDirection copies source to destination until EOF, then
// half-closes destination. A read or write error closes both ends so
// the opposite direction unblocks.
func copyDirection(destination, source net.Conn, watch *idleWatch) {
	_, err := io.Copy(destination, touchingReader{source, watch})
	if err != nil {
		destination.Close()
		source.Close()
		return
	}
	_ = halfClose(destination)
}

// tunnel accepts connections the nat rule redirected, recovers the
// sentinel address and port each one was dialing, checks the port
// against the allowlist, and carries the connection to the broker.
type tunnel struct {
	sentinels     *sentinelAllocator
	allowList     *allowListHolder
	dialer        *brokerDialer
	idleTimeout   time.Duration
	destinationOf func(*net.TCPConn) (netip.AddrPort, error)
	logger        *log.Logger
}

// permits reports whether name may be reached on port from inside the
// box: any port in the reserved zone, otherwise what the allowlist
// says. The broker checks again on its side.
func (t *tunnel) permits(name string, port int) bool {
	if protocol.InZone(name) {
		return true
	}
	return t.allowList.Current().Allows(name, port).Allowed
}

// serve accepts on listener for the life of the process and handles
// each connection in its own goroutine. It returns the accept error
// that stopped it.
func (t *tunnel) serve(listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return fmt.Errorf("the tunnel stopped accepting: %w", err)
		}
		tcp, ok := conn.(*net.TCPConn)
		if !ok {
			conn.Close()
			continue
		}
		go t.handle(tcp)
	}
}

// handle carries one redirected connection. It closes the connection
// without writing anything when the destination is unknown, the port
// is refused, or the broker declines, because the client is part way
// into a protocol the agent does not speak.
func (t *tunnel) handle(conn *net.TCPConn) {
	destination, err := t.destinationOf(conn)
	if err != nil {
		t.logger.Printf("tunnel: cannot read the original destination: %v",
			err)
		conn.Close()
		return
	}
	name, ok := t.sentinels.Lookup(destination.Addr())
	if !ok {
		t.logger.Printf("tunnel: %s stands for no name", destination)
		conn.Close()
		return
	}
	port := int(destination.Port())
	if !t.permits(name, port) {
		t.logger.Printf("tunnel: refused %s:%d, not in the allowlist", name,
			port)
		conn.Close()
		return
	}
	upstream, err := t.dialer.connect(context.Background(), name, port)
	if err != nil {
		t.logger.Printf("tunnel: %s:%d: %v", name, port, err)
		conn.Close()
		return
	}
	splice(conn, upstream, t.idleTimeout)
}
