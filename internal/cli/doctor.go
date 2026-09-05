package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"prison/internal/broker/control"
	"prison/internal/cage"
	"prison/internal/config"
	"prison/internal/hostfw"
	"prison/internal/session"
	"prison/internal/state"
	"prison/internal/vault"
)

// doctorLabelWidth is the column width for detail line labels.
const doctorLabelWidth = 14

// init registers the doctor command with the root.
func init() {
	registerGroup(addDoctorCommand)
}

// addDoctorCommand attaches `prison doctor` to the root command.
func addDoctorCommand(root *cobra.Command) {
	root.AddCommand(&cobra.Command{
		Use:   "doctor",
		Short: "report what Prison sees on this host",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runDoctor(command)
		},
	})
}

// runDoctor prints every section of the doctor report. Returns an
// error only if the environment cannot be loaded.
func runDoctor(command *cobra.Command) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	ctx, cancel := commandContext()
	defer cancel()
	report := &doctorReport{
		out:         command.OutOrStdout(),
		environment: environment,
	}
	report.reportPrison()
	report.reportCage(ctx)
	network := report.reportNetwork(ctx)
	report.reportBroker(ctx)
	report.reportFirewall(ctx, network)
	report.reportVault()
	report.reportProjects()
	report.reportSettings()
	report.reportLegacyLayout()
	return nil
}

// doctorReport writes one report to out. Holds the loaded environment
// and tracks whether a section heading has been written.
type doctorReport struct {
	out            io.Writer
	environment    *session.Environment
	sectionWritten bool
}

// heading starts a new section with the given name.
func (report *doctorReport) heading(name string) {
	if report.sectionWritten {
		fmt.Fprintln(report.out)
	}
	report.sectionWritten = true
	fmt.Fprintln(report.out, name)
}

// detail writes one labelled line under the current heading.
func (report *doctorReport) detail(
	label, format string, arguments ...any,
) {
	fmt.Fprintf(report.out, "  %-*s%s\n", doctorLabelWidth, label,
		fmt.Sprintf(format, arguments...))
}

// note writes one unlabelled line under the current heading.
func (report *doctorReport) note(format string, arguments ...any) {
	fmt.Fprintf(report.out, "  %s\n", fmt.Sprintf(format, arguments...))
}

// command writes an indented command line under the current heading.
func (report *doctorReport) command(text string) {
	fmt.Fprintf(report.out, "    %s\n", text)
}

// blank writes an empty line inside a section.
func (report *doctorReport) blank() {
	fmt.Fprintln(report.out)
}

// reportPrison writes the version, executable, state root, and
// host-wide settings.
func (report *doctorReport) reportPrison() {
	overrides := report.environment.Overrides
	report.heading("prison")
	report.detail("version", "%s", report.environment.Version)
	report.detail("executable", "%s", report.environment.Executable)
	report.detail("state root", "%s", report.environment.Root.Path)
	report.detail("domain", "%s", overrides.Domain)
	report.detail("network", "%s", overrides.Network)
	report.detail("broker port", "%d", overrides.BrokerPort)
}

// reportCage writes the backend name, readiness, isolation, and
// capabilities.
func (report *doctorReport) reportCage(ctx context.Context) {
	selected := report.environment.Cage
	capabilities := selected.Capabilities()
	report.heading("cage")
	report.detail("name", "%s", selected.Name())
	report.detail("backend", "%s", doctorYesNo(selected.Available(),
		"the backend command is installed",
		"the backend command is not installed"))
	if err := selected.Require(ctx); err != nil {
		report.detail("ready", "no: %v", err)
	} else {
		report.detail("ready", "yes, a box can run here")
	}
	report.detail("isolation", "%s", capabilities.Isolation)
	if capabilities.Isolation != cage.IsolationVM {
		report.note("this backend shares one kernel with the host, so a " +
			"box is confined by that kernel, not isolated from it")
	}
	report.detail("addresses", "%s",
		doctorYesNo(capabilities.GuestAddresses, "yes", "no"))
	report.detail("hostnames", "%s",
		doctorYesNo(capabilities.GuestHostnames, "yes", "no"))
	report.detail("dns domain", "%s",
		doctorYesNo(capabilities.DNSDomain, "yes", "no"))
	report.detail("route repair", "%s",
		doctorYesNo(capabilities.RouteRepair, "yes", "no"))
	report.detail("host-only", "%s",
		doctorYesNo(capabilities.HostOnlyNetwork, "yes", "no"))
	report.detail("firewall", "%s",
		doctorYesNo(capabilities.HostFirewall, "yes", "no"))
	var backendReport strings.Builder
	selected.Doctor(ctx, &backendReport)
	if text := strings.TrimRight(backendReport.String(), "\n"); text != "" {
		report.blank()
		doctorIndent(report.out, text, "  ")
	}
}

// reportNetwork writes the box network state and returns its info.
func (report *doctorReport) reportNetwork(
	ctx context.Context,
) cage.NetworkInfo {
	name := report.environment.Overrides.Network
	report.heading("network")
	report.detail("name", "%s", name)
	info, err := report.environment.Cage.Network(ctx, name)
	if err != nil {
		report.note("Prison could not ask the backend about it: %v", err)
		return cage.NetworkInfo{Name: name}
	}
	if !info.Exists {
		report.note("the %s network does not exist yet; `prison up` makes it",
			name)
		return info
	}
	report.detail("exists", "yes")
	if info.HostOnly {
		report.detail("host-only", "yes, so a box cannot reach the internet "+
			"whatever it holds inside")
	} else {
		report.detail("host-only", "no, so egress rests on the in-box rules "+
			"alone")
	}
	if info.Gateway == "" {
		report.detail("gateway", "none yet; the backend brings it up with "+
			"the first box")
	} else {
		report.detail("gateway", "%s", info.Gateway)
	}
	if info.SubnetV4 == "" {
		report.detail("subnet", "none the backend will name")
	} else {
		report.detail("subnet", "%s", info.SubnetV4)
	}
	if info.SubnetV6 != "" {
		report.detail("subnet (v6)", "%s", info.SubnetV6)
	}
	return info
}

// reportBroker writes the broker status and its details if running.
func (report *doctorReport) reportBroker(ctx context.Context) {
	socketPath := report.environment.Root.BrokerSocket()
	report.heading("broker")
	report.detail("socket", "%s", socketPath)
	client := control.NewClient(socketPath)
	if !client.Alive(ctx) {
		report.note("the broker is not running; `prison up` starts it")
		return
	}
	status, err := client.Status(ctx)
	if err != nil {
		report.note("the broker answered once and then stopped: %v", err)
		return
	}
	report.detail("version", "%s", status.Version)
	report.detail("pid", "%d", status.PID)
	report.detail("vault", "%s", status.Vault)
	for _, listener := range status.Listeners {
		report.detail("listener", "%s", doctorListenerText(listener))
	}
	report.detail("projects", "%d", len(status.Projects))
}

// reportFirewall writes the host firewall state and the command to
// read the loaded rules.
func (report *doctorReport) reportFirewall(
	ctx context.Context, network cage.NetworkInfo,
) {
	report.heading("firewall")
	if !report.environment.Cage.Capabilities().HostFirewall {
		report.note("the %s cage has no subnet Prison can filter",
			report.environment.Cage.Name())
		return
	}
	if network.SubnetV4 == "" {
		report.note("the box network names no subnet yet, so there is " +
			"nothing to write rules against; `prison up` makes it, and " +
			"`prison host firewall` installs them")
		report.detail("read rules", "%s", hostfw.ReadCommand())
		return
	}
	spec := hostfw.Spec{
		SubnetV4:   network.SubnetV4,
		SubnetV6:   network.SubnetV6,
		BrokerPort: report.environment.Overrides.BrokerPort,
	}
	installed, err := hostfw.Status(
		ctx, spec, report.environment.Root.Path, hostfw.SystemRunner)
	if err != nil {
		report.note("Prison could not read the firewall: %v", err)
		report.detail("read rules", "%s", hostfw.ReadCommand())
		return
	}
	report.detail("state", "%s", installed)
	switch installed {
	case hostfw.StateInstalled:
		report.note("Prison's rules are loaded, so a box reaches the host "+
			"only on port %d", spec.BrokerPort)
	case hostfw.StateStale:
		report.note("the rules on this host no longer match the box " +
			"network; `prison host firewall` writes them again")
	case hostfw.StateUnsupported:
		report.note("Prison writes no packet filter rules for this host, " +
			"so `prison host firewall` has nothing to install and nothing " +
			"holds the box network shut from the host side")
	default:
		report.note("nothing holds the box network shut from the host " +
			"side, so a host service listening on every interface is " +
			"reachable from a box; `prison host firewall` installs the rules")
	}
	report.detail("read rules", "%s", hostfw.ReadCommand())
}

// reportVault writes whether the vault exists and how many projects
// hold grants.
func (report *doctorReport) reportVault() {
	root := report.environment.Root
	report.heading("vault")
	report.detail("file", "%s", root.VaultFile())
	if !vault.Exists(root.VaultFile()) {
		report.note("no vault yet; `prison secret init` creates one")
		return
	}
	report.detail("state", "present")
	projects, err := root.ListProjects()
	if err != nil {
		report.note("Prison could not read the projects: %v", err)
		return
	}
	granted := 0
	for _, project := range projects {
		grants, err := project.Grants()
		if err != nil {
			report.note("Prison could not read the grants of %s: %v",
				project.ID, err)
			continue
		}
		if len(grants) > 0 {
			granted++
		}
	}
	report.detail("grants", "%d of %d projects hold one",
		granted, len(projects))
}

// reportProjects writes every known project and its path.
func (report *doctorReport) reportProjects() {
	report.heading("projects")
	projects, err := report.environment.Root.ListProjects()
	if err != nil {
		report.note("Prison could not read the projects: %v", err)
		return
	}
	report.detail("count", "%d", len(projects))
	if len(projects) == 0 {
		report.note("the state root holds no projects yet; `prison up` in " +
			"a project directory makes one")
		return
	}
	for _, project := range projects {
		report.detail(project.ID, "%s", doctorProjectPathText(project))
	}
}

// reportSettings writes the pager, diff tool, and PRISON_* variable
// names.
func (report *doctorReport) reportSettings() {
	environment := report.environment
	report.heading("settings")
	pager := config.ResolvePager(
		environment.Overrides, environment.Global, os.LookupEnv)
	if pager == "" {
		report.detail("pager", "none, so Prison prints without one")
	} else {
		report.detail("pager", "%s", pager)
	}
	diffTool := config.ResolveDiffTool(
		environment.Overrides, environment.Global)
	if diffTool == "" {
		report.detail("diff tool", "none, so Prison renders the diff itself")
	} else {
		report.detail("diff tool", "%s", diffTool)
	}
	names := doctorEnvironmentNames(os.Environ())
	if len(names) == 0 {
		report.detail("environment", "no PRISON_* variable is set here")
		return
	}
	report.detail("environment", "%s", strings.Join(names, ", "))
}

// reportLegacyLayout warns if the state root holds directories from
// the previous implementation.
func (report *doctorReport) reportLegacyLayout() {
	root := report.environment.Root
	if !root.LegacyLayoutDetected() {
		return
	}
	shortPath := doctorShortenHome(root.Path, report.environment.HomeDir)
	report.heading("legacy layout")
	report.note("the state root still holds the previous " +
		"implementation's directories")
	report.note("this one keeps every project under projects/, and reads " +
		"nothing out of the old ones")
	report.note("move the old root aside and start fresh:")
	report.command(fmt.Sprintf("mv %s %s-old", shortPath, shortPath))
	report.command("prison up")
	report.note("the secrets have to be added again, because the vault " +
		"format changed")
}

// doctorListenerText returns a description of one broker listener.
func doctorListenerText(listener control.Listener) string {
	address := listener.Address
	if listener.Port != 0 {
		address = fmt.Sprintf("%s:%d", address, listener.Port)
	}
	if listener.Bound {
		return address + ", bound"
	}
	if listener.Error != "" {
		return fmt.Sprintf("%s, not bound: %s", address, listener.Error)
	}
	return address + ", not bound"
}

// doctorProjectPathText returns where a project lives. Marks a
// path that no longer exists.
func doctorProjectPathText(project *state.Project) string {
	record, err := project.Record()
	if err != nil {
		return fmt.Sprintf("its record cannot be read: %v", err)
	}
	if record == nil || record.Path == "" {
		return "no project.json, so Prison does not know its path"
	}
	if _, err := os.Stat(record.Path); err != nil {
		return record.Path + ", which no longer exists"
	}
	return record.Path
}

// doctorEnvironmentNames returns the sorted names of PRISON_*
// variables from the given environment listing.
func doctorEnvironmentNames(environment []string) []string {
	var names []string
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if found && strings.HasPrefix(name, "PRISON_") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// doctorShortenHome replaces the home directory prefix with a tilde.
func doctorShortenHome(path, homeDirectory string) string {
	if homeDirectory == "" {
		return path
	}
	if path == homeDirectory {
		return "~"
	}
	if strings.HasPrefix(path, homeDirectory+"/") {
		return "~" + strings.TrimPrefix(path, homeDirectory)
	}
	return path
}

// doctorYesNo returns whenTrue if condition is true, whenFalse
// otherwise.
func doctorYesNo(condition bool, whenTrue, whenFalse string) string {
	if condition {
		return whenTrue
	}
	return whenFalse
}

// doctorIndent writes text with the given prefix on each non-empty
// line.
func doctorIndent(w io.Writer, text, prefix string) {
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			fmt.Fprintln(w)
			continue
		}
		fmt.Fprintln(w, prefix+line)
	}
}
