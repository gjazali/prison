package session

import (
	"context"
	"fmt"
	"os"

	"prison/internal/cage"
	"prison/internal/hostfw"
	"prison/internal/ui"
)

// ensureHostDNS registers the local domain if the machine does not
// have it yet. Warns and continues if registration fails.
func (s *Session) ensureHostDNS(ctx context.Context) {
	domains := s.Cage.DNS()
	if !s.Cage.Capabilities().DNSDomain || domains == nil {
		return
	}
	domain := s.Overrides.Domain
	registered, err := domains.Exists(ctx, domain)
	if err != nil {
		ui.Warn("prison could not tell whether the `.%s` domain is "+
			"registered: %v", domain, err)
		return
	}
	if registered {
		return
	}
	ui.Progress("registering the `.%s` domain", domain)
	printHostSetupBlock(domains.Describe(domain))
	if err := domains.Register(ctx, domain); err != nil {
		ui.Warn("the `.%s` domain is not registered, so this box is reached "+
			"on its published ports: %v", domain, err)
		return
	}
	ui.Progress("registered `.%s`", domain)
}

// ensureHostFirewall installs host packet filter rules. Takes a
// context and network info. Returns true if rules are in place.
func (s *Session) ensureHostFirewall(
	ctx context.Context, network cage.NetworkInfo) bool {
	if !s.hostFirewallIsMissing(network) {
		return true
	}
	spec := s.hostFirewallSpec(network)
	ui.Progress(
		"installing the host packet filter rules")
	printHostSetupBlock(hostfw.Describe(spec))
	err := hostfw.Install(
		ctx, spec, s.Root.Path, hostfw.SystemRunner, os.Stderr)
	if err != nil {
		ui.Warn("the host packet filter rules are not in place: %v", err)
		return false
	}
	ui.Progress("installed; a box reaches this machine on port %d only",
		s.Overrides.BrokerPort)
	return true
}

// ensureHostRoute adds the route to the box network if missing.
// Warns and continues if the route cannot be installed.
func (s *Session) ensureHostRoute(
	ctx context.Context, network cage.NetworkInfo) {
	routes := s.Cage.Route()
	if !s.Cage.Capabilities().RouteRepair || routes == nil ||
		network.Gateway == "" {
		return
	}
	installed, err := routes.Installed(ctx, network.Gateway)
	if err != nil {
		ui.Warn("prison could not tell whether this machine reaches the "+
			"box network over its bridge: %v", err)
		return
	}
	if installed {
		return
	}
	ui.Progress("routing the box network over %s",
		network.Gateway)
	line, err := routes.Command(ctx, network.Gateway)
	if err == nil && line != "" {
		printHostSetupBlock([]string{line})
	}
	if err := routes.Install(ctx, network.Gateway); err != nil {
		ui.Warn("this machine does not reach the box network over its "+
			"bridge, so the box may not answer on its own address: %v", err)
		return
	}
	ui.Progress("routed")
}

// printHostSetupBlock takes lines and prints them to stderr, indented
// with four spaces.
func printHostSetupBlock(lines []string) {
	for _, line := range lines {
		fmt.Fprintf(os.Stderr, "    %s\n", line)
	}
}
