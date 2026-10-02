//go:build unix

package control

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func SpawnDetached(executable string, arguments []string, logPath string) error {
	logFile, err := os.OpenFile(logPath,
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("cannot open the broker's log %s: %w", logPath, err)
	}
	defer logFile.Close()
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return fmt.Errorf("cannot open %s: %w", os.DevNull, err)
	}
	defer devNull.Close()
	command := exec.Command(executable, arguments...)
	command.Stdin = devNull
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return fmt.Errorf("cannot start %s: %w", executable, err)
	}
	return command.Process.Release()
}
