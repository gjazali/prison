package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"prison/internal/broker"
	"prison/internal/broker/brokerlog"
	"prison/internal/broker/control"
	"prison/internal/session"
	"prison/internal/ui"
	"prison/internal/vault"
)

var brokerLogKinds = []string{
	brokerlog.KindEgress,
	brokerlog.KindInmate,
	brokerlog.KindRoute,
	brokerlog.KindSign,
}

func init() {
	registerGroup(addBrokerCommands)
}

func addBrokerCommands(root *cobra.Command) {
	group := &cobra.Command{
		Use:   "broker",
		Short: "manage the credential broker",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runBrokerStatus(command)
		},
	}
	group.AddCommand(
		newBrokerStatusCommand(),
		newBrokerRestartCommand(),
		newBrokerStopCommand(),
		newBrokerLogCommand(),
		newBrokerServeCommand(),
	)
	root.AddCommand(group)
}

func newBrokerStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "show the broker state",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runBrokerStatus(command)
		},
	}
}

func newBrokerStopCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "stop the broker",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runBrokerStop(command)
		},
	}
}

func newBrokerRestartCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "restart the broker and restore its listeners",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runBrokerRestart(command)
		},
	}
}

func newBrokerLogCommand() *cobra.Command {
	var kind string
	var projectID string
	var limit int
	command := &cobra.Command{
		Use:   "log",
		Short: "show the broker request log",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runBrokerLog(command, kind, projectID, limit)
		},
	}
	command.Flags().StringVar(&kind, "kind", "",
		"request kind: {"+strings.Join(brokerLogKinds, "|")+"}")
	command.Flags().StringVar(&projectID, "project", "",
		"show only this project ID")
	command.Flags().IntVar(&limit, "limit", 40,
		"number of recent entries to print")
	return command
}

// newBrokerServeCommand is hidden because `prison up` starts the broker.
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
		fmt.Fprintln(out, "the broker is not running. Run `prison up`")
		return nil
	}
	status, err := client.Status(ctx)
	if err != nil {
		return fmt.Errorf("cannot get the broker status: %w", err)
	}
	versionText := status.Version
	if status.Version != environment.Version {
		versionText = fmt.Sprintf("%s (CLI is %s; run `prison broker restart`)",
			status.Version, environment.Version)
	}
	fmt.Fprintf(out, "%-14s %s\n", "version", versionText)
	fmt.Fprintf(out, "%-14s %s\n", "state root", status.StateRoot)
	fmt.Fprintf(out, "%-14s %d\n", "pid", status.PID)
	fmt.Fprintf(out, "%-14s %s\n", "vault", describeVaultState(status.Vault))
	if len(status.Listeners) == 0 {
		fmt.Fprintf(out, "%-14s %s\n", "listeners", "none")
	}
	for _, listener := range status.Listeners {
		fmt.Fprintf(out, "%-14s %s\n", "listener",
			describeListener(listener))
	}
	fmt.Fprintf(out, "%-14s %d\n", "projects", len(status.Projects))
	if len(status.InmateCredentials) > 0 {
		fmt.Fprintf(out, "%-14s %s\n", "credentials",
			describeBrokerInmateCredentials(status.InmateCredentials))
	} else {
		fmt.Fprintf(out, "%-14s %s\n", "credentials", "none")
	}
	return nil
}

func describeBrokerInmateCredentials(credentials []control.InmateCredential) string {
	parts := make([]string, 0, len(credentials))
	for _, credential := range credentials {
		label := credential.Inmate
		if credential.Profile != "" && credential.Profile != "default" {
			label += "/" + credential.Profile
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", label, credential.Variable))
	}
	return strings.Join(parts, ", ")
}

func describeVaultState(state string) string {
	switch state {
	case control.VaultAbsent:
		return "absent. Run `prison secret init`"
	case control.VaultLocked:
		return "locked. Run `prison secret unlock`"
	case control.VaultUnlocked:
		return "unlocked"
	default:
		return state
	}
}

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
	return address + ", not bound"
}

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
		return fmt.Errorf("cannot stop the broker: %w", err)
	}
	fmt.Fprintln(command.OutOrStdout(), "the broker is stopping")
	return nil
}

func runBrokerRestart(command *cobra.Command) error {
	ctx, cancel := commandContext()
	defer cancel()
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	socketPath := environment.Root.BrokerSocket()
	client := control.NewClient(socketPath)
	if client.Alive(ctx) {
		if err := client.Shutdown(ctx); err != nil {
			return fmt.Errorf("cannot stop the broker: %w", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for client.Alive(ctx) {
			if !time.Now().Before(deadline) {
				return fmt.Errorf("the broker on %s did not stop in time", socketPath)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	spawn := func() error {
		return control.SpawnDetached(environment.Executable,
			[]string{"broker", "serve"}, environment.Root.BrokerLog()+".err")
	}
	client, err = control.EnsureRunning(ctx, environment.Root, environment.Version, spawn)
	if err != nil {
		return fmt.Errorf("cannot start the broker: %w", err)
	}
	status, err := client.Status(ctx)
	if err != nil {
		return fmt.Errorf("cannot read the restarted broker status: %w", err)
	}
	out := command.OutOrStdout()
	fmt.Fprintf(out, "restarted the broker (pid %d)\n", status.PID)
	for _, listener := range status.Listeners {
		fmt.Fprintf(out, "%-14s %s\n", "listener", describeListener(listener))
	}
	if vault.Exists(environment.Root.VaultFile()) && status.Vault != control.VaultUnlocked {
		fmt.Fprintln(out, "the vault is locked. Run `prison secret unlock`")
	}
	return nil
}

func runBrokerLog(command *cobra.Command, kind, projectID string,
	limit int) error {
	if kind != "" && !slices.Contains(brokerLogKinds, kind) {
		return fmt.Errorf("unknown kind %q. The kinds are %s",
			kind, strings.Join(brokerLogKinds, ", "))
	}
	if limit <= 0 {
		return fmt.Errorf("`--limit` must be at least 1")
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
		fmt.Fprintln(command.OutOrStdout(), "no matching entries")
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
		// The broker can stop after `Alive`, so this falls back to the log file.
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

func shortProjectID(projectID string) string {
	if projectID == "" {
		return "-"
	}
	return projectID
}

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

func describeBrokerLogStatus(entry brokerlog.Entry) string {
	if entry.Status == 0 {
		return entry.Outcome
	}
	if entry.Outcome == "" {
		return strconv.Itoa(entry.Status)
	}
	return fmt.Sprintf("%d %s", entry.Status, entry.Outcome)
}

func runBrokerServe() error {
	ctx, cancel := commandContext()
	defer cancel()
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	tlsConfig, err := brokerUpstreamTLSConfig()
	if err != nil {
		return err
	}
	err = broker.Run(ctx, broker.Options{
		Root:              environment.Root,
		Version:           Version,
		BrokerPort:        environment.Overrides.BrokerPort,
		SSHAuthSocket:     os.Getenv("SSH_AUTH_SOCK"),
		UpstreamTLSConfig: tlsConfig,
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// brokerUpstreamTLSConfig honors SSL_CERT_FILE on platforms where Go's
// crypto/x509 does not read it from the environment (such as macOS).
func brokerUpstreamTLSConfig() (*tls.Config, error) {
	path := strings.TrimSpace(os.Getenv("SSL_CERT_FILE"))
	if path == "" {
		return nil, nil
	}
	pemData, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read SSL_CERT_FILE %s: %w", path, err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pemData) {
		return nil, fmt.Errorf("SSL_CERT_FILE %s has no valid certificates", path)
	}
	return &tls.Config{RootCAs: pool}, nil
}
