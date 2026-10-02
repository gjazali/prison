package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"prison/internal/cage"
	"prison/internal/cages"
	"prison/internal/session"
	"prison/internal/ui"
)

func init() {
	registerGroup(addCageCommands)
}

func addCageCommands(root *cobra.Command) {
	group := &cobra.Command{
		Use:   "cage",
		Short: "manage the backends a box can run on",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runCageList(command)
		},
	}
	group.AddCommand(
		newCageListCommand(),
		newCageShowCommand(),
		newCageVerifyCommand(),
	)
	root.AddCommand(group)
}

func newCageListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list cages and their isolation",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runCageList(command)
		},
	}
}

func newCageShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show [name]",
		Short: "show a cage's details",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			requestedName := ""
			if len(arguments) == 1 {
				requestedName = arguments[0]
			}
			return runCageShow(command, requestedName)
		},
	}
}

func newCageVerifyCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "make sure that this box is isolated",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runCageVerify(command)
		},
	}
}

func runCageList(command *cobra.Command) error {
	rows := [][]string{{"NAME", "ISOLATION", "DESCRIPTION"}}
	for _, name := range cages.Names() {
		selected, err := cages.Lookup(name, cages.Options{})
		if err != nil {
			return err
		}
		rows = append(rows, []string{
			name,
			selected.Capabilities().Isolation,
			selected.Description(),
		})
	}
	return ui.Table(command.OutOrStdout(), rows)
}

func runCageShow(command *cobra.Command, requestedName string) error {
	selected, err := resolveCageByName(requestedName)
	if err != nil {
		return err
	}
	out := command.OutOrStdout()
	capabilities := selected.Capabilities()
	printCageDetail(out, "name", "%s", selected.Name())
	printCageDetail(out, "description", "%s", selected.Description())
	printCageDetail(out, "available", "%s", yesOrNoText(selected.Available()))
	printCageDetail(out, "isolation", "%s", capabilities.Isolation)
	printCageDetail(out, "guest addresses", "%s",
		yesOrNoText(capabilities.GuestAddresses))
	printCageDetail(out, "guest hostnames", "%s",
		yesOrNoText(capabilities.GuestHostnames))
	printCageDetail(out, "dns domain", "%s",
		yesOrNoText(capabilities.DNSDomain))
	printCageDetail(out, "route repair", "%s",
		yesOrNoText(capabilities.RouteRepair))
	printCageDetail(out, "host-only network", "%s",
		yesOrNoText(capabilities.HostOnlyNetwork))
	printCageDetail(out, "host firewall", "%s",
		yesOrNoText(capabilities.HostFirewall))
	return nil
}

func resolveCageByName(requestedName string) (cage.Cage, error) {
	if requestedName != "" {
		return cages.Lookup(requestedName, cages.Options{})
	}
	environment, err := loadEnvironment()
	if err != nil {
		return nil, err
	}
	return environment.Cage, nil
}

func runCageVerify(command *cobra.Command) error {
	ctx, cancel := commandContext()
	defer cancel()
	current, err := openSession()
	if err != nil {
		return err
	}
	if err := current.Cage.Require(ctx); err != nil {
		return err
	}
	if err := current.RequireRunning(ctx); err != nil {
		return err
	}
	out := command.OutOrStdout()
	ui.Progress("verifying %s against %s", current.Cage.Name(), current.BoxName)
	fmt.Fprintf(out, "  isolation: %s\n\n",
		current.Cage.Capabilities().Isolation)

	verification := &cageVerification{out: out}
	checkSessionUserIsNotRoot(ctx, current, verification)
	checkWorkspaceIsTheProject(ctx, current, verification)
	checkGitHooksAreReadOnly(ctx, current, verification)
	checkHostFilesystemIsNotVisible(ctx, current, verification)
	checkDirectEgressIsRefused(ctx, current, verification)
	checkBrokerIsReachable(ctx, current, verification)
	checkFirewallRulesAreOutOfReach(ctx, current, verification)
	checkPublishedPortsAreLoopbackOnly(current, verification)
	checkShadowedDirectoriesAreIsolated(ctx, current, verification)

	fmt.Fprintln(out)
	if verification.failures == 0 {
		ui.Progress("the box is isolated")
		return nil
	}
	return ui.Exit(1, "%d isolation tests failed", verification.failures)
}

type cageVerification struct {
	out      io.Writer
	failures int
}

func (verification *cageVerification) pass(
	property, format string, arguments ...any,
) {
	verification.report("PASS", property, format, arguments...)
}

func (verification *cageVerification) fail(
	property, format string, arguments ...any,
) {
	verification.failures++
	verification.report("FAIL", property, format, arguments...)
}

func (verification *cageVerification) skip(
	property, format string, arguments ...any,
) {
	verification.report("SKIP", property, format, arguments...)
}

func (verification *cageVerification) report(
	outcome, property, format string, arguments ...any,
) {
	fmt.Fprintf(verification.out, "  %s  %-20s %s\n",
		outcome, property, fmt.Sprintf(format, arguments...))
}

func checkSessionUserIsNotRoot(ctx context.Context,
	current *session.Session, verification *cageVerification) {
	status, output, err := current.RunQuiet(ctx, "id", "-u")
	if err != nil {
		verification.fail("session user",
			"cannot run a command in the box: %v", err)
		return
	}
	if status != 0 {
		verification.fail("session user",
			"cannot get the user ID: %s", firstLineOfOutput(output))
		return
	}
	identifier := strings.TrimSpace(output)
	if identifier == "0" {
		verification.fail("session user",
			"the session user is root")
		return
	}
	verification.pass("session user", "uid %s", identifier)
}

func checkWorkspaceIsTheProject(ctx context.Context,
	current *session.Session, verification *cageVerification) {
	guestFile := session.WorkspaceDir + "/" + verifyMarkerName()
	status, output, err := runInBoxShell(ctx, current,
		"touch "+quoteForBoxShell(guestFile)+" && rm -f "+
			quoteForBoxShell(guestFile))
	if err != nil || status != 0 {
		verification.fail("project mount",
			"%s is not writable: %s",
			session.WorkspaceDir, firstLineOfOutput(output))
		return
	}

	hostFile := filepath.Join(current.Directory, verifyMarkerName())
	if err := os.WriteFile(hostFile, nil, 0o600); err != nil {
		verification.skip("project mount",
			"%s is writable, but cannot write to %s: %v",
			session.WorkspaceDir, current.Directory, err)
		return
	}
	defer os.Remove(hostFile)
	status, _, err = runInBoxShell(ctx, current,
		"test -e "+quoteForBoxShell(guestFile))
	if err != nil || status != 0 {
		verification.fail("project mount",
			"%s is not %s",
			session.WorkspaceDir, current.Directory)
		return
	}
	verification.pass("project mount", "%s is writable and is %s",
		session.WorkspaceDir, current.Directory)
}

func checkGitHooksAreReadOnly(ctx context.Context,
	current *session.Session, verification *cageVerification) {
	if !directoryExistsOnHost(filepath.Join(current.Directory, ".git", "hooks")) {
		verification.skip("git hooks",
			"this project has no git hooks")
		return
	}
	guestFile := session.WorkspaceDir + "/.git/hooks/" + verifyMarkerName()
	quoted := quoteForBoxShell(guestFile)
	status, _, err := runInBoxShell(ctx, current, "touch "+quoted)
	if err == nil && status == 0 {
		runInBoxShell(ctx, current, "rm -f "+quoted)
		verification.fail("git hooks",
			"writable from the box")
		return
	}
	if !sudoIsGrantedInBox(current) {
		verification.pass("git hooks", "read-only from the box")
		return
	}
	status, _, err = runInBoxShell(ctx, current, "sudo -n touch "+quoted)
	if err == nil && status == 0 {
		runInBoxShell(ctx, current, "sudo -n rm -f "+quoted)
		verification.fail("git hooks",
			"writable by root in the box")
		return
	}
	verification.pass("git hooks", "read-only from the box, also for root")
}

func checkHostFilesystemIsNotVisible(ctx context.Context,
	current *session.Session, verification *cageVerification) {
	if current.HomeDir == "" {
		verification.skip("host filesystem",
			"this host has no home directory")
		return
	}
	status, _, err := runInBoxShell(ctx, current,
		"test -e "+quoteForBoxShell(current.HomeDir))
	if err == nil && status == 0 {
		verification.fail("host filesystem", "%s exists inside the box",
			current.HomeDir)
		return
	}
	verification.pass("host filesystem", "%s is not in the box", current.HomeDir)
}

func checkDirectEgressIsRefused(ctx context.Context,
	current *session.Session, verification *cageVerification) {
	status, output, err := runInBoxShell(ctx, current,
		"command -v curl >/dev/null || exit 99; "+
			"curl --max-time 3 --noproxy '*' https://1.1.1.1")
	if err != nil {
		verification.fail("direct egress",
			"cannot run the test in the box: %v", err)
		return
	}
	switch {
	case status == 99:
		verification.skip("direct egress", "`curl` is not in the box")
	case status == 0:
		verification.fail("direct egress",
			"the box reached 1.1.1.1 without the broker: %s",
			firstLineOfOutput(output))
	default:
		verification.pass("direct egress", "refused")
	}
}

func checkBrokerIsReachable(ctx context.Context,
	current *session.Session, verification *cageVerification) {
	status, output, err := current.RunQuiet(ctx, session.GuestBinary, "ready")
	if err != nil {
		verification.fail("broker",
			"cannot run the test in the box: %v", err)
		return
	}
	if status != 0 {
		verification.fail("broker",
			"the guest cannot reach the broker: %s",
			firstLineOfOutput(output))
		return
	}
	verification.pass("broker", "the guest can reach it")
}

func checkFirewallRulesAreOutOfReach(ctx context.Context,
	current *session.Session, verification *cageVerification) {
	status, _, err := runInBoxShell(ctx, current,
		"command -v iptables >/dev/null || exit 99; iptables -F")
	if err != nil {
		verification.fail("in-box firewall",
			"cannot run the test in the box: %v", err)
		return
	}
	switch {
	case status == 99:
		verification.skip("in-box firewall",
			"`iptables` is not in the box")
	case status == 0:
		verification.fail("in-box firewall",
			"the session user can flush the rules")
	default:
		verification.pass("in-box firewall", "the session user cannot change it")
	}
}

// checkPublishedPortsAreLoopbackOnly binds each port on the other host
// addresses. A failed bind means that the port is exposed there.
func checkPublishedPortsAreLoopbackOnly(current *session.Session,
	verification *cageVerification) {
	ports := recordedHostPorts(current)
	if len(ports) == 0 {
		verification.skip("published ports", "this box publishes no ports")
		return
	}
	addresses := hostAddressesBeyondLoopback()
	if len(addresses) == 0 {
		verification.skip("published ports",
			"this host has only a loopback address")
		return
	}
	var held []string
	for _, port := range ports {
		for _, address := range addresses {
			target := net.JoinHostPort(address, strconv.Itoa(port))
			listener, err := net.Listen("tcp", target)
			if err != nil {
				held = append(held, target)
				continue
			}
			listener.Close()
		}
	}
	if len(held) > 0 {
		verification.fail("published ports",
			"exposed on %s", strings.Join(held, ", "))
		return
	}
	verification.pass("published ports", "%d on the loopback address only",
		len(ports))
}

func checkShadowedDirectoriesAreIsolated(ctx context.Context,
	current *session.Session, verification *cageVerification) {
	paths := current.ShadowPaths()
	if len(paths) == 0 {
		verification.skip("shadowed paths", "none")
		return
	}
	checked := 0
	var leaked []string
	for _, path := range paths {
		guestFile := session.WorkspaceDir + "/" + path + "/" + verifyMarkerName()
		quoted := quoteForBoxShell(guestFile)
		status, _, err := runInBoxShell(ctx, current, "touch "+quoted)
		if err != nil || status != 0 {
			continue
		}
		checked++
		hostFile := filepath.Join(current.Directory, path, verifyMarkerName())
		if _, err := os.Lstat(hostFile); err == nil {
			leaked = append(leaked, path)
			os.Remove(hostFile)
		}
		runInBoxShell(ctx, current, "rm -f "+quoted)
	}
	switch {
	case checked == 0:
		verification.skip("shadowed paths",
			"cannot write to them in the box")
	case len(leaked) > 0:
		verification.fail("shadowed paths",
			"the host can see box files in %s",
			strings.Join(leaked, ", "))
	default:
		verification.pass("shadowed paths", "%d are isolated", checked)
	}
}

func runInBoxShell(ctx context.Context, current *session.Session,
	snippet string) (int, string, error) {
	return current.RunQuiet(ctx, "bash", "-c", snippet)
}

func recordedHostPorts(current *session.Session) []int {
	if current.Record == nil || current.Record.Box == nil {
		return nil
	}
	ports := make([]int, 0, len(current.Record.Box.Ports))
	for _, mapping := range current.Record.Box.Ports {
		ports = append(ports, mapping[0])
	}
	return ports
}

func hostAddressesBeyondLoopback() []string {
	interfaceAddresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var addresses []string
	for _, interfaceAddress := range interfaceAddresses {
		network, ok := interfaceAddress.(*net.IPNet)
		if !ok || network.IP.To4() == nil {
			continue
		}
		if network.IP.IsLoopback() || network.IP.IsLinkLocalUnicast() {
			continue
		}
		addresses = append(addresses, network.IP.String())
	}
	return addresses
}

func sudoIsGrantedInBox(current *session.Session) bool {
	return current.Record != nil && current.Record.Box != nil &&
		current.Record.Box.Sudo
}

func verifyMarkerName() string {
	return fmt.Sprintf(".prison-verify-%d", os.Getpid())
}

func quoteForBoxShell(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}

func firstLineOfOutput(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return "no output"
}

func directoryExistsOnHost(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func yesOrNoText(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func printCageDetail(w io.Writer, label, format string, arguments ...any) {
	fmt.Fprintf(w, "%-18s %s\n", label, fmt.Sprintf(format, arguments...))
}
