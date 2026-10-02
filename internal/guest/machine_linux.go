//go:build linux

package guest

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"golang.org/x/sys/unix"

	"prison/internal/machine"
)

const machineFlag = "--machine"

type kernelMount struct {
	source, target, filesystem, options string
	flags                               uintptr
}

const (
	noDevices    = unix.MS_NOSUID | unix.MS_NODEV
	noExecutable = noDevices | unix.MS_NOEXEC
)

// machineMounts are the mounts that a container runtime makes. In a
// microVM, `prison-guest` makes them as PID 1.
var machineMounts = []kernelMount{
	{"proc", "/proc", "proc", "", noExecutable},
	{"sysfs", "/sys", "sysfs", "", noExecutable},
	{"devtmpfs", "/dev", "devtmpfs", "mode=0755", unix.MS_NOSUID},
	{"devpts", "/dev/pts", "devpts", "gid=5,mode=0620,ptmxmode=0666",
		unix.MS_NOSUID | unix.MS_NOEXEC},
	{"tmpfs", "/dev/shm", "tmpfs", "mode=1777", noDevices},
	{"tmpfs", "/run", "tmpfs", "mode=0755", noDevices},
	{"cgroup2", "/sys/fs/cgroup", "cgroup2", "", noExecutable},
}

func bootMachine(runner *commandRunner,
	logger *log.Logger) (machine.Config, error) {
	config, err := prepareMachine(runner, logger)
	if err != nil {
		return config, err
	}
	if err := mountShares(config); err != nil {
		return config, err
	}
	return config, nil
}

func prepareMachine(runner *commandRunner,
	logger *log.Logger) (machine.Config, error) {
	var none machine.Config
	for _, mount := range machineMounts {
		if err := os.MkdirAll(mount.target, 0o755); err != nil {
			return none, fmt.Errorf("cannot create %s: %w", mount.target, err)
		}
		err := unix.Mount(mount.source, mount.target, mount.filesystem,
			mount.flags, mount.options)
		if err != nil && !errors.Is(err, unix.EBUSY) {
			return none, fmt.Errorf("cannot mount %s: %w", mount.target, err)
		}
	}
	disk, err := os.ReadFile(machine.ConfigDevice)
	if err != nil {
		return none, fmt.Errorf("cannot read %s: %w", machine.ConfigDevice, err)
	}
	config, err := machine.DecodeConfig(disk)
	if err != nil {
		return none, err
	}
	for _, assignment := range config.Environment {
		name, value, found := strings.Cut(assignment, "=")
		if found {
			os.Setenv(name, value)
		}
	}
	if os.Getenv("PATH") == "" {
		os.Setenv("PATH",
			"/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}
	if config.Hostname != "" {
		if err := unix.Sethostname([]byte(config.Hostname)); err != nil {
			return none, fmt.Errorf("cannot set the hostname: %w", err)
		}
		if err := writeHostsFile(config.Hostname); err != nil {
			logger.Printf("hosts: %v", err)
		}
	}
	if _, err := runner.run("ip", "link", "set", "lo", "up"); err != nil {
		return none, fmt.Errorf("cannot bring up the loopback: %w", err)
	}
	if len(config.Command) == 0 {
		return none, errors.New("the configuration disk has no command")
	}
	return config, nil
}

// mountShares mounts the shares in order so that a nested target is
// inside its parent.
func mountShares(config machine.Config) error {
	for _, share := range config.Mounts {
		if err := os.MkdirAll(share.Target, 0o755); err != nil {
			return fmt.Errorf("cannot create %s: %w", share.Target, err)
		}
		var flags uintptr = unix.MS_NOSUID | unix.MS_NODEV
		if share.ReadOnly {
			flags |= unix.MS_RDONLY
		}
		options := fmt.Sprintf("vers=4.2,proto=tcp,port=%d,addr=%s,hard",
			machine.NFSPort, config.NFSServer)
		source := config.NFSServer + ":" + share.Source
		err := unix.Mount(source, share.Target, "nfs4", flags, options)
		if err != nil {
			return fmt.Errorf("cannot mount %s on %s: %w", source,
				share.Target, err)
		}
	}
	return nil
}

func writeHostsFile(hostname string) error {
	content := "127.0.0.1 localhost\n::1 localhost\n127.0.1.1 " +
		hostname + "\n"
	return os.WriteFile("/etc/hosts", []byte(content), 0o644)
}

// powerOff ends the microVM because Firecracker exits when the guest
// powers off.
func powerOff(logger *log.Logger) {
	unix.Sync()
	if err := unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF); err != nil {
		logger.Printf("power off: %v", err)
	}
}

// serveAgent does not authenticate requests because only the host can
// connect to a guest vsock port.
func serveAgent(runner *commandRunner, logger *log.Logger,
	shutdown func()) error {
	listener, err := unix.Socket(unix.AF_VSOCK,
		unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("cannot create the agent socket: %w", err)
	}
	address := &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY,
		Port: machine.AgentPort}
	if err := unix.Bind(listener, address); err != nil {
		unix.Close(listener)
		return fmt.Errorf("cannot bind the agent port: %w", err)
	}
	if err := unix.Listen(listener, 16); err != nil {
		unix.Close(listener)
		return fmt.Errorf("cannot listen on the agent port: %w", err)
	}
	go func() {
		for {
			connection, _, err := unix.Accept4(listener, unix.SOCK_CLOEXEC)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil {
				logger.Printf("agent: %v", err)
				return
			}
			go handleAgentConnection(os.NewFile(uintptr(connection), "vsock"),
				runner, logger, shutdown)
		}
	}()
	return nil
}

// readRequestLine reads one byte at a time so that the frames after the
// line stay unread.
func readRequestLine(connection *os.File) ([]byte, error) {
	var line []byte
	buffer := make([]byte, 1)
	for len(line) < 1<<16 {
		if _, err := connection.Read(buffer); err != nil {
			return nil, err
		}
		if buffer[0] == '\n' {
			return line, nil
		}
		line = append(line, buffer[0])
	}
	return nil, errors.New("the request line is too long")
}

func handleAgentConnection(connection *os.File, runner *commandRunner,
	logger *log.Logger, shutdown func()) {
	defer connection.Close()
	line, err := readRequestLine(connection)
	if err != nil {
		return
	}
	var request machine.Request
	response := machine.Response{}
	if err := json.Unmarshal(line, &request); err != nil {
		response.Error = "the request is not valid JSON"
	}
	switch request.Operation {
	case machine.OperationExec:
		if request.Exec != nil && len(request.Exec.Command) > 0 {
			serveExec(connection, runner, *request.Exec, logger)
			return
		}
		response.Error = "the exec request has no command"
	case machine.OperationPing:
	case machine.OperationShutdown:
		defer shutdown()
	default:
		if response.Error == "" {
			response.Error = fmt.Sprintf("unknown operation %q",
				request.Operation)
		}
	}
	answer, _ := json.Marshal(response)
	if _, err := connection.Write(append(answer, '\n')); err != nil {
		logger.Printf("agent: %v", err)
	}
}
