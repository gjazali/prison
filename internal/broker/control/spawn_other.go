//go:build !unix

package control

import "errors"

// SpawnDetached always returns an error on non-Unix platforms.
func SpawnDetached(executable string, arguments []string, logPath string) error {
	return errors.New("prison cannot start a detached broker on this platform")
}
