package session

import (
	"context"
	"fmt"
	"os"
	"strings"

	"prison/internal/hostfw"
	"prison/internal/isolator"
	"prison/internal/ui"
)

func (s *Session) ensureHostFirewall(
	ctx context.Context, network isolator.NetworkInfo) error {
	if !s.hostFirewallIsMissing(network) {
		return nil
	}
	spec := s.HostFirewallSpec(network)
	ui.Progress(
		"installing the host packet filter rules")
	printHostSetupBlock(hostfw.Describe())
	printHostSetupBlock(strings.Split(
		strings.TrimSuffix(hostfw.Rules(spec), "\n"), "\n"))
	err := hostfw.Install(
		ctx, spec, s.Root.Path, hostfw.SystemRunner, os.Stderr)
	if err != nil {
		return fmt.Errorf("cannot install the host packet filter rules: %w. "+
			"Run `prison host firewall`", err)
	}
	return nil
}

func printHostSetupBlock(lines []string) {
	for _, line := range lines {
		fmt.Fprintf(os.Stderr, "    %s\n", line)
	}
}
