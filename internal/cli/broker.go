package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"prison/internal/broker"
	"prison/internal/broker/brokerlog"
	"prison/internal/broker/control"
	"prison/internal/broker/limits"
	"prison/internal/session"
	"prison/internal/ui"
)

// brokerLogKinds lists the request kinds the broker records.
// The `--kind` flag accepts these values.
var brokerLogKinds = []string{
	brokerlog.KindEgress,
	brokerlog.KindInmate,
	brokerlog.KindRoute,
	brokerlog.KindSign,
}

func init() {
	registerGroup(addBrokerCommands)
}

// addBrokerCommands adds the `prison broker` command group to root.
// Running `prison broker` with no subcommand shows status.
func addBrokerCommands(root *cobra.Command) {
	group := &cobra.Command{
		Use:   "broker",
		Short: "the one host process that holds every credential",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runBrokerStatus(command)
		},
	}
	group.AddCommand(
		newBrokerStatusCommand(),
		newBrokerStopCommand(),
		newBrokerLogCommand(),
		newBrokerServeCommand(),
	)
	root.AddCommand(group)
}

// newBrokerStatusCommand builds the `prison broker status` command.
// It prints what the running broker holds.
func newBrokerStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "what the running broker holds",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runBrokerStatus(command)
		},
	}
}

// newBrokerStopCommand builds the `prison broker stop` command.
// It stops the broker, which forgets all credentials.
func newBrokerStopCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "stop the broker, so it forgets every credential",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runBrokerStop(command)
		},
	}
}

// newBrokerLogCommand builds the `prison broker log` command.
// It prints the requests the broker answered. Flags `--kind`,
// `--project`, and `--limit` filter the output.
func newBrokerLogCommand() *cobra.Command {
	var kind string
	var projectID string
	var limit int
	command := &cobra.Command{
		Use:   "log",
		Short: "what the broker was asked, never what it carried",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runBrokerLog(command, kind, projectID, limit)
		},
	}
	command.Flags().StringVar(&kind, "kind", "",
		"Options: {"+strings.Join(brokerLogKinds, "|")+"}")
	command.Flags().StringVar(&projectID, "project", "",
		"only this project id")
	command.Flags().IntVar(&limit, "limit", 40,
		"how many of the most recent entries to print")
	return command
}

// newBrokerServeCommand builds the hidden `prison broker serve` command.
// It runs the broker in the foreground. `prison up` starts it.
func newBrokerServeCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "serve",
		Short:  "run the broker in the foreground",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runBrokerServe()
		},
	}
}

// runBrokerStatus prints the broker's socket path and, if the broker is
// running, its version, state root, pid, vault, listeners, projects,
// and credentials. Takes command for its output writer. Returns nil.
func runBrokerStatus(command *cobra.Command) error {
	ctx, cancel := commandContext()
	defer cancel()
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	out := command.OutOrStdout()
	client := control.NewClient(environment.Root.BrokerSocket())
	fmt.Fprintf(out, "%-14s %s\n", "socket", client.SocketPath())
	if !client.Alive(ctx) {
		fmt.Fprintln(out,
			"the broker is not running; `prison up` starts it")
		return nil
	}
	status, err := client.Status(ctx)
	if err != nil {
		return fmt.Errorf("the broker is running but would not say what "+
			"it holds: %w", err)
	}
	fmt.Fprintf(out, "%-14s %s\n", "version", status.Version)
	fmt.Fprintf(out, "%-14s %s\n", "state root", status.StateRoot)
	fmt.Fprintf(out, "%-14s %d\n", "pid", status.PID)
	fmt.Fprintf(out, "%-14s %s\n", "vault", describeVaultState(status.Vault))
	if len(status.Listeners) == 0 {
		fmt.Fprintf(out, "%-14s %s\n", "listeners",
			"none yet, because no box is running")
	}
	for _, listener := range status.Listeners {
		fmt.Fprintf(out, "%-14s %s\n", "listener",
			describeListener(listener))
	}
	fmt.Fprintf(out, "%-14s %d\n", "projects", len(status.Projects))
	if len(status.CredentialVariables) > 0 {
		fmt.Fprintf(out, "%-14s %s\n", "credentials",
			strings.Join(status.CredentialVariables, ", "))
	} else {
		fmt.Fprintf(out, "%-14s %s\n", "credentials",
			"none, so no inmate can reach its upstream")
	}
	return nil
}

// describeVaultState takes a vault state string and returns a
// human-readable description with a suggested next step.
func describeVaultState(state string) string {
	switch state {
	case control.VaultAbsent:
		return "absent, so no secret is held; `prison secret init` makes one"
	case control.VaultLocked:
		return "locked, so granted secrets do not answer; " +
			"`prison secret unlock` opens it"
	case control.VaultUnlocked:
		return "unlocked"
	default:
		return state
	}
}

// describeListener takes a listener and returns a string showing
// its address, port, and bind status.
func describeListener(listener control.Listener) string {
	address := listener.Address
	if listener.Port != 0 {
		address = address + ":" + strconv.Itoa(listener.Port)
	}
	if listener.Bound {
		return address + ", bound"
	}
	if listener.Error != "" {
		return address + ", not bound: " + listener.Error
	}
	return address + ", not bound yet"
}

// runBrokerStop asks the broker to exit. Takes command for its output
// writer. Returns nil if no broker is running.
func runBrokerStop(command *cobra.Command) error {
	ctx, cancel := commandContext()
	defer cancel()
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	client := control.NewClient(environment.Root.BrokerSocket())
	if !client.Alive(ctx) {
		fmt.Fprintln(command.OutOrStdout(), "no broker is running")
		return nil
	}
	if err := client.Shutdown(ctx); err != nil {
		return fmt.Errorf("the broker would not stop: %w", err)
	}
	fmt.Fprintln(command.OutOrStdout(),
		"the broker is stopping; it forgets every credential as it goes")
	return nil
}

// runBrokerLog prints a table of logged requests. Takes command for
// output, kind and projectID as filters, and limit for how many of the
// newest entries to show. Returns an error on failure.
func runBrokerLog(command *cobra.Command, kind, projectID string,
	limit int) error {
	if kind != "" && !slices.Contains(brokerLogKinds, kind) {
		return fmt.Errorf("%q is not a kind of request prison records; "+
			"it keeps %s", kind, strings.Join(brokerLogKinds, ", "))
	}
	if limit <= 0 {
		return fmt.Errorf(
			"`--limit` counts entries to print, so it has to be at least 1")
	}
	ctx, cancel := commandContext()
	defer cancel()
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	entries, err := readBrokerLogEntries(ctx, environment, kind, projectID,
		limit)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Fprintln(command.OutOrStdout(),
			"the broker has answered nothing that matches")
		return nil
	}
	rows := [][]string{{"TIME", "KIND", "PROJECT", "WHAT", "STATUS"}}
	for _, entry := range entries {
		rows = append(rows, []string{
			entry.Time.Format("2006-01-02T15:04:05"),
			entry.Kind,
			shortProjectID(entry.Project),
			describeBrokerLogEntry(entry),
			describeBrokerLogStatus(entry),
		})
	}
	return ui.Table(command.OutOrStdout(), rows)
}

// readBrokerLogEntries returns log entries matching kind, projectID,
// and limit, oldest first. Takes ctx, the environment, filter strings,
// and a limit. It tries the live broker first and falls back to the
// log file. Returns the matching entries or an error.
func readBrokerLogEntries(ctx context.Context,
	environment *session.Environment, kind, projectID string,
	limit int) ([]brokerlog.Entry, error) {
	client := control.NewClient(environment.Root.BrokerSocket())
	if client.Alive(ctx) {
		entries, err := client.Log(ctx, control.LogQuery{
			Kind:    kind,
			Project: projectID,
			Limit:   limit,
		})
		// Falls back to the file if the broker stops between calls.
		if err == nil {
			return entries, nil
		}
	}
	return brokerlog.Read(environment.Root.BrokerLog(), brokerlog.Filter{
		Kind:    kind,
		Project: projectID,
		Limit:   limit,
	})
}

// shortProjectID takes a project ID and returns it, or "-" if empty.
func shortProjectID(projectID string) string {
	if projectID == "" {
		return "-"
	}
	return projectID
}

// describeBrokerLogEntry takes a log entry and returns a short string
// describing what was requested.
func describeBrokerLogEntry(entry brokerlog.Entry) string {
	switch {
	case entry.Path != "" && entry.Method != "":
		return entry.Method + " " + entry.Path
	case entry.Host != "" && entry.Port != 0:
		method := entry.Method
		if method == "" {
			method = "CONNECT"
		}
		return fmt.Sprintf("%s %s:%d", method, entry.Host, entry.Port)
	case entry.Host != "":
		return entry.Host
	case entry.Secret != "":
		return entry.Secret
	default:
		return entry.Outcome
	}
}

// describeBrokerLogStatus takes a log entry and returns its HTTP
// status and outcome as a single string.
func describeBrokerLogStatus(entry brokerlog.Entry) string {
	if entry.Status == 0 {
		return entry.Outcome
	}
	if entry.Outcome == "" {
		return strconv.Itoa(entry.Status)
	}
	return fmt.Sprintf("%d %s", entry.Status, entry.Outcome)
}

// runBrokerServe runs the broker in the foreground until stopped.
// Returns nil on a clean shutdown or interrupt.
func runBrokerServe() error {
	ctx, cancel := commandContext()
	defer cancel()
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	err = broker.Run(ctx, broker.Options{
		Root:          environment.Root,
		Version:       Version,
		BrokerPort:    environment.Overrides.BrokerPort,
		SSHAuthSocket: os.Getenv("SSH_AUTH_SOCK"),
		Confirmer:     limits.NewDialogConfirmer(),
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
