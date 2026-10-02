package relay

import (
	"errors"
	"net"
	"net/http"
	"sync"
)

// singleConnectionListener blocks later Accept calls until the
// connection closes.
type singleConnectionListener struct {
	conn      net.Conn
	accepted  chan net.Conn
	closed    chan struct{}
	closeOnce sync.Once
}

func newSingleConnectionListener(conn net.Conn) *singleConnectionListener {
	listener := &singleConnectionListener{
		accepted: make(chan net.Conn, 1),
		closed:   make(chan struct{}),
	}
	listener.conn = &closingConn{Conn: conn, onClose: listener.Close}
	listener.accepted <- listener.conn
	return listener
}

func (listener *singleConnectionListener) Accept() (net.Conn, error) {
	select {
	case conn := <-listener.accepted:
		return conn, nil
	case <-listener.closed:
		return nil, net.ErrClosed
	}
}

// Close leaves the connection open.
func (listener *singleConnectionListener) Close() error {
	listener.closeOnce.Do(func() { close(listener.closed) })
	return nil
}

func (listener *singleConnectionListener) Addr() net.Addr {
	return listener.conn.LocalAddr()
}

type closingConn struct {
	net.Conn
	onClose func() error
	once    sync.Once
}

func (conn *closingConn) Close() error {
	err := conn.Conn.Close()
	conn.once.Do(func() { conn.onClose() })
	return err
}

func (conn *closingConn) CloseWrite() error {
	return closeWrite(conn.Conn)
}

func ServeConnection(server *http.Server, conn net.Conn) error {
	listener := newSingleConnectionListener(conn)
	err := server.Serve(listener)
	if errors.Is(err, net.ErrClosed) || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
