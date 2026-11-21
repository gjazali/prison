// Package console gives the isolator a pty so that the user terminal can stay
// raw. The isolator `exec` keeps output post-processing on, and this breaks
// programs that draw relative to the cursor.
package console

import (
	"fmt"
	"io"
	"os"
	"os/signal"

	"golang.org/x/term"
)

type Console struct {
	multiplexer  *os.File
	lentTerminal *os.File
	userTerminal int
	restore      *term.State
	resizes      chan struct{}
	stopResize   func()
}

func openLent() (*Console, error) {
	return openLentWithSize(userTerminalSize)
}

func userTerminalSize() (rows, columns int, ok bool) {
	for _, file := range []*os.File{os.Stdout, os.Stdin} {
		columns, rows, err := term.GetSize(int(file.Fd()))
		if err == nil && rows > 0 && columns > 0 {
			return rows, columns, true
		}
	}
	return 0, 0, false
}

func openLentWithSize(
	size func() (rows, columns int, ok bool),
) (*Console, error) {
	multiplexer, lentTerminal, err := OpenTerminalPair()
	if err != nil {
		return nil, err
	}
	console := &Console{
		multiplexer:  multiplexer,
		lentTerminal: lentTerminal,
		resizes:      make(chan struct{}, 1),
	}
	if rows, columns, ok := size(); ok {
		if err := SetSize(int(lentTerminal.Fd()), rows, columns); err != nil {
			console.release()
			return nil, err
		}
	}
	// The isolator resets this when it starts. Raw mode stops early echo.
	if _, err := term.MakeRaw(int(lentTerminal.Fd())); err != nil {
		console.release()
		return nil, fmt.Errorf("cannot set the pty to raw mode: %w", err)
	}
	console.startResizing(size)
	return console, nil
}

func (c *Console) startResizing(size func() (rows, columns int, ok bool)) {
	changes := make(chan os.Signal, 1)
	watchWindowResize(changes)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-changes:
				c.syncSize(size)
			case <-stop:
				return
			}
		}
	}()
	c.stopResize = func() {
		signal.Stop(changes)
		close(stop)
		<-done
	}
}

func (c *Console) syncSize(size func() (rows, columns int, ok bool)) {
	rows, columns, ok := size()
	if !ok {
		return
	}
	if err := c.setMultiplexerSize(rows, columns); err != nil {
		return
	}
	select {
	case c.resizes <- struct{}{}:
	default:
	}
}

// setMultiplexerSize resizes the PTY through the multiplexer's SyscallConn so
// that `lentTerminal.Fd()` is never called after startup. Calling `Fd()` on a
// file opened by `os.OpenFile` resets `O_NONBLOCK` on the shared open file
// description, which breaks child processes (like Apple `container`) that put
// their inherited stdin into non-blocking mode.
func (c *Console) setMultiplexerSize(rows, columns int) error {
	raw, err := c.multiplexer.SyscallConn()
	if err != nil {
		return err
	}
	var setErr error
	if err := raw.Control(func(fd uintptr) {
		setErr = SetSize(int(fd), rows, columns)
	}); err != nil {
		return err
	}
	return setErr
}

func Open() (*Console, error) {
	console, err := openLent()
	if err != nil {
		return nil, err
	}
	console.userTerminal = int(os.Stdin.Fd())
	restore, err := term.MakeRaw(console.userTerminal)
	if err != nil {
		console.release()
		return nil, fmt.Errorf("cannot set the terminal to raw mode: %w", err)
	}
	console.restore = restore
	go func() { _, _ = io.Copy(console.multiplexer, os.Stdin) }()
	go func() { _, _ = io.Copy(io.Discard, console.multiplexer) }()
	return console, nil
}

// OpenOutput lends a pty without changing the user terminal or reading stdin.
func OpenOutput() (*Console, error) {
	console, err := openLent()
	if err != nil {
		return nil, err
	}
	go func() { _, _ = io.Copy(io.Discard, console.multiplexer) }()
	return console, nil
}

func (c *Console) Stdin() *os.File {
	return c.lentTerminal
}

// Resizes signals after the lent terminal's size has been updated to match the
// user's terminal.
func (c *Console) Resizes() <-chan struct{} {
	return c.resizes
}

// Close is safe to call twice. It leaves the stdin goroutine blocked because
// prison exits after the session.
func (c *Console) Close() error {
	var failure error
	if c.restore != nil {
		failure = term.Restore(c.userTerminal, c.restore)
		c.restore = nil
	}
	if err := c.release(); failure == nil {
		failure = err
	}
	return failure
}

func (c *Console) release() error {
	if c.stopResize != nil {
		c.stopResize()
		c.stopResize = nil
	}
	var failure error
	for _, file := range []**os.File{&c.lentTerminal, &c.multiplexer} {
		if *file == nil {
			continue
		}
		if err := (*file).Close(); err != nil && failure == nil {
			failure = err
		}
		*file = nil
	}
	return failure
}
