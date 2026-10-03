package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"prison/internal/isolator"
	"prison/internal/checkpoint"
	"prison/internal/config"
	"prison/internal/session"
	"prison/internal/state"
	"prison/internal/ui"
)

const snapshotsDirectoryName = "snapshots"

const shadowDirectoryName = "shadow"

func init() {
	registerGroup(addLifecycleCommands)
}

func addLifecycleCommands(root *cobra.Command) {
	root.AddCommand(
		newDownCommand(),
		newRemoveCommand(),
		newListCommand(),
		newStatusCommand(),
		newPortsCommand(),
	)
}

func newDownCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "down [box]",
		Short: "stop a box",
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runDown(optionalBoxArgument(arguments))
		},
	}
}

func newRemoveCommand() *cobra.Command {
	var removesState bool
	var assumeYes bool
	command := &cobra.Command{
		Use:   "rm [box]",
		Short: "remove a box and keep its state",
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runRemove(
				optionalBoxArgument(arguments), removesState, assumeYes)
		},
	}
	command.Flags().BoolVar(&removesState, "state", false,
		"also remove the state directory")
	command.Flags().BoolVar(&assumeYes, "yes", false,
		"remove the state directory without the prompt")
	return command
}

func newListCommand() *cobra.Command {
	var stateListing bool
	command := &cobra.Command{
		Use:   "list",
		Short: "list boxes and their projects",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runList(command, stateListing)
		},
	}
	command.Flags().BoolVar(&stateListing, "state", false,
		"list the state directories")
	return command
}

func newStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "show the state of this project",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runStatus(command)
		},
	}
}

func newPortsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ports",
		Short: "show the published ports of this box",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runPorts()
		},
	}
}

func optionalBoxArgument(arguments []string) string {
	if len(arguments) == 0 {
		return ""
	}
	return arguments[0]
}

func runDown(boxArgument string) error {
	ctx, cancel := commandContext()
	defer cancel()
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	if err := environment.Isolator.Require(ctx); err != nil {
		return err
	}
	project, err := environment.ResolveBoxArgument(boxArgument)
	if err != nil {
		return err
	}
	boxName := project.BoxName(environment.Overrides.Domain)
	box, err := environment.Isolator.Box(ctx, boxName)
	if err != nil {
		return err
	}
	if !box.Exists {
		if boxArgument == "" {
			return fmt.Errorf("there is no box for this project. Run `prison up`")
		}
		return fmt.Errorf("no box for %s. Run `prison list`", boxArgument)
	}
	ui.Progress("stopping %s", boxName)
	environment.UnpublishPorts(ctx, boxName)
	if err := environment.Isolator.Stop(ctx, boxName); err != nil {
		return err
	}
	if boxArgument == "" {
		if current, err := environment.OpenCurrent(); err == nil {
			reportCheckpointDrift(current)
		}
	}
	return nil
}

func runRemove(boxArgument string, removesState, assumeYes bool) error {
	ctx, cancel := commandContext()
	defer cancel()
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	if err := environment.Isolator.Require(ctx); err != nil {
		return err
	}
	project, err := environment.ResolveBoxArgument(boxArgument)
	if err != nil {
		return err
	}
	boxName := project.BoxName(environment.Overrides.Domain)
	box, err := environment.Isolator.Box(ctx, boxName)
	if err != nil {
		return err
	}
	if !removesState {
		return removeBoxOnly(ctx, environment, project, boxName,
			box.Exists, boxArgument)
	}
	return removeBoxAndState(ctx, environment, project, boxName,
		box.Exists, boxArgument, assumeYes)
}

func removeBoxOnly(ctx context.Context, environment *session.Environment,
	project *state.Project, boxName string, boxExists bool,
	boxArgument string) error {
	if !boxExists {
		if boxArgument == "" {
			return fmt.Errorf("there is no box for this project. " +
				"Run `prison rm --state` to remove the state")
		}
		return fmt.Errorf("no box for %s. Run `prison list`", boxArgument)
	}
	if err := destroyBox(ctx, environment, boxName); err != nil {
		return err
	}
	if err := forgetBoxShape(project); err != nil {
		return err
	}
	ui.Progress("state kept at %s", project.Dir)
	return nil
}

func removeBoxAndState(ctx context.Context,
	environment *session.Environment, project *state.Project, boxName string,
	boxExists bool, boxArgument string, assumeYes bool) error {
	stateExists := directoryIsPresent(project.Dir)
	if !boxExists && !stateExists {
		if boxArgument == "" {
			return fmt.Errorf("there is no box and no state for this project")
		}
		return fmt.Errorf("no box and no state for %s. "+
			"Run `prison list --state`", boxArgument)
	}
	if stateExists {
		announceStateRemoval(project)
		if !assumeYes && !ui.Confirm("remove it?") {
			return ui.Exit(1, "canceled")
		}
	}
	if boxExists {
		if err := destroyBox(ctx, environment, boxName); err != nil {
			return err
		}
	}
	if stateExists {
		if err := project.Remove(); err != nil {
			return err
		}
		ui.Progress("removed %s", project.Dir)
	}
	return nil
}

func destroyBox(ctx context.Context, environment *session.Environment,
	boxName string) error {
	environment.UnpublishPorts(ctx, boxName)
	_ = environment.Isolator.Stop(ctx, boxName)
	if err := environment.Isolator.Delete(ctx, boxName); err != nil {
		box, lookupError := environment.Isolator.Box(ctx, boxName)
		if lookupError != nil || box.Exists {
			return err
		}
	}
	ui.Progress("removed %s", boxName)
	return nil
}

// forgetBoxShape clears the recorded box so that the next `prison up` uses
// the current configuration.
func forgetBoxShape(project *state.Project) error {
	record, err := project.Record()
	if err != nil || record == nil {
		return err
	}
	record.Box = nil
	record.SetupSignature = ""
	return project.SaveRecord(record)
}

func announceStateRemoval(project *state.Project) {
	size := "an unknown amount"
	if bytes, err := project.Size(); err == nil {
		size = renderDirectorySize(bytes)
	}
	ui.Progress("removing %s (%s):", project.Dir, size)
	for _, name := range childDirectoryNames(project.HomesDir()) {
		fmt.Printf("  the %s home directory\n", name)
	}
	shadowRoot := filepath.Join(project.Dir, shadowDirectoryName)
	for _, name := range childDirectoryNames(shadowRoot) {
		fmt.Printf("  %s (rebuilt on the next `prison up`)\n", name)
	}
	snapshots := filepath.Join(project.CheckpointsDir(), snapshotsDirectoryName)
	if directoryIsPresent(snapshots) {
		fmt.Println("  all checkpoints")
	}
	if fileHasContent(project.EgressAllowFile()) {
		fmt.Println("  the egress allowlist")
	}
	if granted, err := project.Grants(); err == nil && len(granted) > 0 {
		fmt.Println("  the secret grants")
	}
}

func runList(command *cobra.Command, stateListing bool) error {
	ctx, cancel := commandContext()
	defer cancel()
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	if stateListing {
		return runListState(ctx, command, environment)
	}
	if err := environment.Isolator.Require(ctx); err != nil {
		return err
	}
	boxes, err := environment.Isolator.ListBoxes(ctx)
	if err != nil {
		return err
	}
	suffix := "." + environment.Overrides.Domain
	rows := [][]string{{"BOX", "STATE", "PROJECT"}}
	for _, box := range boxes {
		if !strings.HasSuffix(box.Name, suffix) {
			continue
		}
		rows = append(rows, []string{
			box.Name,
			describeBoxPresence(box),
			describeBoxProject(environment, box.Name),
		})
	}
	if len(rows) == 1 {
		ui.Progress("no boxes")
		return nil
	}
	return ui.Table(command.OutOrStdout(), rows)
}

func runListState(ctx context.Context, command *cobra.Command,
	environment *session.Environment) error {
	projects, err := environment.Root.ListProjects()
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		ui.Progress("no state in %s", environment.Root.Path)
		return nil
	}
	boxes, isolatorIsReadable := readBoxesByName(ctx, environment)
	rows := [][]string{{"STATE", "SIZE", "BOX", "PROJECT"}}
	for _, project := range projects {
		size := "?"
		if bytes, err := project.Size(); err == nil {
			size = renderDirectorySize(bytes)
		}
		boxColumn := "?"
		if isolatorIsReadable {
			boxColumn = "none"
			name := project.BoxName(environment.Overrides.Domain)
			if box, held := boxes[name]; held {
				boxColumn = describeBoxPresence(box)
			}
		}
		rows = append(rows, []string{
			project.ID, size, boxColumn, describeProjectOf(project),
		})
	}
	if err := ui.Table(command.OutOrStdout(), rows); err != nil {
		return err
	}
	if !isolatorIsReadable {
		ui.Warn("cannot list the boxes")
	}
	return nil
}

func readBoxesByName(ctx context.Context,
	environment *session.Environment) (map[string]isolator.BoxInfo, bool) {
	if environment.Isolator.Require(ctx) != nil {
		return nil, false
	}
	listed, err := environment.Isolator.ListBoxes(ctx)
	if err != nil {
		return nil, false
	}
	boxes := make(map[string]isolator.BoxInfo, len(listed))
	for _, box := range listed {
		boxes[box.Name] = box
	}
	return boxes, true
}

func runStatus(command *cobra.Command) error {
	ctx, cancel := commandContext()
	defer cancel()
	current, err := openSession()
	if err != nil {
		return err
	}
	current.WarnAboutUntrustedConfiguration()
	rows := [][]string{
		{"project", current.Directory},
		{"box", current.BoxName},
		{"state", current.Project.Dir},
		{"network", current.Overrides.Network},
		{"sudo", describeSudo(current)},
		{"inmates", namesOrNone(current.InmateNames)},
		{"secrets", describeGrantedSecrets(current)},
	}
	if err := current.Isolator.Require(ctx); err != nil {
		rows = append(rows, []string{"status", err.Error()})
		return ui.Table(command.OutOrStdout(), rows)
	}
	box, err := current.Isolator.Box(ctx, current.BoxName)
	if err != nil {
		return err
	}
	rows = append(rows, []string{"status", describeBoxPresence(box)})
	return ui.Table(command.OutOrStdout(), rows)
}

// runPorts reads the current configuration because the box record can hold
// older ports.
func runPorts() error {
	ctx, cancel := commandContext()
	defer cancel()
	current, err := openSession()
	if err != nil {
		return err
	}
	if err := current.RequireRunning(ctx); err != nil {
		return err
	}
	ports, err := current.ResolvePorts()
	if err != nil {
		return err
	}
	if len(ports) == 0 {
		ui.Progress("this box publishes no ports. " +
			"Set `[box] ports` in prison.toml")
		return nil
	}
	for _, mapping := range ports {
		printPortLine(mapping)
	}
	return nil
}

func describeSudo(current *session.Session) string {
	if current.Record != nil && current.Record.Box != nil {
		if current.Record.Box.Sudo {
			return "yes, as the box was created"
		}
		return "no, as the box was created"
	}
	if config.ResolveSudo(current.Overrides, current.Config) {
		return "yes"
	}
	return "no"
}

func describeGrantedSecrets(current *session.Session) string {
	granted, err := current.Project.Grants()
	if err != nil {
		return "unreadable: " + err.Error()
	}
	return namesOrNone(granted)
}

func describeBoxProject(environment *session.Environment,
	boxName string) string {
	identifier := projectIDFromBoxName(boxName, environment.Overrides.Domain)
	if identifier == "" {
		return "(no state)"
	}
	project, err := environment.Root.ProjectByID(identifier)
	if err != nil {
		return "(no state)"
	}
	return describeProjectOf(project)
}

func describeProjectOf(project *state.Project) string {
	record, err := project.Record()
	if err != nil || record == nil || record.Path == "" {
		return "(no record)"
	}
	if directoryIsPresent(record.Path) {
		return record.Path
	}
	return record.Path + " (gone)"
}

func projectIDFromBoxName(boxName, domain string) string {
	candidate := strings.TrimSuffix(boxName, "."+domain)
	if index := strings.LastIndex(candidate, "-"); index >= 0 {
		candidate = candidate[index+1:]
	}
	if !state.IsProjectID(candidate) {
		return ""
	}
	return candidate
}

func describeBoxPresence(box isolator.BoxInfo) string {
	switch {
	case box.Running:
		return "running"
	case box.Exists:
		return "stopped"
	default:
		return "absent"
	}
}

func reportCheckpointDrift(current *session.Session) {
	snapshots := filepath.Join(
		current.Project.CheckpointsDir(), snapshotsDirectoryName)
	if !directoryIsPresent(snapshots) {
		return
	}
	patterns := append([]string(nil), checkpoint.DefaultIgnores...)
	if current.Config != nil {
		patterns = append(patterns, current.Config.Checkpoint.Ignore...)
	}
	store, err := checkpoint.Open(current.Project.CheckpointsDir(),
		current.Directory, checkpoint.NewIgnore(patterns))
	if err != nil {
		return
	}
	identifier, err := store.DiffTarget("")
	if err != nil {
		return
	}
	changes, _, _, _, err := store.Diff(identifier)
	if err != nil || changes.Empty() {
		return
	}
	fmt.Printf("the tree changed after checkpoint %s. "+
		"Run `prison checkpoint diff`\n", identifier)
}

func renderDirectorySize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%dB", bytes)
	}
	units := []string{"K", "M", "G", "T", "P"}
	value := float64(bytes)
	index := -1
	for value >= unit && index < len(units)-1 {
		value /= unit
		index++
	}
	return fmt.Sprintf("%.1f%s", value, units[index])
}

func childDirectoryNames(directory string) []string {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names
}

func directoryIsPresent(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fileHasContent(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

func namesOrNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, " ")
}
