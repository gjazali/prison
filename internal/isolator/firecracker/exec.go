package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"prison/internal/isolator"
	"prison/internal/machine"
)

func (driver *Driver) Exec(
	ctx context.Context, spec isolator.ExecSpec,
) (int, error) {
	if driver.processOf(spec.Box) == 0 {
		return 0, fmt.Errorf("box %s is not running", spec.Box)
	}
	socket := filepath.Join(driver.boxDirectory(spec.Box), vsockSocketFile)
	connection, err := dialAgent(ctx, socket)
	if err != nil {
		return 0, fmt.Errorf("cannot reach box %s: %w", spec.Box, err)
	}
	defer connection.Close()
	stdin, stdout, stderr := spec.Stdin, spec.Stdout, spec.Stderr
	if stdin == nil {
		stdin = os.Stdin
	}
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	request := machine.ExecRequest{
		Command:     spec.Command,
		TTY:         spec.TTY,
		WorkDir:     spec.WorkDir,
		UID:         spec.UID,
		GID:         spec.GID,
		Environment: spec.Environment,
	}
	if spec.TTY {
		request.Rows, request.Columns = terminalSize(stdin)
	}
	line, _ := json.Marshal(machine.Request{
		Operation: machine.OperationExec, Exec: &request})
	if _, err := connection.Write(append(line, '\n')); err != nil {
		return 0, fmt.Errorf("cannot reach box %s: %w", spec.Box, err)
	}
	session := &hostExecSession{connection: connection}
	if spec.Interactive || spec.TTY {
		go session.forwardStdin(stdin)
	} else {
		session.send(machine.FrameStdinClose, nil)
	}
	if spec.TTY {
		stopResizing := session.forwardResizes(stdin)
		defer stopResizing()
	}
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			connection.Close()
		case <-finished:
		}
	}()
	return session.receive(ctx, spec.Box, stdout, stderr)
}

type hostExecSession struct {
	connection net.Conn
	writeLock  sync.Mutex
}

func (session *hostExecSession) send(kind byte, payload []byte) error {
	session.writeLock.Lock()
	defer session.writeLock.Unlock()
	return machine.WriteFrame(session.connection, kind, payload)
}

func (session *hostExecSession) forwardStdin(stdin io.Reader) {
	buffer := make([]byte, 32*1024)
	for {
		count, err := stdin.Read(buffer)
		if count > 0 {
			if session.send(machine.FrameStdin, buffer[:count]) != nil {
				return
			}
		}
		if err != nil {
			session.send(machine.FrameStdinClose, nil)
			return
		}
	}
}

func (session *hostExecSession) forwardResizes(stdin io.Reader) func() {
	changes := make(chan os.Signal, 1)
	signal.Notify(changes, unix.SIGWINCH)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-changes:
				rows, columns := terminalSize(stdin)
				if rows > 0 && columns > 0 {
					session.send(machine.FrameResize,
						machine.ResizePayload(rows, columns))
				}
			case <-stop:
				return
			}
		}
	}()
	return func() {
		signal.Stop(changes)
		close(stop)
	}
}

func (session *hostExecSession) receive(ctx context.Context, box string,
	stdout, stderr io.Writer) (int, error) {
	for {
		kind, payload, err := machine.ReadFrame(session.connection)
		if err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			return 0, fmt.Errorf("the connection to box %s ended: %w",
				box, err)
		}
		switch kind {
		case machine.FrameStdout:
			stdout.Write(payload)
		case machine.FrameStderr:
			stderr.Write(payload)
		case machine.FrameExit:
			status, valid := machine.ParseExit(payload)
			if !valid {
				return 0, errors.New("the exit frame is not valid")
			}
			return status, nil
		case machine.FrameFailure:
			return 0, fmt.Errorf("cannot run the command in box %s: %s",
				box, payload)
		}
	}
}

func terminalSize(stdin io.Reader) (int, int) {
	candidates := []*os.File{os.Stdout}
	if file, isFile := stdin.(*os.File); isFile {
		candidates = append([]*os.File{file}, candidates...)
	}
	for _, file := range candidates {
		columns, rows, err := term.GetSize(int(file.Fd()))
		if err == nil && rows > 0 && columns > 0 {
			return rows, columns
		}
	}
	return 0, 0
}
