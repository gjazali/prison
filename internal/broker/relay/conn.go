package relay

import (
	"bufio"
	"net"
)

type closeWriter interface {
	CloseWrite() error
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func NewBufferedConn(conn net.Conn, reader *bufio.Reader) net.Conn {
	if reader == nil || reader.Buffered() == 0 {
		return conn
	}
	return &bufferedConn{Conn: conn, reader: reader}
}

func (c *bufferedConn) Read(buffer []byte) (int, error) {
	if c.reader.Buffered() > 0 {
		return c.reader.Read(buffer)
	}
	return c.Conn.Read(buffer)
}

func (c *bufferedConn) CloseWrite() error {
	return closeWrite(c.Conn)
}

func closeWrite(conn net.Conn) error {
	if half, ok := conn.(closeWriter); ok {
		return half.CloseWrite()
	}
	return conn.Close()
}
