// Package broker runs the host-side daemon. It authenticates
// connections from boxes, enforces egress rules, serves the
// prison.internal zone, injects credentials, signs payloads, and
// answers CLI commands over a Unix socket.
package broker

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"prison/internal/broker/brokerlog"
	"prison/internal/broker/ca"
	"prison/internal/broker/limits"
	"prison/internal/broker/relay"
	"prison/internal/broker/signing"
	"prison/internal/state"
	"prison/internal/vault"
)

// Timing constants for the daemon.
const (
	pollInterval                = 2 * time.Second
	tunnelIdleTimeout           = 300 * time.Second
	connectDialTimeout          = 15 * time.Second
	inmateResponseHeaderTimeout = 900 * time.Second
	routeResponseHeaderTimeout  = 300 * time.Second
	readHeaderTimeout           = 30 * time.Second
	controlReadHeaderTimeout    = 10 * time.Second
	shutdownGrace               = 5 * time.Second
	bindRetryInterval           = time.Second
	bindAttempts                = 30
)

// Options holds the settings for a single broker. Root is required.
// All other fields have sensible defaults when left at their zero
// value.
type Options struct {
	Root              *state.Root
	Version           string
	BrokerPort        int
	SSHAuthSocket     string
	Confirmer         limits.Confirmer
	Now               func() time.Time
	UpstreamTLSConfig *tls.Config
	Logger            *log.Logger
}

// Broker is the running daemon's state. Run creates it and it lives
// until Run returns.
type Broker struct {
	options    Options
	root       *state.Root
	logger     *log.Logger
	quietLog   *log.Logger
	now        func() time.Time
	runContext context.Context

	requestLog *brokerlog.Writer

	projects       atomic.Pointer[projectTable]
	floor          atomic.Pointer[floorState]
	reloadMutex    sync.Mutex
	lastLoadErrors map[string]string

	credentialsMutex sync.Mutex
	credentials      map[string]string

	vaultMutex  sync.Mutex
	vault       *vault.Vault
	authorities *ca.Authorities

	listeners listenerManager

	limiter   *limits.RateLimiter
	confirmer limits.Confirmer
	agent     *signing.Agent

	inmateTransport *http.Transport
	routeTransport  *http.Transport

	shutdownOnce      sync.Once
	shutdownRequested chan struct{}
}

// Run starts the broker and serves until ctx is cancelled, a signal
// arrives, or POST /shutdown is called. It takes a context and an
// Options value. It returns nil on a clean shutdown, or an error if
// the broker could not start.
func Run(ctx context.Context, options Options) error {
	if options.Root == nil {
		return errors.New("the broker needs a state root to run against")
	}
	ctx, stopSignals := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stopSignals()
	broker := newBroker(ctx, options)

	if err := broker.root.SaveBrokerPID(os.Getpid()); err != nil {
		return err
	}
	defer broker.removePIDFile()

	requestLog, err := brokerlog.OpenWriter(broker.root.BrokerLog(), 0, 0)
	if err != nil {
		return err
	}
	broker.requestLog = requestLog
	defer requestLog.Close()

	socketListener, err := broker.listenControlSocket()
	if err != nil {
		return err
	}
	var removeSocket sync.Once
	unlinkSocket := func() {
		removeSocket.Do(func() { os.Remove(broker.root.BrokerSocket()) })
	}
	defer unlinkSocket()

	broker.reloadFloor()
	if err := broker.reloadAll(); err != nil {
		broker.logger.Print(err)
	}

	controlServer := &http.Server{
		Handler:           broker.controlHandler(),
		ReadHeaderTimeout: controlReadHeaderTimeout,
		ErrorLog:          broker.quietLog,
	}
	serveFailure := make(chan error, 1)
	go func() {
		err := controlServer.Serve(socketListener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveFailure <- err
		}
	}()
	go broker.poll(ctx)

	var failure error
	select {
	case <-ctx.Done():
	case <-broker.shutdownRequested:
	case err := <-serveFailure:
		failure = fmt.Errorf("the control socket stopped serving: %w", err)
	}

	// Remove the socket before the deferred cleanup, so a replacement
	// broker can bind without a race.
	unlinkSocket()
	graceContext, cancelGrace := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancelGrace()
	if err := controlServer.Shutdown(graceContext); err != nil {
		controlServer.Close()
	}
	broker.closeListeners()
	broker.lockVault()
	broker.inmateTransport.CloseIdleConnections()
	broker.routeTransport.CloseIdleConnections()
	return failure
}

// newBroker creates a Broker from options with defaults filled in.
// It takes a context and an Options value. It returns a ready Broker
// but does not touch the filesystem.
func newBroker(ctx context.Context, options Options) *Broker {
	logger := options.Logger
	if logger == nil {
		logger = log.New(os.Stderr, "prison-broker: ", log.LstdFlags)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	confirmer := options.Confirmer
	if confirmer == nil {
		confirmer = limits.NewDialogConfirmer()
	}
	broker := &Broker{
		options:           options,
		root:              options.Root,
		logger:            logger,
		quietLog:          log.New(io.Discard, "", 0),
		now:               now,
		runContext:        ctx,
		lastLoadErrors:    map[string]string{},
		credentials:       map[string]string{},
		limiter:           limits.NewRateLimiter(now),
		confirmer:         confirmer,
		agent:             signing.NewAgent(options.SSHAuthSocket),
		inmateTransport:   relay.NewTransport(inmateResponseHeaderTimeout, options.UpstreamTLSConfig),
		routeTransport:    relay.NewTransport(routeResponseHeaderTimeout, options.UpstreamTLSConfig),
		shutdownRequested: make(chan struct{}),
	}
	broker.listeners.byAddress = map[string]*tcpListener{}
	broker.projects.Store(&projectTable{byID: map[string]*projectSnapshot{}})
	broker.floor.Store(&floorState{})
	return broker
}

// listenControlSocket binds the Unix socket at mode 0600. It removes
// a stale socket file, but returns an error if another broker is
// already listening. It returns the listener or an error.
func (broker *Broker) listenControlSocket() (net.Listener, error) {
	socketPath := broker.root.BrokerSocket()
	if _, err := os.Lstat(socketPath); err == nil {
		if probe, err := net.DialTimeout("unix", socketPath, time.Second); err == nil {
			probe.Close()
			return nil, fmt.Errorf("another broker is already listening on %s; "+
				"`prison broker stop` ends it", socketPath)
		}
		if err := os.Remove(socketPath); err != nil {
			return nil, fmt.Errorf("cannot remove the stale socket %s: %w",
				socketPath, err)
		}
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("cannot listen on %s: %w", socketPath, err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		listener.Close()
		return nil, fmt.Errorf("cannot set the mode of %s: %w", socketPath, err)
	}
	return listener, nil
}

// removePIDFile deletes the pid file only if it still belongs to this
// process.
func (broker *Broker) removePIDFile() {
	if pid, ok := broker.root.BrokerPID(); ok && pid == os.Getpid() {
		os.Remove(broker.root.BrokerPIDFile())
	}
}

// requestShutdown signals Run to return. Safe to call more than once.
func (broker *Broker) requestShutdown() {
	broker.shutdownOnce.Do(func() { close(broker.shutdownRequested) })
}

// poll checks for state changes every pollInterval until ctx ends.
func (broker *Broker) poll(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			broker.pollOnce()
		}
	}
}

// record appends an entry to the request log. Write failures are sent
// to the operational logger.
func (broker *Broker) record(entry brokerlog.Entry) {
	if entry.Time.IsZero() {
		entry.Time = broker.now()
	}
	if err := broker.requestLog.Write(entry); err != nil {
		broker.logger.Print(err)
	}
}

// elapsedMilliseconds returns the milliseconds elapsed since started.
func elapsedMilliseconds(started time.Time) int64 {
	return time.Since(started).Milliseconds()
}
