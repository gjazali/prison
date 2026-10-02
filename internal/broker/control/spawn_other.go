//go:build !unix

package control

import "errors"

func SpawnDetached(executable string, arguments []string, logPath string) error {
	return errors.New("cannot start a detached broker on this platform")
}
