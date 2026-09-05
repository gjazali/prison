package relay

import (
	"errors"
	"net"
	"net/http"
	"sync"
)

// singleConnectionListener serves exactly one connection. Later
// Accept calls block until the connection closes.
type singleConnectionListener struct {
	conn      net.Conn
	accepted  chan net.Conn
	closed    chan struct{}
	closeOnce sync.Once
}

// newSingleConnectionListener returns a listener that serves the
// given connection once. The listener closes when the connection
// closes.
func newSingleConnectionListener(conn net.Conn) *singleConnectionListener {
	listener := &singleConnectionListener{
		accepted: make(chan net.Conn, 1),
		closed:   make(chan struct{}),
	}
	listener.conn = &closingConn{Conn: conn, onClose: listener.Close}
	listener.accepted <- listener.conn
	return listener
}

// Accept returns the connection on the first call. Later calls block
// until the listener closes, then return net.ErrClosed.
func (listener *singleConnectionListener) Accept() (net.Conn, error) {
	select {
	case conn := <-listener.accepted:
		return conn, nil
	case <-listener.closed:
		return nil, net.ErrClosed
	}
}

// Close releases any blocked Accept calls. Does not close the
// connection itself. Safe to call multiple times.
func (listener *singleConnectionListener) Close() error {
	listener.closeOnce.Do(func() { close(listener.closed) })
	return nil
}

// Addr returns the local address of the wrapped connection.
func (listener *singleConnectionListener) Addr() net.Addr {
	return listener.conn.LocalAddr()
}

// closingConn runs a callback when the connection closes for the
// first time.
type closingConn struct {
	net.Conn
	onClose func() error
	once    sync.Once
}

// Close closes the connection and runs the close callback once.
func (conn *closingConn) Close() error {
	err := conn.Conn.Close()
	conn.once.Do(func() { conn.onClose() })
	return err
}

// CloseWrite half-closes the write side if supported.
func (conn *closingConn) CloseWrite() error {
	return closeWrite(conn.Conn)
}

// ServeConnection runs an HTTP server over a single connection until
// it closes. Takes an http.Server and a net.Conn. Returns nil on
// normal close, or the server's error.
func ServeConnection(server *http.Server, conn net.Conn) error {
	listener := newSingleConnectionListener(conn)
	err := server.Serve(listener)
	if errors.Is(err, net.ErrClosed) || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
