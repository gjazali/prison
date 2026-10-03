package session

import (
	"context"

	"prison/internal/isolator"
	"prison/internal/ui"
)

func (s *Session) ensureHostDNS(ctx context.Context) {
	domains := s.Isolator.DNS()
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

func (s *Session) ensureHostRoute(
	ctx context.Context, network isolator.NetworkInfo) {
	if network.Gateway == "" {
		return
	}
	routes := s.Isolator.Route()
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
