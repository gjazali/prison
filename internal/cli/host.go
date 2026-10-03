package cli

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"prison/internal/hostfw"
	"prison/internal/ui"
)

func init() {
	registerGroup(addHostCommands)
}

func addHostCommands(root *cobra.Command) {
	group := &cobra.Command{
		Use:   "host",
		Short: "set up this host for prison",
		Args: cobra.NoArgs,
	}
	group.AddCommand(
		newHostFirewallCommand(),
		newHostNFSHelperCommand(),
	)
	group.AddCommand(platformHostCommands()...)
	root.AddCommand(group)
}

func newHostFirewallCommand() *cobra.Command {
	var removeRules bool
	command := &cobra.Command{
		Use:   "firewall",
		Short: "block box access to the host",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runHostFirewall(command, removeRules)
		},
	}
	command.Flags().BoolVar(&removeRules, "remove", false,
		"remove the rules")
	return command
}

func runHostFirewall(command *cobra.Command, removeRules bool) error {
	ctx, cancel := commandContext()
	defer cancel()
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	if err := environment.Isolator.Require(ctx); err != nil {
		return err
	}
	network, err := environment.Isolator.Network(
		ctx, environment.Overrides.Network)
	if err != nil {
		return err
	}
	if network.SubnetV4 == "" {
		return fmt.Errorf("cannot read the subnet of the %s network. "+
			"Run `prison up`", environment.Overrides.Network)
	}
	spec := hostfw.Spec{
		SubnetV4:   network.SubnetV4,
		SubnetV6:   network.SubnetV6,
		BrokerPort: environment.Overrides.BrokerPort,
	}
	stagingDirectory := environment.Root.Path

	state, err := hostfw.Status(ctx, spec, stagingDirectory, hostfw.SystemRunner)
	if err != nil {
		return err
	}
	if state == hostfw.StateUnsupported {
		return fmt.Errorf("the firewall is not supported on %s",
			runtime.GOOS)
	}
	ui.Progress("the rules are %s", hostFirewallStateText(state))
	if removeRules {
		return removeHostFirewall(command, spec, stagingDirectory, state)
	}
	return installHostFirewall(command, spec, stagingDirectory)
}

func installHostFirewall(command *cobra.Command, spec hostfw.Spec,
	stagingDirectory string) error {
	ctx, cancel := commandContext()
	defer cancel()
	out := command.OutOrStdout()
	printIndentedBlock(out, strings.Join(hostfw.Describe(spec), "\n"))
	fmt.Fprintln(out)
	printIndentedBlock(out, hostfw.Rules(spec))
	fmt.Fprintln(out)
	if !ui.Confirm("install these rules with sudo?") {
		return ui.Exit(1, "canceled")
	}
	if err := hostfw.Install(ctx, spec, stagingDirectory,
		hostfw.SystemRunner, os.Stderr); err != nil {
		return err
	}
	ui.Progress("a box can reach this host only on port %d", spec.BrokerPort)
	return nil
}

func removeHostFirewall(command *cobra.Command, spec hostfw.Spec,
	stagingDirectory string, state hostfw.State) error {
	ctx, cancel := commandContext()
	defer cancel()
	if state == hostfw.StateMissing {
		ui.Progress("the rules are not installed")
		return nil
	}
	if !ui.Confirm("remove the rules with sudo?") {
		return ui.Exit(1, "canceled")
	}
	if err := hostfw.Remove(ctx, spec, stagingDirectory,
		hostfw.SystemRunner, os.Stderr); err != nil {
		return err
	}
	ui.Progress("done")
	return nil
}

func hostFirewallStateText(state hostfw.State) string {
	switch state {
	case hostfw.StateInstalled:
		return "installed and loaded"
	case hostfw.StateStale:
		return "out of date or not loaded"
	case hostfw.StateMissing:
		return "not installed"
	default:
		return string(state)
	}
}

func printIndentedBlock(w io.Writer, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	fmt.Fprintln(w, ui.Indent(text, "  "))
}
