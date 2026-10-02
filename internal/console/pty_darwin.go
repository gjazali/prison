//go:build darwin

package console

import (
	"bytes"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// deviceNameLength is the buffer size that `TIOCPTYGNAME` expects.
const deviceNameLength = 128

// OpenTerminalPair allocates a pty and returns its multiplexer end and its
// terminal device end.
func OpenTerminalPair() (*os.File, *os.File, error) {
	multiplexer, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot allocate a pty: %w", err)
	}
	name, err := deviceName(multiplexer)
	if err != nil {
		multiplexer.Close()
		return nil, nil, err
	}
	for _, request := range []uint{unix.TIOCPTYGRANT, unix.TIOCPTYUNLK} {
		if err := unix.IoctlSetInt(
			int(multiplexer.Fd()), request, 0); err != nil {
			multiplexer.Close()
			return nil, nil, fmt.Errorf("cannot unlock the pty %s: %w",
				name, err)
		}
	}
	device, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		multiplexer.Close()
		return nil, nil, fmt.Errorf("cannot open the pty %s: %w", name, err)
	}
	return multiplexer, device, nil
}

func deviceName(multiplexer *os.File) (string, error) {
	buffer := make([]byte, deviceNameLength)
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, multiplexer.Fd(),
		uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&buffer[0])))
	if errno != 0 {
		return "", fmt.Errorf("cannot get the pty device name: %w", errno)
	}
	end := bytes.IndexByte(buffer, 0)
	if end <= 0 {
		return "", fmt.Errorf("the pty has no device name")
	}
	return string(buffer[:end]), nil
}
