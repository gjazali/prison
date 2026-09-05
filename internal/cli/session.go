package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"prison"
	"prison/internal/broker/control"
	"prison/internal/image"
	"prison/internal/plugin"
	"prison/internal/session"
	"prison/internal/ui"
)

// init registers the session commands with the root.
func init() {
	registerGroup(addSessionCommands)
}

// addSessionCommands attaches `prison shell` and `prison run` to
// the root command.
func addSessionCommands(root *cobra.Command) {
	root.AddCommand(newShellCommand(), newRunCommand())
}

// newShellCommand builds `prison shell`. Returns the command.
func newShellCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "shell",
		Short: "open a login shell in this project's box",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runShell()
		},
	}
}

// newRunCommand builds `prison run <command...>`. Returns the
// command.
func newRunCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "run <command...>",
		Short: "run a command in this project's box",
		Args: cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runRun(arguments)
		},
	}
	command.Flags().SetInterspersed(false)
	return command
}

// runRootFallback handles unknown first arguments by starting an
// inmate session. Prints help when no arguments are given. Returns
// an error for unexpected arguments.
func runRootFallback(command *cobra.Command, arguments []string) error {
	if len(arguments) == 0 {
		return command.Help()
	}
	yolo := false
	for _, argument := range arguments[1:] {
		if argument != "--yolo" {
			return fmt.Errorf("unexpected argument %q; a session takes only "+
				"`--yolo`, and `prison run <command...>` runs anything else",
				argument)
		}
		yolo = true
	}
	return runInmateSession(arguments[0], yolo)
}

// runShell opens a login shell in the running box. Requires a
// terminal. Returns an error if the box is not running.
func runShell() error {
	ctx, cancel := commandContext()
	defer cancel()
	current, err := openSession()
	if err != nil {
		return err
	}
	if err := current.RequireRunning(ctx); err != nil {
		return err
	}
	if err := requireInteractiveTerminal("shell"); err != nil {
		return err
	}
	return execInBox(ctx, current, "exec bash -l", true)
}

// runRun joins arguments into one command line and runs it in the
// box. Exits with the command's status.
func runRun(arguments []string) error {
	ctx, cancel := commandContext()
	defer cancel()
	current, err := openSession()
	if err != nil {
		return err
	}
	if err := current.RequireRunning(ctx); err != nil {
		return err
	}
	return execInBox(ctx, current, strings.Join(arguments, " "),
		hasInteractiveTerminal())
}

// runInmateSession starts an inmate session by name. Takes the
// inmate name and whether to use unsafe mode. Returns an error if
// the inmate is unknown, the box is not running, or no terminal is
// available.
func runInmateSession(name string, yolo bool) error {
	ctx, cancel := commandContext()
	defer cancel()
	current, err := openSession()
	if err != nil {
		return err
	}
	inmate, enabled := current.InmateByName(name)
	if !enabled {
		return unknownSessionError(current, name)
	}
	if err := current.RequireRunning(ctx); err != nil {
		return err
	}
	if err := requireInteractiveTerminal(name); err != nil {
		return err
	}
	line, err := sessionCommandLine(inmate, yolo)
	if err != nil {
		return err
	}
	if err := requireCommandInBox(ctx, current, inmate, line); err != nil {
		return err
	}
	return execInBox(ctx, current, "exec "+line, true)
}

// sessionCommandLine returns the shell line for a session. Uses the
// unsafe command when yolo is true. Returns an error if the inmate
// has no unsafe mode.
func sessionCommandLine(inmate *plugin.Inmate, yolo bool) (string, error) {
	if !yolo {
		return inmate.Command.Run, nil
	}
	if strings.TrimSpace(inmate.Command.Unsafe) == "" {
		return "", fmt.Errorf("the %s inmate has no unsafe mode", inmate.Name)
	}
	ui.Warn("in bypass mode, the %s inmate stops asking before performing "+
		"unsafe actions ", inmate.Name)
	return inmate.Command.Unsafe, nil
}

// unknownSessionError returns an error explaining why the given name
// did not start a session.
func unknownSessionError(current *session.Session, name string) error {
	if _, err := current.Registry.Get(name); err == nil {
		return fmt.Errorf("this box holds no %s; add it to `inmates` under "+
			"[prison] in %s and run `prison trust` to hold it here, or to "+
			"%s to hold it in every box",
			name, session.ProjectConfigFileName, current.Root.ConfigFile())
	}
	return fmt.Errorf("unknown command %q; `prison help` lists what prison "+
		"does, and `prison inmate list` the tools it knows", name)
}

// requireCommandInBox returns an error if the box does not hold the
// program the session line starts with.
func requireCommandInBox(ctx context.Context, current *session.Session,
	inmate *plugin.Inmate, line string) error {
	program := strings.Fields(line)
	if len(program) == 0 {
		return fmt.Errorf("the %s inmate declares no command to run",
			inmate.Name)
	}
	result, err := current.Probe(ctx, program[:1], "")
	if err != nil {
		return err
	}
	if result.Has(program[0]) {
		return nil
	}
	wanted, err := resolveBoxImageTag(current)
	if err != nil {
		return err
	}
	if current.Overrides.Image != "" {
		return fmt.Errorf("PRISON_IMAGE names %s, which carries no %s, so "+
			"the %s inmate has nothing to run here; unset it to let prison "+
			"build an image from the enabled inmates",
			wanted, program[0], inmate.Name)
	}
	if created := recordedBoxImage(current); created != "" && created != wanted {
		return fmt.Errorf("this box was created from %s, which carries no "+
			"%s; holding %s needs %s, and a box cannot change image, so "+
			"`prison rm` then `prison up` builds that one and creates a box "+
			"on it", created, program[0], inmate.Name, wanted)
	}
	return fmt.Errorf("%s carries no %s, so the %s inmate has nothing to "+
		"run; the inmate's own layer is what installs it, and `prison inmate "+
		"show %s` names the Dockerfile that should",
		wanted, program[0], inmate.Name, inmate.Name)
}

// recordedBoxImage returns the image the box was created from.
// Returns an empty string if none was recorded.
func recordedBoxImage(current *session.Session) string {
	if current.Record == nil || current.Record.Box == nil {
		return ""
	}
	return current.Record.Box.Image
}

// resolveBoxImageTag returns the image tag for this project's box.
// Uses PRISON_IMAGE if set, otherwise computes the tag from the
// enabled inmates. Does not build anything.
func resolveBoxImageTag(current *session.Session) (string, error) {
	if current.Overrides.Image != "" {
		return current.Overrides.Image, nil
	}
	foundation, err := image.ResolveFoundation(
		current.Inmates, current.Overrides.Foundation)
	if err != nil {
		return "", err
	}
	plan, err := image.NewPlan(current.Assets, prison.BaseImageDir,
		current.Inmates, foundation, current.HostUID)
	if err != nil {
		return "", err
	}
	return plan.Final, nil
}

// execInBox runs a shell line in the running box with the session
// environment. Takes a context, session, shell line, and whether to
// use a tty. Returns a `ui.ExitError` for non-zero exit status.
func execInBox(ctx context.Context, current *session.Session, line string,
	tty bool) error {
	term := current.ResolveTerm(ctx)
	environment, err := current.ExecEnvironment(
		ctx, liveBrokerClient(ctx, current), term)
	if err != nil {
		return err
	}
	rows, columns := ui.TerminalSize()
	status, err := current.Exec(ctx, session.ExecRequest{
		Command: []string{"bash", "-lc", fmt.Sprintf(
			"stty rows %d cols %d 2>/dev/null; %s", rows, columns, line)},
		TTY:         tty,
		Interactive: true,
		WorkDir:     session.WorkspaceDir,
		Environment: environment,
		Stdin:       os.Stdin,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
	})
	if err != nil {
		return err
	}
	if status != 0 {
		return &ui.ExitError{Status: status}
	}
	return nil
}

// liveBrokerClient returns a client for a running broker. Returns
// nil if no broker is answering.
func liveBrokerClient(ctx context.Context,
	current *session.Session) *control.Client {
	client := control.NewClient(current.Root.BrokerSocket())
	if !client.Alive(ctx) {
		return nil
	}
	return client
}

// hasInteractiveTerminal returns true if both stdin and stdout are
// terminals.
func hasInteractiveTerminal() bool {
	return ui.StdinIsTerminal() && ui.StdoutIsTerminal()
}

// requireInteractiveTerminal returns an error if no terminal is
// available. Takes the name of what was requested.
func requireInteractiveTerminal(what string) error {
	if hasInteractiveTerminal() {
		return nil
	}
	return fmt.Errorf(
		"%s needs an interactive terminal; use `prison run <command...>`",
		what)
}
