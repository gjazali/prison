package firecracker

import (
	"context"
	"errors"
	"io"
	"os/exec"
)

type hostCommand struct {
	name      string
	arguments []string
	stdin     io.Reader
	stdout    io.Writer
	stderr    io.Writer
}

// commandRunner returns the exit status. It returns an error only when the
// program cannot start.
type commandRunner func(ctx context.Context, command hostCommand) (int, error)

func runOnHost(ctx context.Context, command hostCommand) (int, error) {
	process := exec.CommandContext(ctx, command.name, command.arguments...)
	process.Stdin = command.stdin
	process.Stdout = command.stdout
	process.Stderr = command.stderr
	err := process.Run()
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode(), nil
	}
	if err != nil {
		return 0, err
	}
	return 0, nil
}
