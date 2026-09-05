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

// Command describes a host program to run. Nil streams are discarded.
// Setting InheritStreams gives the child the process's own streams.
type Command struct {
	Name           string
	Arguments      []string
	Stdin          io.Reader
	Stdout         io.Writer
	Stderr         io.Writer
	InheritStreams bool
}

// Runner runs host commands for the driver. All shell-outs go through
// this interface.
type Runner interface {
	// Run runs the command. Returns the exit status. The error is
	// non-nil only when the program could not start.
	Run(ctx context.Context, command Command) (int, error)
}

// SystemRunner runs commands as real child processes via os/exec.
type SystemRunner struct{}

// Run starts the command and waits for it. Returns the exit status.
// A non-zero exit is a status, not an error.
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

// runCapturing runs a command and captures its output. Returns stdout,
// stderr, the exit status, and any error.
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

// runQuietly runs a command and discards its output. Returns true if
// the exit status is zero.
func (driver *Driver) runQuietly(
	ctx context.Context, name string, arguments ...string,
) (bool, error) {
	_, _, status, err := driver.runCapturing(ctx, name, arguments...)
	if err != nil {
		return false, err
	}
	return status == 0, nil
}

// containerJSON runs a container subcommand and decodes its JSON
// output into target. Returns false on a non-zero exit instead of an
// error.
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

// trailingDetail formats stderr for appending to an error message.
// Returns an empty string when stderr is empty.
func trailingDetail(stderr []byte) string {
	text := strings.TrimSpace(string(stderr))
	if text == "" {
		return ""
	}
	return ": " + text
}
