package broker

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"prison/internal/broker/control"
)

// tcpListener tracks the state of one address the broker was asked to
// bind.
type tcpListener struct {
	address   string
	mutex     sync.Mutex
	bound     bool
	binding   bool
	port      int
	lastError string
	listener  net.Listener
	server    *http.Server
}

// state returns the listener's current status for reporting.
func (entry *tcpListener) state() control.Listener {
	entry.mutex.Lock()
	defer entry.mutex.Unlock()
	return control.Listener{
		Address: entry.address,
		Port:    entry.port,
		Bound:   entry.bound,
		Error:   entry.lastError,
	}
}

// listenerManager tracks all listeners by address in request order.
type listenerManager struct {
	mutex     sync.Mutex
	byAddress map[string]*tcpListener
	order     []string
}

// listen ensures a listener exists for address and starts binding it.
// It tries once before returning, then retries in the background on
// failure. It returns the state after the first attempt.
func (broker *Broker) listen(address string) control.Listener {
	broker.listeners.mutex.Lock()
	entry, exists := broker.listeners.byAddress[address]
	if !exists {
		entry = &tcpListener{address: address}
		broker.listeners.byAddress[address] = entry
		broker.listeners.order = append(broker.listeners.order, address)
	}
	broker.listeners.mutex.Unlock()

	entry.mutex.Lock()
	if entry.bound || entry.binding {
		entry.mutex.Unlock()
		return entry.state()
	}
	entry.binding = true
	entry.mutex.Unlock()

	if broker.tryBind(entry) {
		return entry.state()
	}
	go broker.retryBind(entry)
	return entry.state()
}

// tryBind makes one attempt to bind entry's address on the broker port
// and, on success, starts serving it. It reports whether the bind
// succeeded and records the error when it did not.
func (broker *Broker) tryBind(entry *tcpListener) bool {
	hostPort := net.JoinHostPort(entry.address, strconv.Itoa(broker.options.BrokerPort))
	listener, err := net.Listen("tcp", hostPort)
	entry.mutex.Lock()
	defer entry.mutex.Unlock()
	if err != nil {
		entry.lastError = err.Error()
		return false
	}
	server := &http.Server{
		Handler:           http.HandlerFunc(broker.serveTCP),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       tunnelIdleTimeout,
		ErrorLog:          broker.quietLog,
		BaseContext: func(net.Listener) context.Context {
			return broker.runContext
		},
	}
	entry.listener = listener
	entry.server = server
	entry.bound = true
	entry.binding = false
	entry.lastError = ""
	if address, ok := listener.Addr().(*net.TCPAddr); ok {
		entry.port = address.Port
	}
	go broker.serveListener(entry, server, listener)
	return true
}

// serveListener runs server on listener until it stops and then marks
// the entry unbound, keeping the error unless the server was closed on
// purpose.
func (broker *Broker) serveListener(entry *tcpListener, server *http.Server, listener net.Listener) {
	err := server.Serve(listener)
	entry.mutex.Lock()
	defer entry.mutex.Unlock()
	if entry.server != server {
		return
	}
	entry.bound = false
	entry.listener = nil
	entry.server = nil
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		entry.lastError = err.Error()
	}
}

// retryBind keeps trying to bind entry once a second until it succeeds,
// the attempts run out, or the broker stops.
func (broker *Broker) retryBind(entry *tcpListener) {
	for attempt := 2; attempt <= bindAttempts; attempt++ {
		select {
		case <-broker.runContext.Done():
			break
		case <-time.After(bindRetryInterval):
			if broker.tryBind(entry) {
				return
			}
			continue
		}
		break
	}
	entry.mutex.Lock()
	entry.binding = false
	entry.mutex.Unlock()
}

// listenerStates renders every listener in request order.
func (broker *Broker) listenerStates() []control.Listener {
	broker.listeners.mutex.Lock()
	defer broker.listeners.mutex.Unlock()
	states := make([]control.Listener, 0, len(broker.listeners.order))
	for _, address := range broker.listeners.order {
		states = append(states, broker.listeners.byAddress[address].state())
	}
	return states
}

// closeListeners closes every bound listener and its connections.
// Hijacked tunnels are not tracked by the servers and end with the
// process.
func (broker *Broker) closeListeners() {
	broker.listeners.mutex.Lock()
	entries := make([]*tcpListener, 0, len(broker.listeners.order))
	for _, address := range broker.listeners.order {
		entries = append(entries, broker.listeners.byAddress[address])
	}
	broker.listeners.mutex.Unlock()
	for _, entry := range entries {
		entry.mutex.Lock()
		server := entry.server
		entry.mutex.Unlock()
		if server != nil {
			server.Close()
		}
	}
}
