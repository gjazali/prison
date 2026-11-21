package broker

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/net/netutil"

	"prison/internal/broker/control"
)

const (
	maxBoxConnections    = 512
	listenerProbeTimeout = 300 * time.Millisecond
)

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

type listenerManager struct {
	mutex     sync.Mutex
	byAddress map[string]*tcpListener
	order     []string
}

func (broker *Broker) getOrCreateListener(address string) *tcpListener {
	broker.listeners.mutex.Lock()
	defer broker.listeners.mutex.Unlock()
	entry, exists := broker.listeners.byAddress[address]
	if !exists {
		entry = &tcpListener{address: address}
		broker.listeners.byAddress[address] = entry
		broker.listeners.order = append(broker.listeners.order, address)
	}
	return entry
}

func (broker *Broker) listen(address string) control.Listener {
	entry := broker.getOrCreateListener(address)

	entry.mutex.Lock()
	bound := entry.bound
	binding := entry.binding
	if !bound && !binding {
		entry.binding = true
	}
	entry.mutex.Unlock()

	if binding {
		return entry.state()
	}
	if bound {
		return broker.ensureListenerHealthy(entry, true)
	}
	if broker.tryBind(entry) {
		return entry.state()
	}
	go broker.retryBind(entry)
	return entry.state()
}

// ensureListenerHealthy probes a bound listener and closes + rebinds it if
// the underlying socket or host interface is no longer healthy.
func (broker *Broker) ensureListenerHealthy(entry *tcpListener, retryOnFailure bool) control.Listener {
	entry.mutex.Lock()
	bound := entry.bound
	port := entry.port
	server := entry.server
	entry.mutex.Unlock()
	if !bound || port == 0 {
		return entry.state()
	}
	probe := broker.probeListener
	if probe == nil {
		probe = defaultProbeListener
	}
	err := probe(entry.address, port)
	if err == nil {
		return entry.state()
	}

	entry.mutex.Lock()
	if !entry.bound || entry.server != server {
		entry.mutex.Unlock()
		return entry.state()
	}
	entry.bound = false
	entry.binding = true
	entry.listener = nil
	entry.server = nil
	entry.lastError = err.Error()
	entry.mutex.Unlock()

	if server != nil {
		server.Close()
	}
	broker.logger.Printf("listener %s:%d failed healthcheck (%v); rebinding",
		entry.address, port, err)
	if broker.tryBind(entry) {
		return entry.state()
	}
	if retryOnFailure {
		go broker.retryBind(entry)
	} else {
		entry.mutex.Lock()
		entry.binding = false
		entry.mutex.Unlock()
	}
	return entry.state()
}

func (broker *Broker) tryBind(entry *tcpListener) bool {
	hostPort := net.JoinHostPort(entry.address, strconv.Itoa(broker.options.BrokerPort))
	listener, err := net.Listen("tcp", hostPort)
	entry.mutex.Lock()
	if err != nil {
		entry.lastError = err.Error()
		entry.mutex.Unlock()
		return false
	}
	listener = netutil.LimitListener(listener, maxBoxConnections)
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
	entry.mutex.Unlock()
	broker.persistListeners()
	go broker.serveListener(entry, server, listener)
	return true
}

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

func (broker *Broker) restoreListeners() {
	addresses, err := broker.root.BrokerListeners()
	if err != nil {
		broker.logger.Printf("cannot read saved broker listeners: %v", err)
		return
	}
	if len(addresses) == 0 {
		return
	}
	kept := make([]string, 0, len(addresses))
	for _, address := range addresses {
		if !addressIsLocalOrUnspecified(address) {
			broker.logger.Printf("skipping saved listener %s: address is not on any host interface", address)
			continue
		}
		entry := broker.getOrCreateListener(address)
		entry.mutex.Lock()
		entry.binding = true
		entry.mutex.Unlock()
		if broker.tryBind(entry) {
			kept = append(kept, address)
		} else {
			go broker.retryBind(entry)
			kept = append(kept, address)
		}
	}
	if err := broker.root.SaveBrokerListeners(kept); err != nil {
		broker.logger.Printf("cannot update saved broker listeners: %v", err)
	}
}

func (broker *Broker) persistListeners() {
	broker.listeners.mutex.Lock()
	addresses := append([]string(nil), broker.listeners.order...)
	broker.listeners.mutex.Unlock()
	if err := broker.root.SaveBrokerListeners(addresses); err != nil {
		broker.logger.Printf("cannot save broker listeners: %v", err)
	}
}

func (broker *Broker) listenerStates() []control.Listener {
	broker.listeners.mutex.Lock()
	entries := make([]*tcpListener, 0, len(broker.listeners.order))
	for _, address := range broker.listeners.order {
		entries = append(entries, broker.listeners.byAddress[address])
	}
	broker.listeners.mutex.Unlock()
	states := make([]control.Listener, 0, len(entries))
	for _, entry := range entries {
		states = append(states, broker.ensureListenerHealthy(entry, false))
	}
	return states
}

func defaultProbeListener(address string, port int) error {
	if !addressIsLocalOrUnspecified(address) {
		return errors.New("address is not on any host interface")
	}
	dialAddress := address
	if ip := net.ParseIP(address); ip != nil && ip.IsUnspecified() {
		if ip.To4() != nil {
			dialAddress = "127.0.0.1"
		} else {
			dialAddress = "::1"
		}
	}
	conn, err := net.DialTimeout("tcp",
		net.JoinHostPort(dialAddress, strconv.Itoa(port)), listenerProbeTimeout)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

func addressIsLocalOrUnspecified(address string) bool {
	target := net.ParseIP(address)
	if target == nil {
		return true
	}
	if target.IsUnspecified() {
		return true
	}
	interfaceAddrs, err := net.InterfaceAddrs()
	if err != nil {
		return true
	}
	for _, candidate := range interfaceAddrs {
		var ip net.IP
		switch value := candidate.(type) {
		case *net.IPNet:
			ip = value.IP
		case *net.IPAddr:
			ip = value.IP
		}
		if ip != nil && ip.Equal(target) {
			return true
		}
	}
	return false
}

// closeListeners does not close hijacked tunnels. They end with the
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
