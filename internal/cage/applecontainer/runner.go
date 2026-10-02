package applecontainer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Command describes a host program to run. Without `InheritStreams`, a nil
// stream discards its data.
type Command struct {
	Name           string
	Arguments      []string
	Stdin          io.Reader
	Stdout         io.Writer
	Stderr         io.Writer
	InheritStreams bool
}

type Runner interface {
	// Run returns the exit status. It returns an error only when the program
	// cannot start.
	Run(ctx context.Context, command Command) (int, error)
}

type SystemRunner struct{}

func (SystemRunner) Run(
	ctx context.Context, command Command,
) (int, error) {
	process := exec.CommandContext(ctx, command.Name, command.Arguments...)
	if command.InheritStreams {
		process.Stdin = os.Stdin
		process.Stdout = os.Stdout
		process.Stderr = os.Stderr
	}
	if command.Stdin != nil {
		process.Stdin = command.Stdin
	}
	if command.Stdout != nil {
		process.Stdout = command.Stdout
	}
	if command.Stderr != nil {
		process.Stderr = command.Stderr
	}
	err := process.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 0, fmt.Errorf("running %s: %w", command.Name, err)
}

func (driver *Driver) runCapturing(
	ctx context.Context, name string, arguments ...string,
) (stdout []byte, stderr []byte, status int, err error) {
	var outputBuffer, errorBuffer bytes.Buffer
	status, err = driver.runner.Run(ctx, Command{
		Name:      name,
		Arguments: arguments,
		Stdout:    &outputBuffer,
		Stderr:    &errorBuffer,
	})
	return outputBuffer.Bytes(), errorBuffer.Bytes(), status, err
}

func (driver *Driver) runQuietly(
	ctx context.Context, name string, arguments ...string,
) (bool, error) {
	_, _, status, err := driver.runCapturing(ctx, name, arguments...)
	if err != nil {
		return false, err
	}
	return status == 0, nil
}

func (driver *Driver) containerJSON(
	ctx context.Context, target any, arguments ...string,
) (bool, error) {
	stdout, stderr, status, err := driver.runCapturing(
		ctx, containerBinary, arguments...)
	if err != nil {
		return false, err
	}
	if status != 0 {
		return false, nil
	}
	if err := json.Unmarshal(stdout, target); err != nil {
		return false, fmt.Errorf(
			"reading `container %s`: %w%s",
			arguments[0], err, trailingDetail(stderr))
	}
	return true, nil
}

func trailingDetail(stderr []byte) string {
	text := strings.TrimSpace(string(stderr))
	if text == "" {
		return ""
	}
	return ": " + text
}
