package guest

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

const (
	uplinkKeepAliveInterval    = 15 * time.Second
	uplinkForwardDialTimeout   = 5 * time.Second
	uplinkForwardHeaderTimeout = 5 * time.Second
	uplinkMaxStreams           = 512
)

// yamux.Stream.Close only half-closes (FIN); Close also expires reads to unblock splices.
type uplinkStream struct {
	*yamux.Stream
}

func newUplinkStream(stream *yamux.Stream) *uplinkStream {
	return &uplinkStream{Stream: stream}
}

func (s *uplinkStream) CloseWrite() error {
	return s.Stream.Close()
}

func (s *uplinkStream) Close() error {
	_ = s.Stream.SetReadDeadline(time.Now())
	return s.Stream.Close()
}

type uplinkTransport struct {
	listener   net.Listener
	logger     *log.Logger
	dialTarget func(port int) (net.Conn, error)

	mutex   sync.Mutex
	closed  bool
	session *yamux.Session
	ready   chan struct{}
}

func startUplinkTransport(socketPath string, logger *log.Logger) (*uplinkTransport, error) {
	listener, err := listenUplinkSocket(socketPath)
	if err != nil {
		return nil, err
	}
	return newUplinkTransport(listener, logger), nil
}

func listenUplinkSocket(socketPath string) (net.Listener, error) {
	directory := filepath.Dir(socketPath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("cannot create %s: %w", directory, err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, fmt.Errorf("cannot secure %s: %w", directory, err)
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("cannot remove stale socket %s: %w", socketPath, err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("cannot listen on %s: %w", socketPath, err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		listener.Close()
		return nil, fmt.Errorf("cannot secure %s: %w", socketPath, err)
	}
	return listener, nil
}

func newUplinkTransport(listener net.Listener, logger *log.Logger) *uplinkTransport {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	transport := &uplinkTransport{
		listener: listener,
		logger:   logger,
		ready:    make(chan struct{}),
		dialTarget: func(port int) (net.Conn, error) {
			return net.DialTimeout("tcp4",
				net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
				uplinkForwardDialTimeout)
		},
	}
	go transport.acceptLoop()
	return transport
}

func (t *uplinkTransport) Close() error {
	t.mutex.Lock()
	t.closed = true
	session := t.session
	t.session = nil
	select {
	case <-t.ready:
	default:
		close(t.ready)
	}
	t.mutex.Unlock()
	if session != nil {
		_ = session.Close()
	}
	return t.listener.Close()
}

func (t *uplinkTransport) OpenStream(ctx context.Context) (net.Conn, error) {
	for {
		t.mutex.Lock()
		if t.closed {
			t.mutex.Unlock()
			return nil, errors.New("uplink transport is closed")
		}
		session := t.session
		wait := t.ready
		t.mutex.Unlock()

		if session != nil && !session.IsClosed() {
			stream, err := session.OpenStream()
			if err == nil {
				return newUplinkStream(stream), nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if !session.IsClosed() {
				return nil, err
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wait:
		}
	}
}

func (t *uplinkTransport) acceptLoop() {
	for {
		conn, err := t.listener.Accept()
		if err != nil {
			t.mutex.Lock()
			closed := t.closed
			t.mutex.Unlock()
			if closed || errors.Is(err, net.ErrClosed) {
				return
			}
			t.logger.Printf("uplink accept: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		cfg := yamux.DefaultConfig()
		cfg.LogOutput = io.Discard
		cfg.EnableKeepAlive = true
		cfg.KeepAliveInterval = uplinkKeepAliveInterval
		cfg.AcceptBacklog = uplinkMaxStreams
		session, err := yamux.Client(conn, cfg)
		if err != nil {
			conn.Close()
			t.logger.Printf("uplink yamux: %v", err)
			continue
		}
		t.replaceSession(session)
		go t.serveForwards(session)
	}
}

func (t *uplinkTransport) replaceSession(session *yamux.Session) {
	t.mutex.Lock()
	if t.closed {
		t.mutex.Unlock()
		_ = session.Close()
		return
	}
	previous := t.session
	t.session = session
	select {
	case <-t.ready:
	default:
		close(t.ready)
	}
	t.mutex.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
}

func (t *uplinkTransport) clearSession(session *yamux.Session) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	if t.closed || t.session != session {
		return
	}
	t.session = nil
	t.ready = make(chan struct{})
}

func (t *uplinkTransport) serveForwards(session *yamux.Session) {
	defer func() {
		t.clearSession(session)
		_ = session.Close()
	}()
	for {
		stream, err := session.AcceptStream()
		if err != nil {
			return
		}
		go t.handleForward(newUplinkStream(stream))
	}
}

func (t *uplinkTransport) handleForward(stream net.Conn) {
	if err := stream.SetDeadline(time.Now().Add(uplinkForwardHeaderTimeout)); err != nil {
		stream.Close()
		return
	}
	var header [2]byte
	if _, err := io.ReadFull(stream, header[:]); err != nil {
		stream.Close()
		return
	}
	port := int(binary.BigEndian.Uint16(header[:]))
	if port < 1 || port > 65535 {
		_, _ = stream.Write([]byte{1})
		stream.Close()
		return
	}
	target, err := t.dialTarget(port)
	if err != nil {
		_, _ = stream.Write([]byte{1})
		stream.Close()
		return
	}
	if _, err := stream.Write([]byte{0}); err != nil {
		target.Close()
		stream.Close()
		return
	}
	if err := stream.SetDeadline(time.Time{}); err != nil {
		target.Close()
		stream.Close()
		return
	}
	splice(stream, target, tunnelIdleTimeout)
}
