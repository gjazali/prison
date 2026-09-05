package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"prison/internal/cage"
)

// GuestBinary is the guest agent path inside the box.
const GuestBinary = "/usr/local/bin/prison-guest"

// readinessTimeout is the deadline for WaitUntilReady.
const readinessTimeout = 60 * time.Second

// readinessInterval is the pause between poll attempts.
const readinessInterval = time.Second

// ExecRequest holds the command and its I/O for a box exec.
type ExecRequest struct {
	Command     []string
	TTY         bool
	Interactive bool
	WorkDir     string
	Environment []string
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
}

// Exec runs a command in the box. Takes a context and an ExecRequest.
// Returns the exit status.
func (s *Session) Exec(ctx context.Context, request ExecRequest) (int, error) {
	workDir := request.WorkDir
	if workDir == "" {
		workDir = WorkspaceDir
	}
	return s.Cage.Exec(ctx, cage.ExecSpec{
		Box:         s.BoxName,
		Command:     request.Command,
		TTY:         request.TTY,
		Interactive: request.Interactive,
		WorkDir:     workDir,
		UID:         s.HostUID,
		GID:         s.HostGID,
		Environment: request.Environment,
		Stdin:       request.Stdin,
		Stdout:      request.Stdout,
		Stderr:      request.Stderr,
	})
}

// RunQuiet runs command strings in the box. Returns the exit status
// and combined stdout/stderr.
func (s *Session) RunQuiet(ctx context.Context, command ...string) (int, string, error) {
	output := &bytes.Buffer{}
	status, err := s.Exec(ctx, ExecRequest{
		Command: command,
		Stdout:  output,
		Stderr:  output,
	})
	return status, output.String(), err
}

// RunShell takes a context, shell line, and environment. Runs it in
// the box through `bash -lc` and returns the exit status.
func (s *Session) RunShell(ctx context.Context, line string,
	environment []string) (int, error) {
	return s.Exec(ctx, ExecRequest{
		Command:     []string{"bash", "-lc", line},
		Environment: environment,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
	})
}

// ProbeResult holds which commands the box has and whether the
// requested terminfo is installed.
type ProbeResult struct {
	Commands map[string]bool `json:"commands"`
	Terminfo bool            `json:"terminfo"`
}

// Has takes a command name and returns true if the box has it.
func (p ProbeResult) Has(command string) bool {
	return p.Commands[command]
}

// Probe takes a context, command names, and a TERM value. Returns
// which commands the box has and whether the terminfo is installed.
func (s *Session) Probe(ctx context.Context, commands []string,
	term string) (ProbeResult, error) {
	result := ProbeResult{Commands: map[string]bool{}}
	if len(commands) == 0 && term == "" {
		return result, nil
	}
	arguments := []string{GuestBinary, "probe"}
	if len(commands) > 0 {
		arguments = append(arguments, "--command", strings.Join(commands, ","))
	}
	if term != "" {
		arguments = append(arguments, "--term", term)
	}
	output := &bytes.Buffer{}
	status, err := s.Exec(ctx, ExecRequest{Command: arguments, Stdout: output})
	if err != nil {
		return result, fmt.Errorf("cannot ask the box what it holds: %w", err)
	}
	if status != 0 {
		return result, fmt.Errorf(
			"the box could not report what it holds; `prison rm` then `prison up` rebuilds it")
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		return result, fmt.Errorf("the box answered something unexpected: %w", err)
	}
	if result.Commands == nil {
		result.Commands = map[string]bool{}
	}
	return result, nil
}

// WaitUntilReady polls the box until its resolver is ready, up to
// readinessTimeout. Returns an error on timeout or cancellation.
func (s *Session) WaitUntilReady(ctx context.Context) error {
	deadline := time.Now().Add(readinessTimeout)
	var lastOutput string
	for {
		status, output, err := s.RunQuiet(ctx, GuestBinary, "ready")
		if err == nil && status == 0 {
			return nil
		}
		if output != "" {
			lastOutput = strings.TrimSpace(output)
		}
		if time.Now().After(deadline) {
			message := "the box started but its resolver never came up"
			if lastOutput != "" {
				message += ": " + lastOutput
			}
			return fmt.Errorf("%s; `prison rm` then `prison up` rebuilds it",
				message)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(readinessInterval):
		}
	}
}

// RequireRunning returns an error if the box does not exist or is not
// running.
func (s *Session) RequireRunning(ctx context.Context) error {
	box, err := s.Cage.Box(ctx, s.BoxName)
	if err != nil {
		return err
	}
	if !box.Exists {
		return fmt.Errorf("this project has no box; `prison up` creates one")
	}
	if !box.Running {
		return fmt.Errorf("this project's box is stopped; `prison up` starts it")
	}
	return nil
}

// ResolveTerm returns the TERM value for interactive sessions. Falls
// back to "xterm-256color" if the host's value is missing or
// unsupported.
func (s *Session) ResolveTerm(ctx context.Context) string {
	term := os.Getenv("TERM")
	if term == "" || term == "dumb" {
		return "xterm-256color"
	}
	result, err := s.Probe(ctx, nil, term)
	if err != nil || result.Terminfo {
		return term
	}
	return "xterm-256color"
}
