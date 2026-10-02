package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"prison"
	"prison/internal/broker/control"
	"prison/internal/console"
	"prison/internal/image"
	"prison/internal/plugin"
	"prison/internal/session"
	"prison/internal/ui"
)

func init() {
	registerGroup(addSessionCommands)
}

func addSessionCommands(root *cobra.Command) {
	root.AddCommand(newShellCommand(), newRunCommand())
}

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

// runRootFallback treats an unknown first argument as an inmate name.
func runRootFallback(command *cobra.Command, arguments []string) error {
	if len(arguments) == 0 {
		return command.Help()
	}
	yolo := false
	for _, argument := range arguments[1:] {
		if argument != "--yolo" {
			return fmt.Errorf("unexpected argument %q. A session takes only "+
				"`--yolo`", argument)
		}
		yolo = true
	}
	return runInmateSession(arguments[0], yolo)
}

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

func sessionCommandLine(inmate *plugin.Inmate, yolo bool) (string, error) {
	if !yolo {
		return inmate.Command.Run, nil
	}
	if strings.TrimSpace(inmate.Command.Unsafe) == "" {
		return "", fmt.Errorf("the %s inmate has no unsafe mode", inmate.Name)
	}
	ui.Warn("the %s inmate will not ask before unsafe actions", inmate.Name)
	return inmate.Command.Unsafe, nil
}

func unknownSessionError(current *session.Session, name string) error {
	if known, err := current.Registry.Get(name); err == nil {
		return fmt.Errorf("%s is not enabled in this box. Add it to "+
			"`inmates` under [prison] in %s and run `prison trust`, or add "+
			"it to %s",
			known.Name, session.ProjectConfigFileName,
			current.Root.ConfigFile())
	}
	return fmt.Errorf("unknown command %q. Run `prison help`", name)
}

func requireCommandInBox(ctx context.Context, current *session.Session,
	inmate *plugin.Inmate, line string) error {
	program := strings.Fields(line)
	if len(program) == 0 {
		return fmt.Errorf("the %s inmate has no command",
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
		return fmt.Errorf("PRISON_IMAGE image %s has no %s for the %s "+
			"inmate. Unset PRISON_IMAGE",
			wanted, program[0], inmate.Name)
	}
	if created := recordedBoxImage(current); created != "" && created != wanted {
		return fmt.Errorf("this box uses image %s, which has no %s. The %s "+
			"inmate needs %s. Run `prison rm`, then `prison up`",
			created, program[0], inmate.Name, wanted)
	}
	return fmt.Errorf("image %s has no %s. Make sure that the Dockerfile "+
		"of the %s inmate installs it. Run `prison inmate show %s`",
		wanted, program[0], inmate.Name, inmate.Name)
}

func recordedBoxImage(current *session.Session) string {
	if current.Record == nil || current.Record.Box == nil {
		return ""
	}
	return current.Record.Box.Image
}

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

func execInBox(ctx context.Context, current *session.Session, line string,
	tty bool) error {
	term := current.ResolveTerm(ctx)
	environment, err := current.ExecEnvironment(
		ctx, liveBrokerClient(ctx, current), term)
	if err != nil {
		return err
	}
	stdin := io.Reader(os.Stdin)
	if tty {
		if held, err := console.Open(); err != nil {
			ui.Warn("cannot set up the terminal: %v", err)
		} else {
			defer held.Close()
			stdin = held.Stdin()
		}
	}
	rows, columns := ui.TerminalSize()
	status, err := current.Exec(ctx, session.ExecRequest{
		Command: []string{"bash", "-lc", fmt.Sprintf(
			"stty rows %d cols %d 2>/dev/null; %s", rows, columns, line)},
		TTY:         tty,
		Interactive: true,
		WorkDir:     session.WorkspaceDir,
		Environment: environment,
		Stdin:       stdin,
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

func liveBrokerClient(ctx context.Context,
	current *session.Session) *control.Client {
	client := control.NewClient(current.Root.BrokerSocket())
	if !client.Alive(ctx) {
		return nil
	}
	return client
}

func hasInteractiveTerminal() bool {
	return ui.StdinIsTerminal() && ui.StdoutIsTerminal()
}

func requireInteractiveTerminal(what string) error {
	if hasInteractiveTerminal() {
		return nil
	}
	return fmt.Errorf(
		"%s needs an interactive terminal. Use `prison run <command...>`",
		what)
}
