package session

import (
	"context"
	"fmt"
	"os"

	"prison/internal/hostfw"
	"prison/internal/isolator"
	"prison/internal/ui"
)

func (s *Session) ensureHostFirewall(
	ctx context.Context, network isolator.NetworkInfo) bool {
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

func printHostSetupBlock(lines []string) {
	for _, line := range lines {
		fmt.Fprintf(os.Stderr, "    %s\n", line)
	}
}
