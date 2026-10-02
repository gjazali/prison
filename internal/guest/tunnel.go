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

const (
	tunnelIdleTimeout = 300 * time.Second
	brokerDialTimeout = 15 * time.Second
	maximumHeadBytes  = 16 * 1024
)

type brokerDialer struct {
	brokerAddress string
	token         string
	timeout       time.Duration
}

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

func readConnectResponse(reader *bufio.Reader) (int, error) {
	statusLine, err := readHeadLine(reader)
	if err != nil {
		return 0, fmt.Errorf("cannot read the broker status line: %w", err)
	}
	fields := strings.SplitN(statusLine, " ", 3)
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "HTTP/1.") {
		return 0, fmt.Errorf("the broker response is not HTTP: %q", statusLine)
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil || status < 100 || status > 999 {
		return 0, fmt.Errorf("the broker status line is not valid: %q",
			statusLine)
	}
	consumed := len(statusLine)
	for {
		line, err := readHeadLine(reader)
		if err != nil {
			return 0, fmt.Errorf("cannot read the broker headers: %w", err)
		}
		if line == "" {
			return status, nil
		}
		consumed += len(line)
		if consumed > maximumHeadBytes {
			return 0, fmt.Errorf("the broker response head exceeds %d bytes",
				maximumHeadBytes)
		}
	}
}

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

func (d *brokerDialer) connect(ctx context.Context, host string,
	port int) (*tunnelConn, error) {
	dialer := net.Dialer{Timeout: d.timeout}
	raw, err := dialer.DialContext(ctx, "tcp4", d.brokerAddress)
	if err != nil {
		return nil, fmt.Errorf("cannot reach the broker at %s: %w",
			d.brokerAddress, err)
	}
	if err := raw.SetDeadline(time.Now().Add(d.timeout)); err != nil {
		raw.Close()
		return nil, err
	}
	if _, err := raw.Write(connectRequest(host, port, d.token)); err != nil {
		raw.Close()
		return nil, fmt.Errorf("cannot send CONNECT for %s:%d: %w", host, port,
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
		return nil, fmt.Errorf("the broker returned %d for %s:%d", status,
			host, port)
	}
	if err := raw.SetDeadline(time.Time{}); err != nil {
		raw.Close()
		return nil, err
	}
	return &tunnelConn{Conn: raw, reader: reader}, nil
}

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

// tunnelConn reads through `reader` first because the reader can hold
// bytes that arrived after the CONNECT response.
type tunnelConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *tunnelConn) Read(buffer []byte) (int, error) {
	return c.reader.Read(buffer)
}

func (c *tunnelConn) CloseWrite() error {
	return halfClose(c.Conn)
}

type writeCloser interface {
	CloseWrite() error
}

func halfClose(conn net.Conn) error {
	if closer, ok := conn.(writeCloser); ok {
		return closer.CloseWrite()
	}
	return nil
}

// idleWatch extends the deadline of both connections on each read, so a
// splice ends only after `timeout` with no traffic in either direction.
type idleWatch struct {
	connections []net.Conn
	timeout     time.Duration
}

func (w *idleWatch) touch() {
	deadline := time.Now().Add(w.timeout)
	for _, conn := range w.connections {
		_ = conn.SetDeadline(deadline)
	}
}

type touchingReader struct {
	reader io.Reader
	watch  *idleWatch
}

func (r touchingReader) Read(buffer []byte) (int, error) {
	count, err := r.reader.Read(buffer)
	if count > 0 {
		r.watch.touch()
	}
	return count, err
}

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

// copyDirection closes both ends on an error so that the opposite
// direction unblocks.
func copyDirection(destination, source net.Conn, watch *idleWatch) {
	_, err := io.Copy(destination, touchingReader{source, watch})
	if err != nil {
		destination.Close()
		source.Close()
		return
	}
	_ = halfClose(destination)
}

// tunnel accepts connections that the nat rule redirects and sends each
// one to the broker by name.
type tunnel struct {
	sentinels     *sentinelAllocator
	allowList     *allowListHolder
	dialer        *brokerDialer
	idleTimeout   time.Duration
	destinationOf func(*net.TCPConn) (netip.AddrPort, error)
	logger        *log.Logger
}

// permits allows all names in the reserved zone. The broker applies the
// same rule again.
func (t *tunnel) permits(name string, port int) bool {
	if protocol.InZone(name) {
		return true
	}
	return t.allowList.Current().Allows(name, port).Allowed
}

func (t *tunnel) serve(listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return fmt.Errorf("cannot accept tunnel connections: %w", err)
		}
		tcp, ok := conn.(*net.TCPConn)
		if !ok {
			conn.Close()
			continue
		}
		go t.handle(tcp)
	}
}

// handle closes a failed connection without a reply because the client
// protocol is unknown.
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
		t.logger.Printf("tunnel: no name for %s", destination)
		conn.Close()
		return
	}
	port := int(destination.Port())
	if !t.permits(name, port) {
		t.logger.Printf("tunnel: %s:%d is not in the allowlist", name, port)
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
