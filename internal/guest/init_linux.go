//go:build linux

package guest

import (
	"context"
	"errors"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"

	"prison/internal/broker/protocol"
)

// sudoersPath is where the sudo rule for the dev user is stored.
const sudoersPath = "/etc/sudoers.d/prison"

// runInit is the PID 1 entry point. It confines the box, starts the
// resolver and tunnel, waits for the broker, and runs the command as
// dev. It takes the command arguments and a logger. Returns the exit
// status: 0 for success, 1 for failure, 2 for bad usage.
func runInit(arguments []string, logger *log.Logger) int {
	configuration, err := readInitConfiguration(arguments, os.LookupEnv)
	if err != nil {
		logger.Printf("%v", err)
		return 2
	}
	// Subscribe to signals before forking so no SIGCHLD is lost.
	signals := make(chan os.Signal, 64)
	signal.Notify(signals, unix.SIGCHLD, unix.SIGTERM, unix.SIGINT)
	runner := &commandRunner{}

	gateway, err := defaultGateway(runner)
	if err != nil {
		logger.Printf("no default route, refusing to run unconfined: %v", err)
		return 1
	}
	err = installFirewall(runner, gateway, configuration.brokerPort)
	if err != nil {
		logger.Printf("firewall setup failed, refusing to start: %v", err)
		return 1
	}
	if err := proveEgressClosed(); err != nil {
		logger.Printf("%v; refusing to start", err)
		return 1
	}
	logger.Printf("egress restricted to %s port %d", gateway,
		configuration.brokerPort)

	dev := lookupDevIdentity()
	if err := configureSudo(runner, configuration.sudo); err != nil {
		logger.Printf("sudo: %v", err)
	}
	for _, path := range configuration.persistPaths {
		if err := os.Chown(path, dev.uid, dev.gid); err != nil {
			logger.Printf("persist: %v", err)
		}
	}
	if err := installShims(dev); err != nil {
		logger.Printf("shims: %v", err)
	}

	sentinels := newSentinelAllocator()
	allowList := newAllowListHolder()
	dialer := &brokerDialer{
		brokerAddress: net.JoinHostPort(gateway.String(),
			strconv.Itoa(configuration.brokerPort)),
		token:   configuration.token,
		timeout: brokerDialTimeout,
	}
	resolverConn, err := net.ListenPacket("udp4",
		net.JoinHostPort(resolverAddress, strconv.Itoa(resolverPort)))
	if err != nil {
		logger.Printf("resolver: %v", err)
		return 1
	}
	tunnelListener, err := net.Listen("tcp4",
		net.JoinHostPort("127.0.0.1", strconv.Itoa(tunnelPort)))
	if err != nil {
		logger.Printf("tunnel: %v", err)
		return 1
	}
	fatal := make(chan error, 2)
	nameServer := &resolver{sentinels: sentinels, allowList: allowList}
	go func() { fatal <- nameServer.serve(resolverConn) }()
	tunnelServer := &tunnel{
		sentinels:     sentinels,
		allowList:     allowList,
		dialer:        dialer,
		idleTimeout:   tunnelIdleTimeout,
		destinationOf: originalDestination,
		logger:        logger,
	}
	go func() { fatal <- tunnelServer.serve(tunnelListener) }()
	if err := os.WriteFile(resolvConfPath, []byte(resolvConfContent()),
		0o644); err != nil {
		logger.Printf("resolv.conf: %v", err)
		return 1
	}

	refresher := &brokerRefresher{
		client:    dialer.httpClient(fetchTimeout),
		baseURL:   "http://" + protocol.BrokerHost,
		allowList: allowList,
		certificates: &certificateInstaller{
			directory: certificateDirectory,
			runner:    runner,
			logger:    logger,
		},
		logger: logger,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	brokerAnswered := make(chan struct{})
	go refresher.run(ctx, brokerAnswered)

	return supervise(configuration.command, dev, runner, signals, fatal,
		brokerAnswered, logger)
}

// supervise waits for the broker, spawns the command as dev, then
// handles signals until the command exits. Returns the exit status.
func supervise(command []string, dev devIdentity, runner *commandRunner,
	signals <-chan os.Signal, fatal <-chan error,
	brokerAnswered <-chan struct{}, logger *log.Logger) int {
	var child *exec.Cmd
	for {
		select {
		case <-brokerAnswered:
			brokerAnswered = nil
			started, err := spawnAsDev(command, dev, os.Environ())
			if err != nil {
				logger.Printf("cannot start %q: %v", command, err)
				return 1
			}
			child = started
		case received := <-signals:
			unixSignal, _ := received.(syscall.Signal)
			if unixSignal == unix.SIGCHLD {
				childPid := 0
				if child != nil {
					childPid = child.Process.Pid
				}
				if code, done := reapChildren(runner, childPid); done {
					return code
				}
				continue
			}
			if child == nil {
				return 128 + int(unixSignal)
			}
			if err := child.Process.Signal(received); err != nil {
				logger.Printf("forwarding %s: %v", received, err)
			}
		case err := <-fatal:
			logger.Printf("%v; stopping the box", err)
			if child != nil {
				_ = child.Process.Signal(unix.SIGTERM)
			}
			return 1
		}
	}
}

// spawnAsDev starts command as the dev user. It takes the command,
// the dev identity, and the environment. Returns the started process.
// The caller must not call Wait on the result; the reaper collects
// the exit status.
func spawnAsDev(command []string, dev devIdentity,
	environment []string) (*exec.Cmd, error) {
	child := exec.Command(command[0], command[1:]...)
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	child.Env = childEnvironment(environment, dev)
	// An empty Groups slice clears supplementary groups; nil would
	// keep root's.
	child.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid:    uint32(dev.uid),
			Gid:    uint32(dev.gid),
			Groups: []uint32{},
		},
	}
	if err := child.Start(); err != nil {
		return nil, err
	}
	return child, nil
}

// reapChildren collects all exited children. If the main child
// (identified by mainPid) exited, it returns the exit code and true.
func reapChildren(runner *commandRunner, mainPid int) (int, bool) {
	code, done := 0, false
	runner.exclusively(func() {
		for {
			var status unix.WaitStatus
			pid, err := unix.Wait4(-1, &status, unix.WNOHANG, nil)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil || pid <= 0 {
				return
			}
			if pid == mainPid {
				code, done = exitCode(status), true
			}
		}
	})
	return code, done
}

// exitCode converts a wait status to a shell-style exit code.
func exitCode(status unix.WaitStatus) int {
	if status.Exited() {
		return status.ExitStatus()
	}
	if status.Signaled() {
		return 128 + int(status.Signal())
	}
	return 1
}

// configureSudo writes or removes the passwordless sudo rule for dev.
// It takes a runner and whether sudo is enabled. Returns an error on
// failure.
func configureSudo(runner *commandRunner, enabled bool) error {
	if !enabled {
		if err := os.Remove(sudoersPath); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	rule := devUserName + " ALL=(ALL) NOPASSWD: ALL\n"
	if err := os.MkdirAll(filepath.Dir(sudoersPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(sudoersPath, []byte(rule), 0o440); err != nil {
		return err
	}
	if err := os.Chmod(sudoersPath, 0o440); err != nil {
		return err
	}
	if _, err := exec.LookPath("visudo"); err != nil {
		return nil
	}
	if _, err := runner.run("visudo", "-cf", sudoersPath); err != nil {
		_ = os.Remove(sudoersPath)
		return errors.New("the sudo rule did not parse, so it was not" +
			" installed")
	}
	return nil
}

// installShims creates symlinks for prison-sign and prison-ssh-sign
// in the dev user's shim directory. Returns an error on failure.
func installShims(dev devIdentity) error {
	if err := os.MkdirAll(shimDirectory, 0o755); err != nil {
		return err
	}
	for _, directory := range []string{filepath.Dir(shimDirectory),
		shimDirectory} {
		if err := os.Chown(directory, dev.uid, dev.gid); err != nil {
			return err
		}
	}
	for _, name := range []string{"prison-sign", "prison-ssh-sign"} {
		link := filepath.Join(shimDirectory, name)
		if err := os.Remove(link); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Symlink(guestBinaryPath, link); err != nil {
			return err
		}
		if err := os.Lchown(link, dev.uid, dev.gid); err != nil {
			return err
		}
	}
	return nil
}
