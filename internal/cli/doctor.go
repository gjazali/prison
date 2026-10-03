package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"prison/internal/broker/control"
	"prison/internal/isolator"
	"prison/internal/config"
	"prison/internal/hostfw"
	"prison/internal/session"
	"prison/internal/state"
	"prison/internal/vault"
)

const doctorLabelWidth = 14

func init() {
	registerGroup(addDoctorCommand)
}

func addDoctorCommand(root *cobra.Command) {
	root.AddCommand(&cobra.Command{
		Use:   "doctor",
		Short: "show the state of prison on this host",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runDoctor(command)
		},
	})
}

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
	report.reportIsolator(ctx)
	network := report.reportNetwork(ctx)
	report.reportBroker(ctx)
	report.reportFirewall(ctx, network)
	report.reportVault()
	report.reportProjects()
	report.reportSettings()
	report.reportLegacyLayout()
	return nil
}

type doctorReport struct {
	out            io.Writer
	environment    *session.Environment
	sectionWritten bool
}

func (report *doctorReport) heading(name string) {
	if report.sectionWritten {
		fmt.Fprintln(report.out)
	}
	report.sectionWritten = true
	fmt.Fprintln(report.out, name)
}

func (report *doctorReport) detail(
	label, format string, arguments ...any,
) {
	fmt.Fprintf(report.out, "  %-*s%s\n", doctorLabelWidth, label,
		fmt.Sprintf(format, arguments...))
}

func (report *doctorReport) note(format string, arguments ...any) {
	fmt.Fprintf(report.out, "  %s\n", fmt.Sprintf(format, arguments...))
}

func (report *doctorReport) command(text string) {
	fmt.Fprintf(report.out, "    %s\n", text)
}

func (report *doctorReport) blank() {
	fmt.Fprintln(report.out)
}

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

func (report *doctorReport) reportIsolator(ctx context.Context) {
	selected := report.environment.Isolator
	report.heading("isolator")
	report.detail("name", "%s", session.IsolatorName)
	if err := selected.Require(ctx); err != nil {
		report.detail("ready", "no: %v", err)
	} else {
		report.detail("ready", "yes")
	}
	var isolatorReport strings.Builder
	selected.Doctor(ctx, &isolatorReport)
	if text := strings.TrimRight(isolatorReport.String(), "\n"); text != "" {
		report.blank()
		doctorIndent(report.out, text, "  ")
	}
}

func (report *doctorReport) reportNetwork(
	ctx context.Context,
) isolator.NetworkInfo {
	name := report.environment.Overrides.Network
	report.heading("network")
	report.detail("name", "%s", name)
	info, err := report.environment.Isolator.Network(ctx, name)
	if err != nil {
		report.note("cannot get the network state: %v", err)
		return isolator.NetworkInfo{Name: name}
	}
	if !info.Exists {
		report.note("the %s network does not exist. Run `prison up`", name)
		return info
	}
	report.detail("exists", "yes")
	if info.HostOnly {
		report.detail("host-only", "yes")
	} else {
		report.detail("host-only", "no")
	}
	if info.Gateway == "" {
		report.detail("gateway", "none")
	} else {
		report.detail("gateway", "%s", info.Gateway)
	}
	if info.SubnetV4 == "" {
		report.detail("subnet", "none")
	} else {
		report.detail("subnet", "%s", info.SubnetV4)
	}
	if info.SubnetV6 != "" {
		report.detail("subnet (v6)", "%s", info.SubnetV6)
	}
	return info
}

func (report *doctorReport) reportBroker(ctx context.Context) {
	socketPath := report.environment.Root.BrokerSocket()
	report.heading("broker")
	report.detail("socket", "%s", socketPath)
	client := control.NewClient(socketPath)
	if !client.Alive(ctx) {
		report.note("the broker is not running. Run `prison up`")
		return
	}
	status, err := client.Status(ctx)
	if err != nil {
		report.note("the broker stopped: %v", err)
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

func (report *doctorReport) reportFirewall(
	ctx context.Context, network isolator.NetworkInfo,
) {
	report.heading("firewall")
	if network.SubnetV4 == "" {
		report.note("the box network has no subnet. Run `prison up`")
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
		report.note("cannot read the firewall: %v", err)
		report.detail("read rules", "%s", hostfw.ReadCommand())
		return
	}
	report.detail("state", "%s", installed)
	switch installed {
	case hostfw.StateInstalled:
		report.note("a box can reach the host only on %s",
			portListText(hostfw.OpenPorts(spec)))
	case hostfw.StateStale:
		report.note("the rules do not match the box network. " +
			"Run `prison host firewall`")
	case hostfw.StateUnsupported:
		report.note("this host does not support the firewall rules")
	default:
		report.note("a box can reach every host service. " +
			"Run `prison host firewall`")
	}
	report.detail("read rules", "%s", hostfw.ReadCommand())
}

func (report *doctorReport) reportVault() {
	root := report.environment.Root
	report.heading("vault")
	report.detail("file", "%s", root.VaultFile())
	if !vault.Exists(root.VaultFile()) {
		report.note("there is no vault. Run `prison secret init`")
		return
	}
	report.detail("state", "present")
	projects, err := root.ListProjects()
	if err != nil {
		report.note("cannot read the projects: %v", err)
		return
	}
	granted := 0
	for _, project := range projects {
		grants, err := project.Grants()
		if err != nil {
			report.note("cannot read the grants of %s: %v", project.ID, err)
			continue
		}
		if len(grants) > 0 {
			granted++
		}
	}
	report.detail("grants", "%d of %d projects",
		granted, len(projects))
}

func (report *doctorReport) reportProjects() {
	report.heading("projects")
	projects, err := report.environment.Root.ListProjects()
	if err != nil {
		report.note("cannot read the projects: %v", err)
		return
	}
	report.detail("count", "%d", len(projects))
	if len(projects) == 0 {
		report.note("there are no projects")
		return
	}
	for _, project := range projects {
		report.detail(project.ID, "%s", doctorProjectPathText(project))
	}
}

func (report *doctorReport) reportSettings() {
	environment := report.environment
	report.heading("settings")
	pager := config.ResolvePager(
		environment.Overrides, environment.Global, os.LookupEnv)
	if pager == "" {
		report.detail("pager", "none")
	} else {
		report.detail("pager", "%s", pager)
	}
	diffTool := config.ResolveDiffTool(
		environment.Overrides, environment.Global)
	if diffTool == "" {
		report.detail("diff tool", "none")
	} else {
		report.detail("diff tool", "%s", diffTool)
	}
	names := doctorEnvironmentNames(os.Environ())
	if len(names) == 0 {
		report.detail("environment", "no PRISON_* variable is set")
		return
	}
	report.detail("environment", "%s", strings.Join(names, ", "))
}

func (report *doctorReport) reportLegacyLayout() {
	root := report.environment.Root
	if !root.LegacyLayoutDetected() {
		return
	}
	shortPath := doctorShortenHome(root.Path, report.environment.HomeDir)
	report.heading("legacy layout")
	report.note("the state root uses an old layout. To start again, run:")
	report.command(fmt.Sprintf("mv %s %s-old", shortPath, shortPath))
	report.command("prison up")
	report.note("then add the secrets again")
}

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

func doctorProjectPathText(project *state.Project) string {
	record, err := project.Record()
	if err != nil {
		return fmt.Sprintf("cannot read the record: %v", err)
	}
	if record == nil || record.Path == "" {
		return "path unknown, no project.json"
	}
	if _, err := os.Stat(record.Path); err != nil {
		return record.Path + " (missing)"
	}
	return record.Path
}

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

func doctorIndent(w io.Writer, text, prefix string) {
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			fmt.Fprintln(w)
			continue
		}
		fmt.Fprintln(w, prefix+line)
	}
}

func portListText(ports []int) string {
	words := make([]string, 0, len(ports))
	for _, port := range ports {
		words = append(words, strconv.Itoa(port))
	}
	if len(words) == 1 {
		return "port " + words[0]
	}
	return "ports " + strings.Join(words[:len(words)-1], ", ") + " and " +
		words[len(words)-1]
}
