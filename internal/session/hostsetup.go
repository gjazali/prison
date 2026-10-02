package session

import (
	"context"
	"fmt"
	"os"

	"prison/internal/cage"
	"prison/internal/hostfw"
	"prison/internal/ui"
)

func (s *Session) ensureHostDNS(ctx context.Context) {
	domains := s.Cage.DNS()
	if !s.Cage.Capabilities().DNSDomain || domains == nil {
		return
	}
	domain := s.Overrides.Domain
	registered, err := domains.Exists(ctx, domain)
	if err != nil {
		ui.Warn("cannot read the `.%s` domain: %v", domain, err)
		return
	}
	if registered {
		return
	}
	ui.Progress("registering the `.%s` domain", domain)
	printHostSetupBlock(domains.Describe(domain))
	if err := domains.Register(ctx, domain); err != nil {
		ui.Warn("cannot register the `.%s` domain: %v", domain, err)
	}
}

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
		ui.Warn("cannot install the host packet filter rules: %v", err)
		return false
	}
	return true
}

func (s *Session) ensureHostRoute(
	ctx context.Context, network cage.NetworkInfo) {
	routes := s.Cage.Route()
	if !s.Cage.Capabilities().RouteRepair || routes == nil ||
		network.Gateway == "" {
		return
	}
	installed, err := routes.Installed(ctx, network.Gateway)
	if err != nil {
		ui.Warn("cannot read the route to the box network: %v", err)
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
		ui.Warn("cannot add the route to the box network: %v", err)
	}
}

func printHostSetupBlock(lines []string) {
	for _, line := range lines {
		fmt.Fprintf(os.Stderr, "    %s\n", line)
	}
}
