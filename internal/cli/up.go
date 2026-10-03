package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"prison/internal/isolator"
	"prison/internal/session"
	"prison/internal/ui"
)

func init() {
	registerGroup(addUpCommand)
	registerGroup(addSetupCommand)
}

func addUpCommand(root *cobra.Command) {
	root.AddCommand(&cobra.Command{
		Use:   "up",
		Short: "create or start this project's box",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runUp()
		},
	})
}

func runUp() error {
	ctx, cancel := commandContext()
	defer cancel()
	current, err := openSession()
	if err != nil {
		return err
	}
	result, err := current.Up(ctx)
	if err != nil {
		return err
	}
	printAccessSummary(ctx, current, result)
	return nil
}

func printAccessSummary(ctx context.Context, current *session.Session,
	result *session.UpResult) {
	fmt.Println()
	ui.Summary("box", "%s", current.BoxName)
	if current.Record != nil && current.Record.Box != nil && current.Record.Box.Sudo {
		ui.Summary("sudo", "granted in this box")
	}
	if result.Network.HostOnly {
		ui.Summary("network", "%s, host-only", result.Network.Name)
	} else {
		ui.Summary("network", "%s, not host-only", result.Network.Name)
	}
	if len(current.Inmates) > 0 {
		ui.Summary("inmates", "%s", describeInmates(current, result))
	}
	ui.Summary("project", "%s -> %s", current.Directory, session.WorkspaceDir)
	ui.Summary("state", "%s", current.Project.Dir)
	if len(result.Shadow) > 0 {
		ui.Summary("shadow", "%s", strings.Join(result.Shadow, ", "))
	}
	ui.Summary("image", "%s", result.Image)
	ui.Summary("egress", "%s", current.Project.EgressAllowFile())
	printHostnameSummary(current)
	if box, err := current.Isolator.Box(ctx, current.BoxName); err == nil &&
		box.Address != "" {
		ui.Summary("ip", "%s", box.Address)
	}
	if current.ConfigPresent {
		if current.ConfigTrusted {
			ui.Summary("config", "prison.toml, trusted")
		} else {
			ui.Summary("config", "prison.toml, ignored until `prison trust`")
		}
	}
	for _, mapping := range result.Ports {
		printPortLine(mapping)
	}
	printSessionLines(current, result)
}

func printPortLine(mapping isolator.PortMapping) {
	if mapping.Host == mapping.Guest {
		ui.Summary("ports", "http://127.0.0.1:%d", mapping.Host)
		return
	}
	ui.Summary("ports", "http://127.0.0.1:%d -> %d", mapping.Host, mapping.Guest)
}

func describeInmates(current *session.Session, result *session.UpResult) string {
	names := make([]string, 0, len(current.Inmates))
	for _, inmate := range current.Inmates {
		name := inmate.Name
		if !inmateIsInBox(inmate.Command.Run, result) {
			name += " (not in this box)"
		}
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

func inmateIsInBox(runCommand string, result *session.UpResult) bool {
	fields := strings.Fields(runCommand)
	if len(fields) == 0 {
		return false
	}
	return result.Probe.Has(fields[0])
}

func printSessionLines(current *session.Session, result *session.UpResult) {
	fmt.Println()
	for _, inmate := range current.Inmates {
		if !inmateIsInBox(inmate.Command.Run, result) {
			continue
		}
		fmt.Printf("  prison %-8s session with %s\n", inmate.Name, inmate.Name)
	}
	fmt.Printf("  prison %-8s interactive shell\n", "shell")
}

func addSetupCommand(root *cobra.Command) {
	var force bool
	command := &cobra.Command{
		Use:   "setup",
		Short: "install this project's dependencies in the box",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			ctx, cancel := commandContext()
			defer cancel()
			current, err := openSession()
			if err != nil {
				return err
			}
			if err := current.RequireRunning(ctx); err != nil {
				return err
			}
			current.WarnAboutUntrustedConfiguration()
			return current.RunSetup(ctx, force)
		},
	}
	command.Flags().BoolVar(&force, "force", false,
		"run the setup commands even if they did not change")
	root.AddCommand(command)
}
