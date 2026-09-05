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

// addCageCommands adds the `prison cage` command group to root.
// Running `prison cage` with no subcommand lists all cages.
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

// newCageListCommand builds the `prison cage list` command.
// It prints every cage compiled into this build.
func newCageListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "every cage this build holds, and the isolation it gives",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runCageList(command)
		},
	}
}

// newCageShowCommand builds the `prison cage show [name]` command.
// It prints one cage's details and capabilities.
func newCageShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show [name]",
		Short: "a cage's description and what it can do",
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

// newCageVerifyCommand builds the `prison cage verify` command.
// It tests a running box against every property prison claims.
func newCageVerifyCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "check that this box holds what Prison claims",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runCageVerify(command)
		},
	}
}

// runCageList prints a table of all compiled-in cages. Takes command
// for its output writer. Returns an error on failure.
func runCageList(command *cobra.Command) error {
	rows := [][]string{{"NAME", "ISOLATION", "DESCRIPTION"}}
	for _, name := range cages.Names() {
		selected, err := cages.Lookup(name)
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

// runCageShow prints one cage's details and capabilities. Takes
// command for output and requestedName as the cage to show. An empty
// name uses the environment's default cage.
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

// resolveCageByName takes a cage name and returns it. An empty name
// returns the environment's default cage.
func resolveCageByName(requestedName string) (cage.Cage, error) {
	if requestedName != "" {
		return cages.Lookup(requestedName)
	}
	environment, err := loadEnvironment()
	if err != nil {
		return nil, err
	}
	return environment.Cage, nil
}

// runCageVerify checks every claimed property on a running box and
// prints results. Takes command for output. Exits 1 if any property
// fails.
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
		return fmt.Errorf("%w, and these are properties of a running box, "+
			"not a manifest", err)
	}
	out := command.OutOrStdout()
	ui.Progress("verifying %s against %s", current.Cage.Name(), current.BoxName)
	fmt.Fprintf(out, "  the cage declares %s isolation\n\n",
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
		ui.Progress("every property holds")
		return nil
	}
	return ui.Exit(1, "%d of the properties Prison claims do not hold on "+
		"this box",
		verification.failures)
}

// cageVerification tracks verification results and counts failures.
type cageVerification struct {
	out      io.Writer
	failures int
}

// pass records a property that holds.
func (verification *cageVerification) pass(
	property, format string, arguments ...any,
) {
	verification.report("PASS", property, format, arguments...)
}

// fail records a property that does not hold and increments the
// failure count.
func (verification *cageVerification) fail(
	property, format string, arguments ...any,
) {
	verification.failures++
	verification.report("FAIL", property, format, arguments...)
}

// skip records a property that could not be checked.
func (verification *cageVerification) skip(
	property, format string, arguments ...any,
) {
	verification.report("SKIP", property, format, arguments...)
}

// report writes one result line with the outcome, property name, and
// detail.
func (verification *cageVerification) report(
	outcome, property, format string, arguments ...any,
) {
	fmt.Fprintf(verification.out, "  %s  %-20s %s\n",
		outcome, property, fmt.Sprintf(format, arguments...))
}

// checkSessionUserIsNotRoot checks that the box session user is not
// root.
func checkSessionUserIsNotRoot(ctx context.Context,
	current *session.Session, verification *cageVerification) {
	status, output, err := current.RunQuiet(ctx, "id", "-u")
	if err != nil {
		verification.fail("session user",
			"the box takes no commands at all: %v", err)
		return
	}
	if status != 0 {
		verification.fail("session user",
			"the box could not say who it runs as: %s", firstLineOfOutput(output))
		return
	}
	identifier := strings.TrimSpace(output)
	if identifier == "0" {
		verification.fail("session user",
			"root, so a session can undo the box's own rules")
		return
	}
	verification.pass("session user", "uid %s", identifier)
}

// checkWorkspaceIsTheProject checks that /workspace is writable and
// maps to the real project directory.
func checkWorkspaceIsTheProject(ctx context.Context,
	current *session.Session, verification *cageVerification) {
	guestFile := session.WorkspaceDir + "/" + verifyMarkerName()
	status, output, err := runInBoxShell(ctx, current,
		"touch "+quoteForBoxShell(guestFile)+" && rm -f "+
			quoteForBoxShell(guestFile))
	if err != nil || status != 0 {
		verification.fail("project mount",
			"%s is not writable, so nothing a session does reaches the host: %s",
			session.WorkspaceDir, firstLineOfOutput(output))
		return
	}

	hostFile := filepath.Join(current.Directory, verifyMarkerName())
	if err := os.WriteFile(hostFile, nil, 0o600); err != nil {
		verification.skip("project mount",
			"%s is writable, but nothing could be left in %s to compare "+
				"against: %v", session.WorkspaceDir, current.Directory, err)
		return
	}
	defer os.Remove(hostFile)
	status, _, err = runInBoxShell(ctx, current,
		"test -e "+quoteForBoxShell(guestFile))
	if err != nil || status != 0 {
		verification.fail("project mount",
			"%s is writable but is not %s, so a session works on a copy",
			session.WorkspaceDir, current.Directory)
		return
	}
	verification.pass("project mount", "%s is writable and is %s",
		session.WorkspaceDir, current.Directory)
}

// checkGitHooksAreReadOnly checks that the box cannot write git hooks.
func checkGitHooksAreReadOnly(ctx context.Context,
	current *session.Session, verification *cageVerification) {
	if !directoryExistsOnHost(filepath.Join(current.Directory, ".git", "hooks")) {
		verification.skip("git hooks",
			"this project is not a repository with hooks to protect")
		return
	}
	guestFile := session.WorkspaceDir + "/.git/hooks/" + verifyMarkerName()
	quoted := quoteForBoxShell(guestFile)
	status, _, err := runInBoxShell(ctx, current, "touch "+quoted)
	if err == nil && status == 0 {
		runInBoxShell(ctx, current, "rm -f "+quoted)
		verification.fail("git hooks",
			"writable, so a hook written in the box runs on the host")
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
			"root in the box can write them, so the mount is not enforced outside")
		return
	}
	verification.pass("git hooks", "read-only from the box, root there included")
}

// checkHostFilesystemIsNotVisible checks that the host home directory
// is not visible inside the box.
func checkHostFilesystemIsNotVisible(ctx context.Context,
	current *session.Session, verification *cageVerification) {
	if current.HomeDir == "" {
		verification.skip("host filesystem",
			"this host has no home directory to look for")
		return
	}
	status, _, err := runInBoxShell(ctx, current,
		"test -e "+quoteForBoxShell(current.HomeDir))
	if err == nil && status == 0 {
		verification.fail("host filesystem", "%s exists inside the box",
			current.HomeDir)
		return
	}
	verification.pass("host filesystem", "%s is not there", current.HomeDir)
}

// checkDirectEgressIsRefused checks that the box cannot open a direct
// outbound connection without the broker.
func checkDirectEgressIsRefused(ctx context.Context,
	current *session.Session, verification *cageVerification) {
	status, output, err := runInBoxShell(ctx, current,
		"command -v curl >/dev/null || exit 99; "+
			"curl --max-time 3 --noproxy '*' https://1.1.1.1")
	if err != nil {
		verification.fail("direct egress",
			"the box could not be asked to try: %v", err)
		return
	}
	switch {
	case status == 99:
		verification.skip("direct egress", "the box has no `curl` to try it with")
	case status == 0:
		verification.fail("direct egress",
			"the box reached 1.1.1.1 without the broker: %s",
			firstLineOfOutput(output))
	default:
		verification.pass("direct egress", "refused")
	}
}

// checkBrokerIsReachable checks that the guest agent can reach the
// broker.
func checkBrokerIsReachable(ctx context.Context,
	current *session.Session, verification *cageVerification) {
	status, output, err := current.RunQuiet(ctx, session.GuestBinary, "ready")
	if err != nil {
		verification.fail("broker",
			"the box could not be asked whether it is ready: %v", err)
		return
	}
	if status != 0 {
		verification.fail("broker",
			"the guest cannot reach it, so nothing installs: %s",
			firstLineOfOutput(output))
		return
	}
	verification.pass("broker", "the guest reaches it")
}

// checkFirewallRulesAreOutOfReach checks that the session user cannot
// flush the in-box firewall rules.
func checkFirewallRulesAreOutOfReach(ctx context.Context,
	current *session.Session, verification *cageVerification) {
	status, _, err := runInBoxShell(ctx, current,
		"command -v iptables >/dev/null || exit 99; iptables -F")
	if err != nil {
		verification.fail("in-box firewall",
			"the box could not be asked to try: %v", err)
		return
	}
	switch {
	case status == 99:
		verification.skip("in-box firewall",
			"the box has no `iptables` to try it with")
	case status == 0:
		verification.fail("in-box firewall",
			"the session user flushed the box's own rules")
	default:
		verification.pass("in-box firewall", "out of the session user's reach")
	}
}

// checkPublishedPortsAreLoopbackOnly checks that published ports are
// bound only to the loopback address.
func checkPublishedPortsAreLoopbackOnly(current *session.Session,
	verification *cageVerification) {
	ports := recordedHostPorts(current)
	if len(ports) == 0 {
		verification.skip("published ports", "this box publishes none")
		return
	}
	addresses := hostAddressesBeyondLoopback()
	if len(addresses) == 0 {
		verification.skip("published ports",
			"this host has no address beyond the loopback interface to try")
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
			"something already holds %s, so they are not on the loopback "+
				"address alone", strings.Join(held, ", "))
		return
	}
	verification.pass("published ports", "%d on the loopback address only",
		len(ports))
}

// checkShadowedDirectoriesAreIsolated checks that files written in
// shadowed directories inside the box are not visible on the host.
func checkShadowedDirectoriesAreIsolated(ctx context.Context,
	current *session.Session, verification *cageVerification) {
	paths := current.ShadowPaths()
	if len(paths) == 0 {
		verification.skip("shadowed paths", "this project has none")
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
			"none of them could be written in the box to compare against")
	case len(leaked) > 0:
		verification.fail("shadowed paths",
			"the host sees what the box wrote in %s",
			strings.Join(leaked, ", "))
	default:
		verification.pass("shadowed paths", "%d are the box's own", checked)
	}
}

// runInBoxShell runs a shell snippet in the box. Takes ctx, the
// session, and the snippet. Returns the exit status, output, and any
// error.
func runInBoxShell(ctx context.Context, current *session.Session,
	snippet string) (int, string, error) {
	return current.RunQuiet(ctx, "bash", "-c", snippet)
}

// recordedHostPorts returns the host ports the box was created with.
// Takes the session. Returns nil if no ports were recorded.
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

// hostAddressesBeyondLoopback returns this machine's non-loopback,
// non-link-local IPv4 addresses.
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

// sudoIsGrantedInBox returns true if the box was created with sudo.
func sudoIsGrantedInBox(current *session.Session) bool {
	return current.Record != nil && current.Record.Box != nil &&
		current.Record.Box.Sudo
}

// verifyMarkerName returns a unique marker file name for this process.
func verifyMarkerName() string {
	return fmt.Sprintf(".prison-verify-%d", os.Getpid())
}

// quoteForBoxShell wraps text in single quotes for safe shell use.
// Takes a string and returns it quoted.
func quoteForBoxShell(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}

// firstLineOfOutput returns the first non-empty line from output.
// Returns "it said nothing" if the output is blank.
func firstLineOfOutput(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return "it said nothing"
}

// directoryExistsOnHost returns true if path is a directory on the
// host.
func directoryExistsOnHost(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// yesOrNoText returns "yes" or "no" for a boolean value.
func yesOrNoText(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

// printCageDetail writes one `label  value` line with the label padded,
// the shape `prison cage show` prints every field in.
func printCageDetail(w io.Writer, label, format string, arguments ...any) {
	fmt.Fprintf(w, "%-18s %s\n", label, fmt.Sprintf(format, arguments...))
}
