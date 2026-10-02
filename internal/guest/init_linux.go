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
	"prison/internal/machine"
)

const sudoersPath = "/etc/sudoers.d/prison"

func runInit(arguments []string, logger *log.Logger) int {
	// Subscribe before any fork so that no `SIGCHLD` is lost.
	signals := make(chan os.Signal, 64)
	signal.Notify(signals, unix.SIGCHLD, unix.SIGTERM, unix.SIGINT)
	runner := &commandRunner{}
	if len(arguments) > 0 && arguments[0] == machineFlag {
		code := runMachineInit(runner, signals, logger)
		powerOff(logger)
		return code
	}
	return runContainerInit(arguments, nil, runner, signals, logger)
}

func runMachineInit(runner *commandRunner, signals chan os.Signal,
	logger *log.Logger) int {
	config, err := bootMachine(runner, logger)
	if err != nil {
		logger.Printf("%v", err)
		return 1
	}
	var gatewayPorts []int
	if len(config.Mounts) > 0 {
		gatewayPorts = append(gatewayPorts, machine.NFSPort)
	}
	stop := func() { _ = unix.Kill(os.Getpid(), unix.SIGTERM) }
	if err := serveAgent(runner, logger, stop); err != nil {
		logger.Printf("%v", err)
		return 1
	}
	return runContainerInit(config.Command, gatewayPorts, runner, signals,
		logger)
}

func runContainerInit(arguments []string, gatewayPorts []int,
	runner *commandRunner, signals chan os.Signal, logger *log.Logger) int {
	configuration, err := readInitConfiguration(arguments, os.LookupEnv)
	if err != nil {
		logger.Printf("%v", err)
		return 2
	}

	gateway, err := defaultGateway(runner)
	if err != nil {
		logger.Printf("cannot find the default route: %v", err)
		return 1
	}
	err = installFirewall(runner, gateway, configuration.brokerPort,
		gatewayPorts)
	if err != nil {
		logger.Printf("cannot install the firewall: %v", err)
		return 1
	}
	if err := proveEgressClosed(); err != nil {
		logger.Printf("%v", err)
		return 1
	}
	logger.Printf("egress restricted to %s port %d", gateway,
		configuration.brokerPort)

	dev := lookupDevIdentity()
	if err := configureSudo(runner, configuration.sudo); err != nil {
		logger.Printf("sudo: %v", err)
	}
	for _, path := range configuration.persistPaths {
		if err := ownByDev(path, dev); err != nil {
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
	nameServer := &resolver{sentinels: sentinels, allowList: allowList,
		logger: logger}
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
				logger.Printf("cannot forward %s: %v", received, err)
			}
		case err := <-fatal:
			logger.Printf("stopping the box: %v", err)
			if child != nil {
				_ = child.Process.Signal(unix.SIGTERM)
			}
			return 1
		}
	}
}

// spawnAsDev starts the command as `dev`. Callers must not call `Wait`
// because the reaper collects the exit status.
func spawnAsDev(command []string, dev devIdentity,
	environment []string) (*exec.Cmd, error) {
	child := exec.Command(command[0], command[1:]...)
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	child.Env = childEnvironment(environment, dev)
	// An empty `Groups` clears the supplementary groups. A nil value keeps
	// the groups of root.
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
			runner.deliverExit(pid, exitCode(status))
		}
	})
	return code, done
}

func exitCode(status unix.WaitStatus) int {
	if status.Exited() {
		return status.ExitStatus()
	}
	if status.Signaled() {
		return 128 + int(status.Signal())
	}
	return 1
}

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
		return errors.New("the sudo rule is not valid")
	}
	return nil
}

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

// ownByDev skips a path that `dev` owns because `chown` fails on an NFS
// share, where the files belong to the host user.
func ownByDev(path string, dev devIdentity) error {
	var status unix.Stat_t
	if err := unix.Stat(path, &status); err != nil {
		return err
	}
	if int(status.Uid) == dev.uid && int(status.Gid) == dev.gid {
		return nil
	}
	return os.Chown(path, dev.uid, dev.gid)
}
