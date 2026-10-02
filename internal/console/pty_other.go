//go:build !darwin && !linux

package console

import (
	"errors"
	"os"
	"runtime"
)

func OpenTerminalPair() (*os.File, *os.File, error) {
	return nil, nil, errors.New("cannot allocate a pty on " + runtime.GOOS)
}

func SetSize(descriptor, rows, columns int) error {
	return errors.New("cannot set the pty size on " + runtime.GOOS)
}
