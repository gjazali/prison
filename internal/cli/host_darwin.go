package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"prison/internal/session"
	"prison/internal/ui"
)

func platformHostCommands() []*cobra.Command {
	return []*cobra.Command{newHostDNSCommand(), newHostRouteCommand()}
}

func printHostnameSummary(current *session.Session) {
	ui.Summary("host", "%s", current.BoxName)
}

func newHostDNSCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "dns",
		Short: "register the local domain for boxes",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runHostDNS(command)
		},
	}
}

func newHostRouteCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "route",
		Short: "route the box network over the host bridge",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runHostRoute(command)
		},
	}
}

func runHostDNS(command *cobra.Command) error {
	ctx, cancel := commandContext()
	defer cancel()
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	if err := environment.Isolator.Require(ctx); err != nil {
		return err
	}
	domains := environment.Isolator.DNS()
	domain := environment.Overrides.Domain
	out := command.OutOrStdout()

	registered, err := domains.Exists(ctx, domain)
	if err != nil {
		return err
	}
	if registered {
		ui.Progress("`.%s` is registered", domain)
		printIndentedBlock(out, strings.Join(domains.RepairHint(domain), "\n"))
		return nil
	}

	ui.Progress("registering the `.%s` domain", domain)
	printIndentedBlock(out, strings.Join(domains.Describe(domain), "\n"))
	fmt.Fprintln(out)
	if !ui.Confirm("register the domain with sudo?") {
		return ui.Exit(1, "not registered")
	}
	if err := domains.Register(ctx, domain); err != nil {
		return err
	}
	ui.Progress("boxes resolve at <project>-<hash>.%s", domain)
	printIndentedBlock(out, strings.Join(domains.RepairHint(domain), "\n"))
	return nil
}

func runHostRoute(command *cobra.Command) error {
	ctx, cancel := commandContext()
	defer cancel()
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	if err := environment.Isolator.Require(ctx); err != nil {
		return err
	}
	routes := environment.Isolator.Route()
	network, err := environment.Isolator.Network(
		ctx, environment.Overrides.Network)
	if err != nil {
		return err
	}
	if network.Gateway == "" {
		return fmt.Errorf("the %s network has no bridge address. "+
			"Run `prison up`", environment.Overrides.Network)
	}

	installed, err := routes.Installed(ctx, network.Gateway)
	if err != nil {
		return err
	}
	if installed {
		ui.Progress("the route is in place")
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
	if !ui.Confirm("add the route with sudo?") {
		return ui.Exit(1, "canceled")
	}
	if err := routes.Install(ctx, network.Gateway); err != nil {
		return err
	}
	ui.Progress("done")
	ui.Warn("this route can be lost after a restart")
	return nil
}
