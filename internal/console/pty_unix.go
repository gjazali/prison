//go:build darwin || linux

package console

import (
	"fmt"

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
