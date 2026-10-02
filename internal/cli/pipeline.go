package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
)

func pipeThroughCommand(commandLine string, input io.Reader) error {
	command := exec.Command("sh", "-c", commandLine)
	command.Stdin = input
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("cannot run `%s`: %w", commandLine, err)
	}
	_ = command.Wait()
	return nil
}
