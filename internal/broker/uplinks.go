package broker

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	"prison/internal/broker/control"
)

const (
	uplinkDialTimeout       = 2 * time.Second
	uplinkRetryInterval     = 250 * time.Millisecond
	uplinkSlowRetryInterval = time.Second
	uplinkKeepAliveInterval = 15 * time.Second
	uplinkMaxStreams        = 512
)

type uplinkAddr string

func (a uplinkAddr) Network() string { return "uplink" }
func (a uplinkAddr) String() string  { return string(a) }

// yamux.Stream.Close only half-closes (FIN); Close also expires reads to unblock splices.
type yamuxConn struct {
	*yamux.Stream
	remoteAddr net.Addr
}

func newYamuxConn(stream *yamux.Stream, box string) *yamuxConn {
	return &yamuxConn{Stream: stream, remoteAddr: uplinkAddr(box)}
}

func (c *yamuxConn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

func (c *yamuxConn) CloseWrite() error {
	return c.Stream.Close()
}

func (c *yamuxConn) Close() error {
	_ = c.Stream.SetReadDeadline(time.Now())
	return c.Stream.Close()
}

type yamuxListener struct {
	session *yamux.Session
	box     string
}

func (l *yamuxListener) Accept() (net.Conn, error) {
	stream, err := l.session.AcceptStream()
	if err != nil {
		return nil, err
	}
	return newYamuxConn(stream, l.box), nil
}

func (l *yamuxListener) Close() error {
	return l.session.Close()
}

func (l *yamuxListener) Addr() net.Addr {
	return uplinkAddr(l.box)
}

func newYamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = uplinkKeepAliveInterval
	cfg.AcceptBacklog = uplinkMaxStreams
	return cfg
}

type boxUplink struct {
	box      string
	socket   string
	cancel   context.CancelFunc
	retryNow chan struct{}

	mutex     sync.Mutex
	connected bool
	lastError string
	session   *yamux.Session
	server    *http.Server
}

func (entry *boxUplink) state() control.Uplink {
	entry.mutex.Lock()
	defer entry.mutex.Unlock()
	connected := entry.connected && entry.session != nil && !entry.session.IsClosed()
	return control.Uplink{
		Box:       entry.box,
		Socket:    entry.socket,
		Connected: connected,
		Error:     entry.lastError,
	}
}

func (entry *boxUplink) activeSession() *yamux.Session {
	entry.mutex.Lock()
	defer entry.mutex.Unlock()
	if !entry.connected || entry.session == nil || entry.session.IsClosed() {
		return nil
	}
	return entry.session
}

func (entry *boxUplink) closeSession() {
	entry.mutex.Lock()
	server := entry.server
	session := entry.session
	entry.connected = false
	entry.server = nil
	entry.session = nil
	entry.mutex.Unlock()
	if server != nil {
		server.Close()
	}
	if session != nil {
		session.Close()
	}
}

type uplinkManager struct {
	mutex sync.Mutex
	byBox map[string]*boxUplink
}

func (broker *Broker) attachUplink(box, socket string) control.Uplink {
	broker.uplinks.mutex.Lock()
	if broker.uplinks.byBox == nil {
		broker.uplinks.byBox = map[string]*boxUplink{}
	}
	existing := broker.uplinks.byBox[box]
	if existing != nil && existing.socket == socket {
		broker.uplinks.mutex.Unlock()
		if existing.activeSession() != nil {
			return existing.state()
		}
		select {
		case existing.retryNow <- struct{}{}:
		default:
		}
		return existing.state()
	}
	var replaced *boxUplink
	if existing != nil {
		replaced = existing
		delete(broker.uplinks.byBox, box)
	}
	ctx, cancel := context.WithCancel(broker.runContext)
	entry := &boxUplink{
		box:      box,
		socket:   socket,
		cancel:   cancel,
		retryNow: make(chan struct{}, 1),
	}
	broker.uplinks.byBox[box] = entry
	broker.uplinks.mutex.Unlock()

	if replaced != nil {
		replaced.cancel()
		replaced.closeSession()
	}

	broker.persistUplinks()

	closedCh, connected := broker.dialUplink(entry)
	go broker.maintainUplink(ctx, entry, closedCh, connected)
	return entry.state()
}

func (broker *Broker) detachUplink(box string) {
	broker.uplinks.mutex.Lock()
	entry := broker.uplinks.byBox[box]
	delete(broker.uplinks.byBox, box)
	broker.uplinks.mutex.Unlock()
	if entry != nil {
		entry.cancel()
		entry.closeSession()
	}
	broker.persistUplinks()
}

func (broker *Broker) dialUplink(entry *boxUplink) (<-chan struct{}, bool) {
	conn, err := net.DialTimeout("unix", entry.socket, uplinkDialTimeout)
	if err != nil {
		entry.mutex.Lock()
		entry.lastError = err.Error()
		entry.mutex.Unlock()
		return nil, false
	}
	session, err := yamux.Server(conn, newYamuxConfig())
	if err != nil {
		conn.Close()
		entry.mutex.Lock()
		entry.lastError = err.Error()
		entry.mutex.Unlock()
		return nil, false
	}
	listener := &yamuxListener{session: session, box: entry.box}
	server := &http.Server{
		Handler:           http.HandlerFunc(broker.serveTCP),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       tunnelIdleTimeout,
		ErrorLog:          broker.quietLog,
		BaseContext: func(net.Listener) context.Context {
			return broker.runContext
		},
	}

	entry.mutex.Lock()
	entry.session = session
	entry.server = server
	entry.connected = true
	entry.lastError = ""
	entry.mutex.Unlock()

	closedCh := make(chan struct{})
	go func() {
		defer close(closedCh)
		err := server.Serve(listener)
		session.Close()
		entry.mutex.Lock()
		if entry.server == server {
			entry.connected = false
			entry.server = nil
			entry.session = nil
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				entry.lastError = err.Error()
			}
		}
		entry.mutex.Unlock()
	}()
	return closedCh, true
}

func (broker *Broker) maintainUplink(ctx context.Context, entry *boxUplink,
	closedCh <-chan struct{}, connected bool) {
	if connected {
		select {
		case <-ctx.Done():
			entry.closeSession()
			return
		case <-closedCh:
		}
	}
	attempts := 0
	for {
		delay := uplinkRetryInterval
		if attempts >= 20 {
			delay = uplinkSlowRetryInterval
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			entry.closeSession()
			return
		case <-entry.retryNow:
			timer.Stop()
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return
		}
		nextClosed, ok := broker.dialUplink(entry)
		if !ok {
			attempts++
			continue
		}
		attempts = 0
		select {
		case <-ctx.Done():
			entry.closeSession()
			return
		case <-nextClosed:
		}
	}
}

func (broker *Broker) openForward(box string, guestPort int) (net.Conn, error) {
	if guestPort < 1 || guestPort > 65535 {
		return nil, fmt.Errorf("guest port %d is out of range", guestPort)
	}
	broker.uplinks.mutex.Lock()
	entry := broker.uplinks.byBox[box]
	broker.uplinks.mutex.Unlock()
	if entry == nil {
		return nil, fmt.Errorf("no uplink registered for box %s", box)
	}
	session := entry.activeSession()
	if session == nil {
		return nil, fmt.Errorf("uplink for box %s is not connected", box)
	}
	stream, err := session.OpenStream()
	if err != nil {
		return nil, fmt.Errorf("cannot open uplink stream to %s: %w", box, err)
	}
	conn := newYamuxConn(stream, box)
	if err := conn.SetDeadline(time.Now().Add(forwardDialTimeout)); err != nil {
		conn.Close()
		return nil, err
	}
	var header [2]byte
	binary.BigEndian.PutUint16(header[:], uint16(guestPort))
	if _, err := conn.Write(header[:]); err != nil {
		conn.Close()
		return nil, fmt.Errorf("cannot request port %d on %s: %w", guestPort, box, err)
	}
	var status [1]byte
	if _, err := io.ReadFull(conn, status[:]); err != nil {
		conn.Close()
		return nil, fmt.Errorf("cannot read port forward reply from %s:%d: %w", box, guestPort, err)
	}
	if status[0] != 0 {
		conn.Close()
		return nil, fmt.Errorf("box %s refused connection to port %d", box, guestPort)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func (broker *Broker) restoreUplinks() {
	saved, err := broker.root.BrokerUplinks()
	if err != nil {
		broker.logger.Printf("cannot read saved broker uplinks: %v", err)
		return
	}
	if len(saved) == 0 {
		return
	}
	kept := make(map[string]string, len(saved))
	for box, socket := range saved {
		if _, err := os.Stat(socket); err != nil {
			broker.logger.Printf("skipping saved uplink %s: socket %s is absent", box, socket)
			continue
		}
		kept[box] = socket
		broker.attachUplink(box, socket)
	}
	if len(kept) != len(saved) {
		if err := broker.root.SaveBrokerUplinks(kept); err != nil {
			broker.logger.Printf("cannot update saved broker uplinks: %v", err)
		}
	}
}

func (broker *Broker) persistUplinks() {
	broker.uplinks.mutex.Lock()
	saved := make(map[string]string, len(broker.uplinks.byBox))
	for box, entry := range broker.uplinks.byBox {
		saved[box] = entry.socket
	}
	broker.uplinks.mutex.Unlock()
	if err := broker.root.SaveBrokerUplinks(saved); err != nil {
		broker.logger.Printf("cannot save broker uplinks: %v", err)
	}
}

func (broker *Broker) uplinkStates() []control.Uplink {
	broker.uplinks.mutex.Lock()
	entries := make([]*boxUplink, 0, len(broker.uplinks.byBox))
	for _, entry := range broker.uplinks.byBox {
		entries = append(entries, entry)
	}
	broker.uplinks.mutex.Unlock()
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].box < entries[j].box
	})
	states := make([]control.Uplink, 0, len(entries))
	for _, entry := range entries {
		states = append(states, entry.state())
	}
	return states
}

func (broker *Broker) closeUplinks() {
	broker.uplinks.mutex.Lock()
	entries := make([]*boxUplink, 0, len(broker.uplinks.byBox))
	for _, entry := range broker.uplinks.byBox {
		entries = append(entries, entry)
	}
	broker.uplinks.mutex.Unlock()
	for _, entry := range entries {
		entry.cancel()
		entry.closeSession()
	}
}
