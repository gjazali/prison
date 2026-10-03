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

	"prison/internal/isolator"
)

const GuestBinary = "/usr/local/bin/prison-guest"

const readinessTimeout = 60 * time.Second

const readinessInterval = time.Second

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

func (s *Session) Exec(ctx context.Context, request ExecRequest) (int, error) {
	workDir := request.WorkDir
	if workDir == "" {
		workDir = WorkspaceDir
	}
	return s.Isolator.Exec(ctx, isolator.ExecSpec{
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

func (s *Session) RunQuiet(ctx context.Context, command ...string) (int, string, error) {
	output := &bytes.Buffer{}
	status, err := s.Exec(ctx, ExecRequest{
		Command: command,
		Stdout:  output,
		Stderr:  output,
	})
	return status, output.String(), err
}

func (s *Session) RunShell(ctx context.Context, line string,
	environment []string) (int, error) {
	return s.Exec(ctx, ExecRequest{
		Command:     []string{"bash", "-lc", line},
		Environment: environment,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
	})
}

type ProbeResult struct {
	Commands map[string]bool `json:"commands"`
	Terminfo bool            `json:"terminfo"`
}

func (p ProbeResult) Has(command string) bool {
	return p.Commands[command]
}

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
		return result, fmt.Errorf("cannot probe the box: %w", err)
	}
	if status != 0 {
		return result, fmt.Errorf(
			"the box probe failed. Run `prison rm`, then `prison up`")
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		return result, fmt.Errorf("the box probe returned invalid output: %w", err)
	}
	if result.Commands == nil {
		result.Commands = map[string]bool{}
	}
	return result, nil
}

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
			message := "the box resolver did not start"
			if lastOutput != "" {
				message += ": " + lastOutput
			}
			return fmt.Errorf("%s. Run `prison rm`, then `prison up`", message)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(readinessInterval):
		}
	}
}

func (s *Session) RequireRunning(ctx context.Context) error {
	box, err := s.Isolator.Box(ctx, s.BoxName)
	if err != nil {
		return err
	}
	if !box.Exists {
		return fmt.Errorf("this project has no box. Run `prison up`")
	}
	if !box.Running {
		return fmt.Errorf("this project's box is stopped. Run `prison up`")
	}
	return nil
}

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
