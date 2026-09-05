package relay

import (
	"bufio"
	"net"
)

// closeWriter is a connection that supports half-closing the write
// side.
type closeWriter interface {
	CloseWrite() error
}

// bufferedConn wraps a hijacked connection so buffered bytes are read
// first, then reads go to the underlying connection.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

// NewBufferedConn wraps conn so buffered bytes are read first. Takes
// the connection and its hijack reader. A nil or empty reader returns
// conn unchanged.
func NewBufferedConn(conn net.Conn, reader *bufio.Reader) net.Conn {
	if reader == nil || reader.Buffered() == 0 {
		return conn
	}
	return &bufferedConn{Conn: conn, reader: reader}
}

// Read drains buffered bytes first, then reads from the connection.
func (c *bufferedConn) Read(buffer []byte) (int, error) {
	if c.reader.Buffered() > 0 {
		return c.reader.Read(buffer)
	}
	return c.Conn.Read(buffer)
}

// CloseWrite half-closes the write side, or fully closes if not
// supported.
func (c *bufferedConn) CloseWrite() error {
	return closeWrite(c.Conn)
}

// closeWrite half-closes the write side of conn, or fully closes it
// if half-close is not supported.
func closeWrite(conn net.Conn) error {
	if half, ok := conn.(closeWriter); ok {
		return half.CloseWrite()
	}
	return conn.Close()
}
