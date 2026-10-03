// Package console gives the isolator a pty so that the user terminal can stay
// raw. The isolator `exec` keeps output post-processing on, and this breaks
// programs that draw relative to the cursor.
package console

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

type Console struct {
	multiplexer  *os.File
	lentTerminal *os.File
	userTerminal int
	restore      *term.State
}

func Open() (*Console, error) {
	multiplexer, lentTerminal, err := OpenTerminalPair()
	if err != nil {
		return nil, err
	}
	console := &Console{
		multiplexer:  multiplexer,
		lentTerminal: lentTerminal,
		userTerminal: int(os.Stdin.Fd()),
	}
	columns, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err == nil {
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
	restore, err := term.MakeRaw(console.userTerminal)
	if err != nil {
		console.release()
		return nil, fmt.Errorf("cannot set the terminal to raw mode: %w", err)
	}
	console.restore = restore
	go func() { _, _ = io.Copy(multiplexer, os.Stdin) }()
	go func() { _, _ = io.Copy(io.Discard, multiplexer) }()
	return console, nil
}

func (c *Console) Stdin() *os.File {
	return c.lentTerminal
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
