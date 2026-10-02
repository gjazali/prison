//go:build linux

package guest

import (
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"prison/internal/console"
	"prison/internal/machine"
)

// outputDrainTimeout bounds the wait for output after the command exits.
// A background process can keep the output open forever.
const outputDrainTimeout = 2 * time.Second

type execSession struct {
	connection *os.File
	writeLock  sync.Mutex
	logger     *log.Logger
}

func (session *execSession) send(kind byte, payload []byte) error {
	session.writeLock.Lock()
	defer session.writeLock.Unlock()
	return machine.WriteFrame(session.connection, kind, payload)
}

func serveExec(connection *os.File, runner *commandRunner,
	request machine.ExecRequest, logger *log.Logger) {
	session := &execSession{connection: connection, logger: logger}
	environment := execEnvironment(os.Environ(), request)
	path, err := lookPathIn(request.Command[0], environmentValue(
		environment, "PATH"))
	if err != nil {
		session.send(machine.FrameFailure, []byte(err.Error()))
		return
	}
	process := exec.Command(path, request.Command[1:]...)
	process.Args[0] = request.Command[0]
	process.Env = environment
	process.Dir = request.WorkDir
	process.SysProcAttr = &syscall.SysProcAttr{}
	if os.Geteuid() == 0 {
		process.SysProcAttr.Credential = &syscall.Credential{
			Uid:    uint32(request.UID),
			Gid:    uint32(request.GID),
			Groups: []uint32{},
		}
	}
	var streams *execStreams
	if request.TTY {
		streams, err = ttyStreams(process, request)
	} else {
		streams, err = pipeStreams(process)
	}
	if err != nil {
		session.send(machine.FrameFailure, []byte(err.Error()))
		return
	}
	exited, err := runner.startOwned(process)
	streams.closeChildEnds()
	if err != nil {
		streams.closeParentEnds()
		session.send(machine.FrameFailure, []byte(err.Error()))
		return
	}
	pid := process.Process.Pid
	drained := streams.copyOutput(session)
	var finished atomic.Bool
	go session.copyInput(streams, pid, request.TTY, &finished)
	code := <-exited
	finished.Store(true)
	select {
	case <-drained:
	case <-time.After(outputDrainTimeout):
		streams.closeParentEnds()
		<-drained
	}
	session.send(machine.FrameExit, machine.ExitPayload(code))
	streams.closeParentEnds()
}

// execStreams holds both ends of the standard streams. With a TTY, `input`
// and `output` are the same pty multiplexer.
type execStreams struct {
	input, output, errors *os.File
	childEnds             []*os.File
	terminal              *os.File
	closeOnce             sync.Once
}

func ttyStreams(process *exec.Cmd,
	request machine.ExecRequest) (*execStreams, error) {
	multiplexer, device, err := console.OpenTerminalPair()
	if err != nil {
		return nil, err
	}
	if request.Rows > 0 && request.Columns > 0 {
		console.SetSize(int(multiplexer.Fd()), request.Rows, request.Columns)
	}
	process.Stdin, process.Stdout, process.Stderr = device, device, device
	process.SysProcAttr.Setsid = true
	process.SysProcAttr.Setctty = true
	return &execStreams{
		input:     multiplexer,
		output:    multiplexer,
		childEnds: []*os.File{device},
		terminal:  multiplexer,
	}, nil
}

func pipeStreams(process *exec.Cmd) (*execStreams, error) {
	streams := &execStreams{}
	var pipes [3][2]*os.File
	for index := range pipes {
		reader, writer, err := os.Pipe()
		if err != nil {
			streams.closeChildEnds()
			streams.closeParentEnds()
			return nil, err
		}
		pipes[index] = [2]*os.File{reader, writer}
	}
	process.Stdin, streams.input = pipes[0][0], pipes[0][1]
	process.Stdout, streams.output = pipes[1][1], pipes[1][0]
	process.Stderr, streams.errors = pipes[2][1], pipes[2][0]
	streams.childEnds = []*os.File{pipes[0][0], pipes[1][1], pipes[2][1]}
	process.SysProcAttr.Setpgid = true
	return streams, nil
}

func (streams *execStreams) closeChildEnds() {
	for _, file := range streams.childEnds {
		file.Close()
	}
}

func (streams *execStreams) closeParentEnds() {
	streams.closeOnce.Do(func() {
		for _, file := range []*os.File{streams.input, streams.output,
			streams.errors} {
			if file != nil {
				file.Close()
			}
		}
	})
}

func (streams *execStreams) copyOutput(session *execSession) <-chan struct{} {
	var group sync.WaitGroup
	forward := func(source *os.File, kind byte) {
		defer group.Done()
		buffer := make([]byte, 32*1024)
		for {
			count, err := source.Read(buffer)
			if count > 0 {
				if session.send(kind, buffer[:count]) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	group.Add(1)
	go forward(streams.output, machine.FrameStdout)
	if streams.errors != nil {
		group.Add(1)
		go forward(streams.errors, machine.FrameStderr)
	}
	drained := make(chan struct{})
	go func() {
		group.Wait()
		close(drained)
	}()
	return drained
}

// copyInput kills the command when the host disconnects first because
// nobody can read its output. After a normal exit, background processes
// keep running.
func (session *execSession) copyInput(streams *execStreams, pid int,
	tty bool, finished *atomic.Bool) {
	for {
		kind, payload, err := machine.ReadFrame(session.connection)
		if err != nil {
			if finished.Load() {
				return
			}
			signal := unix.SIGKILL
			if tty {
				signal = unix.SIGHUP
			}
			unix.Kill(-pid, signal)
			return
		}
		switch kind {
		case machine.FrameStdin:
			if _, err := streams.input.Write(payload); err != nil &&
				!errors.Is(err, os.ErrClosed) {
				session.logger.Printf("exec stdin: %v", err)
			}
		case machine.FrameStdinClose:
			if tty {
				streams.input.Write([]byte{4})
			} else {
				streams.input.Close()
			}
		case machine.FrameResize:
			rows, columns, valid := machine.ParseResize(payload)
			if valid && streams.terminal != nil {
				console.SetSize(int(streams.terminal.Fd()), rows, columns)
			}
		}
	}
}

func execEnvironment(base []string,
	request machine.ExecRequest) []string {
	values := map[string]string{}
	var order []string
	set := func(assignment string) {
		name, value, found := strings.Cut(assignment, "=")
		if !found || name == "PRISON_TOKEN" {
			return
		}
		if _, seen := values[name]; !seen {
			order = append(order, name)
		}
		values[name] = value
	}
	for _, assignment := range base {
		set(assignment)
	}
	for _, assignment := range request.Environment {
		set(assignment)
	}
	explicit := map[string]bool{}
	for _, assignment := range request.Environment {
		name, _, _ := strings.Cut(assignment, "=")
		explicit[name] = true
	}
	if name, home, found := passwdEntryForUID(request.UID); found {
		for variable, value := range map[string]string{
			"HOME": home, "USER": name, "LOGNAME": name} {
			if !explicit[variable] {
				set(variable + "=" + value)
			}
		}
	}
	result := make([]string, 0, len(order))
	for _, name := range order {
		result = append(result, name+"="+values[name])
	}
	return result
}

func passwdEntryForUID(uid int) (string, string, bool) {
	content, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return "", "", false
	}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) >= 6 && fields[2] == strconv.Itoa(uid) {
			return fields[0], fields[5], true
		}
	}
	return "", "", false
}

func environmentValue(environment []string, name string) string {
	value := ""
	for _, assignment := range environment {
		if found, ok := strings.CutPrefix(assignment, name+"="); ok {
			value = found
		}
	}
	return value
}

// lookPathIn uses the PATH of the request because the PATH of the agent
// can differ.
func lookPathIn(name, path string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil
	}
	for _, directory := range filepath.SplitList(path) {
		candidate := filepath.Join(directory, name)
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", errors.New(name + ": command not found")
}
