//go:build linux

package console

import (
	"fmt"
	"os"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// OpenTerminalPair allocates a pty and returns its multiplexer end and its
// terminal device end.
func OpenTerminalPair() (*os.File, *os.File, error) {
	multiplexer, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot allocate a pty: %w", err)
	}
	descriptor := int(multiplexer.Fd())
	number, err := unix.IoctlGetInt(descriptor, unix.TIOCGPTN)
	if err != nil {
		multiplexer.Close()
		return nil, nil, fmt.Errorf("cannot get the pty number: %w", err)
	}
	name := "/dev/pts/" + strconv.Itoa(number)
	err = unix.IoctlSetPointerInt(descriptor, unix.TIOCSPTLCK, 0)
	if err != nil {
		multiplexer.Close()
		return nil, nil, fmt.Errorf("cannot unlock the pty %s: %w", name, err)
	}
	device, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		multiplexer.Close()
		return nil, nil, fmt.Errorf("cannot open the pty %s: %w", name, err)
	}
	return multiplexer, device, nil
}
