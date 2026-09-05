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

// init registers the host commands with the root.
func init() {
	registerGroup(addHostCommands)
}

// addHostCommands attaches `prison host` and its subcommands to the
// root command.
func addHostCommands(root *cobra.Command) {
	group := &cobra.Command{
		Use:   "host",
		Short: "set up what Prison needs on this machine",
		Args: cobra.NoArgs,
	}
	group.AddCommand(
		newHostDNSCommand(),
		newHostRouteCommand(),
		newHostFirewallCommand(),
	)
	root.AddCommand(group)
}

// newHostDNSCommand builds `prison host dns`. Returns the command.
func newHostDNSCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "dns",
		Short: "register the local domain boxes answer to",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runHostDNS(command)
		},
	}
}

// newHostRouteCommand builds `prison host route`. Returns the
// command.
func newHostRouteCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "route",
		Short: "put the box network back on the host's bridge",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runHostRoute(command)
		},
	}
}

// newHostFirewallCommand builds `prison host firewall`. Returns the
// command.
func newHostFirewallCommand() *cobra.Command {
	var removeRules bool
	command := &cobra.Command{
		Use:   "firewall",
		Short: "hold the box network shut from the host side",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runHostFirewall(command, removeRules)
		},
	}
	command.Flags().BoolVar(&removeRules, "remove", false,
		"take the rules back out, and stop loading them at boot")
	return command
}

// runHostDNS registers the local DNS domain for boxes. Does nothing
// if already registered. Returns an error if the cage does not
// support hostnames.
func runHostDNS(command *cobra.Command) error {
	ctx, cancel := commandContext()
	defer cancel()
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	if err := environment.Cage.Require(ctx); err != nil {
		return err
	}
	domains := environment.Cage.DNS()
	if !environment.Cage.Capabilities().DNSDomain || domains == nil {
		return fmt.Errorf("the %s cage does not register hostnames, so "+
			"boxes are reached on published ports instead",
			environment.Cage.Name())
	}
	domain := environment.Overrides.Domain
	out := command.OutOrStdout()

	registered, err := domains.Exists(ctx, domain)
	if err != nil {
		return err
	}
	if registered {
		ui.Progress("`.%s` is already registered", domain)
		printIndentedBlock(out, strings.Join(domains.RepairHint(domain), "\n"))
		return nil
	}

	ui.Progress("registering the `.%s` domain", domain)
	printIndentedBlock(out, strings.Join(domains.Describe(domain), "\n"))
	fmt.Fprintln(out)
	if !ui.Confirm("register the domain? this asks for sudo") {
		return ui.Exit(1, "left unregistered")
	}
	if err := domains.Register(ctx, domain); err != nil {
		return err
	}
	ui.Progress("done; boxes resolve at <project>-<hash>.%s", domain)
	printIndentedBlock(out, strings.Join(domains.RepairHint(domain), "\n"))
	return nil
}

// runHostRoute adds the route to the box network if it is missing.
// Returns an error if the cage does not support route repair.
func runHostRoute(command *cobra.Command) error {
	ctx, cancel := commandContext()
	defer cancel()
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	if err := environment.Cage.Require(ctx); err != nil {
		return err
	}
	routes := environment.Cage.Route()
	if !environment.Cage.Capabilities().RouteRepair || routes == nil {
		return fmt.Errorf("the %s cage does not reach boxes over a bridge, "+
			"so there is no route to add", environment.Cage.Name())
	}
	network, err := environment.Cage.Network(ctx, environment.Overrides.Network)
	if err != nil {
		return err
	}
	if network.Gateway == "" {
		return fmt.Errorf("the %s network has no bridge address; the "+
			"bridge exists only while a box does, so run `prison up` first",
			environment.Overrides.Network)
	}

	installed, err := routes.Installed(ctx, network.Gateway)
	if err != nil {
		return err
	}
	if installed {
		ui.Progress("the box network already routes over the bridge")
		return nil
	}

	routeCommand, err := routes.Command(ctx, network.Gateway)
	if err != nil {
		return err
	}
	out := command.OutOrStdout()
	ui.Progress("routing the box network over %s", network.Gateway)
	printIndentedBlock(out, routeCommand)
	fmt.Fprintln(out)
	if !ui.Confirm("add the route? this asks for sudo") {
		return ui.Exit(1, "left alone")
	}
	if err := routes.Install(ctx, network.Gateway); err != nil {
		return err
	}
	ui.Progress("done")
	ui.Warn("this route may not be persistent")
	return nil
}

// runHostFirewall installs or removes prison's packet filter rules.
// Takes the command and whether to remove rules. Returns an error
// if the cage does not support a host firewall.
func runHostFirewall(command *cobra.Command, removeRules bool) error {
	ctx, cancel := commandContext()
	defer cancel()
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	if err := environment.Cage.Require(ctx); err != nil {
		return err
	}
	if !environment.Cage.Capabilities().HostFirewall {
		return fmt.Errorf("the %s cage does not put boxes on a subnet "+
			"Prison can filter, so there is nothing to install",
			environment.Cage.Name())
	}
	network, err := environment.Cage.Network(ctx, environment.Overrides.Network)
	if err != nil {
		return err
	}
	if network.SubnetV4 == "" {
		return fmt.Errorf("could not read the %s network's subnet; "+
			"`prison up` creates the network", environment.Overrides.Network)
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
		return fmt.Errorf("Prison has no packet filter rules for %s",
			runtime.GOOS)
	}
	ui.Progress("Prison's rules are %s", hostFirewallStateText(state))
	if removeRules {
		return removeHostFirewall(command, spec, stagingDirectory, state)
	}
	return installHostFirewall(command, spec, stagingDirectory)
}

// installHostFirewall shows the rules, asks for confirmation, and
// installs them. Declining exits 1.
func installHostFirewall(command *cobra.Command, spec hostfw.Spec,
	stagingDirectory string) error {
	ctx, cancel := commandContext()
	defer cancel()
	out := command.OutOrStdout()
	printIndentedBlock(out, strings.Join(hostfw.Describe(spec), "\n"))
	fmt.Fprintln(out)
	printIndentedBlock(out, hostfw.Rules(spec))
	fmt.Fprintln(out)
	if !ui.Confirm("install these rules? this asks for sudo") {
		return ui.Exit(1, "left alone")
	}
	if err := hostfw.Install(ctx, spec, stagingDirectory,
		hostfw.SystemRunner, os.Stderr); err != nil {
		return err
	}
	ui.Progress("done; a box now reaches this machine on port %d only",
		spec.BrokerPort)
	ui.Progress("the system loads them again at every boot")
	return nil
}

// removeHostFirewall removes prison's rules after asking. Does
// nothing if no rules are installed.
func removeHostFirewall(command *cobra.Command, spec hostfw.Spec,
	stagingDirectory string, state hostfw.State) error {
	ctx, cancel := commandContext()
	defer cancel()
	if state == hostfw.StateMissing {
		ui.Progress("there is nothing of Prison's to remove")
		return nil
	}
	if !ui.Confirm("remove Prison's rules? this asks for sudo") {
		return ui.Exit(1, "left alone")
	}
	if err := hostfw.Remove(ctx, spec, stagingDirectory,
		hostfw.SystemRunner, os.Stderr); err != nil {
		return err
	}
	ui.Progress("done")
	return nil
}

// hostFirewallStateText returns a readable description of a firewall
// state.
func hostFirewallStateText(state hostfw.State) string {
	switch state {
	case hostfw.StateInstalled:
		return "installed and loaded"
	case hostfw.StateStale:
		return "installed but no longer match this network, or are not loaded"
	case hostfw.StateMissing:
		return "not installed"
	default:
		return string(state)
	}
}

// printIndentedBlock writes text indented two spaces. Writes nothing
// if text is empty.
func printIndentedBlock(w io.Writer, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	fmt.Fprintln(w, ui.Indent(text, "  "))
}
