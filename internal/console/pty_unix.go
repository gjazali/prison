//go:build darwin || linux

package console

import (
	"fmt"
	"os"
	"os/signal"

	"golang.org/x/sys/unix"
)

func SetSize(descriptor, rows, columns int) error {
	err := unix.IoctlSetWinsize(descriptor, unix.TIOCSWINSZ, &unix.Winsize{
		Row: uint16(rows),
		Col: uint16(columns),
	})
	if err != nil {
		return fmt.Errorf("cannot set the pty size: %w", err)
	}
	return nil
}

func watchWindowResize(changes chan<- os.Signal) {
	signal.Notify(changes, unix.SIGWINCH)
}

// SignalResize delivers SIGWINCH to a child process so it re-reads the lent
// terminal's updated window size.
func SignalResize(process *os.Process) {
	if process != nil {
		_ = process.Signal(unix.SIGWINCH)
	}
}
