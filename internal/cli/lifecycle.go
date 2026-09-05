package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"prison/internal/cage"
	"prison/internal/checkpoint"
	"prison/internal/config"
	"prison/internal/session"
	"prison/internal/state"
	"prison/internal/ui"
)

// snapshotsDirectoryName is the subdirectory that holds checkpoint
// snapshots. Present only when the project has a checkpoint store.
const snapshotsDirectoryName = "snapshots"

// shadowDirectoryName is the subdirectory holding box-private copies of
// shadowed paths.
const shadowDirectoryName = "shadow"

// init registers the lifecycle commands.
func init() {
	registerGroup(addLifecycleCommands)
}

// addLifecycleCommands adds the down, rm, list, status, and ports
// commands to root.
func addLifecycleCommands(root *cobra.Command) {
	root.AddCommand(
		newDownCommand(),
		newRemoveCommand(),
		newListCommand(),
		newStatusCommand(),
		newPortsCommand(),
	)
}

// newDownCommand builds the down command. Stops a box without
// destroying it. Returns a *cobra.Command.
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

// newRemoveCommand builds the rm command. Destroys a box and
// optionally its state directory. Returns a *cobra.Command.
func newRemoveCommand() *cobra.Command {
	var removesState bool
	var assumeYes bool
	command := &cobra.Command{
		Use:   "rm [box]",
		Short: "destroy a box, keeping the state beside it",
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runRemove(
				optionalBoxArgument(arguments), removesState, assumeYes)
		},
	}
	command.Flags().BoolVar(&removesState, "state", false,
		"remove the state directory as well, which cannot be undone")
	command.Flags().BoolVar(&assumeYes, "yes", false,
		"remove the state directory without the confirmation prompt")
	return command
}

// newListCommand builds the list command. Lists boxes or state
// directories. Returns a *cobra.Command.
func newListCommand() *cobra.Command {
	var stateListing bool
	command := &cobra.Command{
		Use:   "list",
		Short: "every box Prison holds, and the project each belongs to",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runList(command, stateListing)
		},
	}
	command.Flags().BoolVar(&stateListing, "state", false,
		"list the state directories")
	return command
}

// newStatusCommand builds the status command. Reports what prison
// holds for the current project. Returns a *cobra.Command.
func newStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "what Prison holds for this project",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runStatus(command)
		},
	}
}

// newPortsCommand builds the ports command. Prints the host addresses
// that reach the running box. Returns a *cobra.Command.
func newPortsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ports",
		Short: "how to reach this project's running box",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runPorts()
		},
	}
}

// optionalBoxArgument returns the first argument, or empty string if
// none was given. Empty means the current project's box.
func optionalBoxArgument(arguments []string) string {
	if len(arguments) == 0 {
		return ""
	}
	return arguments[0]
}

// runDown stops the named box, or the current project's box if
// boxArgument is empty. Prints a checkpoint drift reminder when
// stopping the current project.
func runDown(boxArgument string) error {
	ctx, cancel := commandContext()
	defer cancel()
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	if err := environment.Cage.Require(ctx); err != nil {
		return err
	}
	project, err := environment.ResolveBoxArgument(boxArgument)
	if err != nil {
		return err
	}
	boxName := project.BoxName(environment.Overrides.Domain)
	box, err := environment.Cage.Box(ctx, boxName)
	if err != nil {
		return err
	}
	if !box.Exists {
		if boxArgument == "" {
			return fmt.Errorf(
				"there is no box for this project; `prison up` creates one")
		}
		return fmt.Errorf("no box for %s; `prison list` shows what exists",
			boxArgument)
	}
	ui.Progress("stopping %s", boxName)
	if err := environment.Cage.Stop(ctx, boxName); err != nil {
		return err
	}
	if boxArgument == "" {
		if current, err := environment.OpenCurrent(); err == nil {
			reportCheckpointDrift(current)
		}
	}
	return nil
}

// runRemove destroys a box. Takes the box name (empty for current),
// whether to remove state too, and whether to skip the prompt.
// Declining exits 1.
func runRemove(boxArgument string, removesState, assumeYes bool) error {
	ctx, cancel := commandContext()
	defer cancel()
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	if err := environment.Cage.Require(ctx); err != nil {
		return err
	}
	project, err := environment.ResolveBoxArgument(boxArgument)
	if err != nil {
		return err
	}
	boxName := project.BoxName(environment.Overrides.Domain)
	box, err := environment.Cage.Box(ctx, boxName)
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

// removeBoxOnly destroys the box and keeps the state directory. Fails
// if no box exists.
func removeBoxOnly(ctx context.Context, environment *session.Environment,
	project *state.Project, boxName string, boxExists bool,
	boxArgument string) error {
	if !boxExists {
		if boxArgument == "" {
			return fmt.Errorf("there is no box for this project; " +
				"`prison rm --state` removes the state one left behind")
		}
		return fmt.Errorf("no box for %s; `prison list` shows what exists, "+
			"and `prison rm --state` removes a state whose box is already "+
			"gone", boxArgument)
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

// removeBoxAndState destroys the box and its state directory. Shows
// what the directory holds and asks first unless assumeYes is set.
// Fails if neither exists.
func removeBoxAndState(ctx context.Context,
	environment *session.Environment, project *state.Project, boxName string,
	boxExists bool, boxArgument string, assumeYes bool) error {
	stateExists := directoryIsPresent(project.Dir)
	if !boxExists && !stateExists {
		if boxArgument == "" {
			return fmt.Errorf("there is no box and no state for this project")
		}
		return fmt.Errorf("no box and no state for %s; "+
			"`prison list --state` shows what is left", boxArgument)
	}
	if stateExists {
		announceStateRemoval(project)
		if !assumeYes && !ui.Confirm("remove it?") {
			return ui.Exit(1, "left alone")
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

// destroyBox stops and deletes a box. A missing box on the backend
// counts as done.
func destroyBox(ctx context.Context, environment *session.Environment,
	boxName string) error {
	_ = environment.Cage.Stop(ctx, boxName)
	if err := environment.Cage.Delete(ctx, boxName); err != nil {
		box, lookupError := environment.Cage.Box(ctx, boxName)
		if lookupError != nil || box.Exists {
			return err
		}
	}
	ui.Progress("removed %s", boxName)
	return nil
}

// forgetBoxShape clears the box shape and setup signature from the
// project record. The next `prison up` will recreate the box from the
// current config. Does nothing if no record exists.
func forgetBoxShape(project *state.Project) error {
	record, err := project.Record()
	if err != nil || record == nil {
		return err
	}
	record.Box = nil
	record.SetupSignature = ""
	return project.SaveRecord(record)
}

// announceStateRemoval prints the state directory, its size, and what
// it contains. Shown before asking to confirm removal.
func announceStateRemoval(project *state.Project) {
	size := "an unknown amount"
	if bytes, err := project.Size(); err == nil {
		size = renderDirectorySize(bytes)
	}
	ui.Progress("removing %s, which holds %s:", project.Dir, size)
	for _, name := range childDirectoryNames(project.HomesDir()) {
		fmt.Printf("  the %s home: its history and its settings\n", name)
	}
	shadowRoot := filepath.Join(project.Dir, shadowDirectoryName)
	for _, name := range childDirectoryNames(shadowRoot) {
		fmt.Printf("  %s, the box's own copy, which the next `prison up` "+
			"rebuilds\n", name)
	}
	snapshots := filepath.Join(project.CheckpointsDir(), snapshotsDirectoryName)
	if directoryIsPresent(snapshots) {
		fmt.Println("  every checkpoint of the tree, and any state they " +
			"could restore it to")
	}
	if fileHasContent(project.EgressAllowFile()) {
		fmt.Println("  the egress allowlist you edited for this project")
	}
	if granted, err := project.Grants(); err == nil && len(granted) > 0 {
		fmt.Println("  which secrets this project was granted, though not " +
			"the secrets themselves, which stay in the vault")
	}
	ui.Warn("this is the part that survives `prison rm`")
}

// runList prints a table of boxes, or the state listing if stateListing
// is set.
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
	if err := environment.Cage.Require(ctx); err != nil {
		return err
	}
	boxes, err := environment.Cage.ListBoxes(ctx)
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
		ui.Progress("no boxes on the %s cage; `prison up` in a project "+
			"creates one", environment.Cage.Name())
		ui.Progress("`prison list --state` shows what they left behind")
		return nil
	}
	return ui.Table(command.OutOrStdout(), rows)
}

// runListState prints a table of state directories with their sizes
// and box status. Shows `?` for the box column when the backend cannot
// be read.
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
	boxes, backendIsReadable := readBoxesByName(ctx, environment)
	rows := [][]string{{"STATE", "SIZE", "BOX", "PROJECT"}}
	for _, project := range projects {
		size := "?"
		if bytes, err := project.Size(); err == nil {
			size = renderDirectorySize(bytes)
		}
		boxColumn := "?"
		if backendIsReadable {
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
	if !backendIsReadable {
		ui.Warn("the %s cage could not be read, so whether a box still "+
			"exists for any of these is unknown", environment.Cage.Name())
	}
	return nil
}

// readBoxesByName lists all boxes keyed by name. Returns false as the
// second result if the backend is unavailable.
func readBoxesByName(ctx context.Context,
	environment *session.Environment) (map[string]cage.BoxInfo, bool) {
	if !environment.Cage.Available() {
		return nil, false
	}
	listed, err := environment.Cage.ListBoxes(ctx)
	if err != nil {
		return nil, false
	}
	boxes := make(map[string]cage.BoxInfo, len(listed))
	for _, box := range listed {
		boxes[box.Name] = box
	}
	return boxes, true
}

// runStatus prints the current project's box, state, cage, and
// running status.
func runStatus(command *cobra.Command) error {
	ctx, cancel := commandContext()
	defer cancel()
	current, err := openSession()
	if err != nil {
		return err
	}
	current.WarnAboutUntrustedConfiguration()
	capabilities := current.Cage.Capabilities()
	rows := [][]string{
		{"project", current.Directory},
		{"box", current.BoxName},
		{"state", current.Project.Dir},
		{"cage", fmt.Sprintf("%s, %s isolation",
			current.Cage.Name(), capabilities.Isolation)},
		{"network", current.Overrides.Network},
		{"sudo", describeSudo(current)},
		{"inmates", namesOrNone(current.InmateNames)},
		{"secrets", describeGrantedSecrets(current)},
	}
	if !current.Cage.Available() {
		rows = append(rows, []string{"backend", "not installed"})
		return ui.Table(command.OutOrStdout(), rows)
	}
	box, err := current.Cage.Box(ctx, current.BoxName)
	if err != nil {
		return err
	}
	rows = append(rows, []string{"status", describeBoxPresence(box)})
	return ui.Table(command.OutOrStdout(), rows)
}

// runPorts prints the host addresses that reach into the running box.
// Resolves ports from the current config rather than reading them back.
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
		ui.Progress("this box publishes nothing on the host; `[box] ports` " +
			"in prison.toml names what to publish creation")
		return nil
	}
	for _, mapping := range ports {
		printPortLine(mapping)
	}
	return nil
}

// describeSudo returns a string saying whether sudo is available in
// the box. Takes a session. Checks the box record first, then the
// config.
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

// describeGrantedSecrets returns the names of secrets granted to the
// project, or "none". Does not read values.
func describeGrantedSecrets(current *session.Session) string {
	granted, err := current.Project.Grants()
	if err != nil {
		return "unreadable: " + err.Error()
	}
	return namesOrNone(granted)
}

// describeBoxProject returns the project path for a box name. Returns
// "(no state)" if prison has no record for it.
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

// describeProjectOf returns the project's recorded path. Appends
// "(gone)" if the directory no longer exists. Returns "(no record)" if
// no path is recorded.
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

// projectIDFromBoxName extracts the project id from a box name shaped
// like `<slug>-<id>.<domain>`. Returns empty string if the name does
// not match.
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

// describeBoxPresence returns "running", "stopped", or "absent" for a
// box.
func describeBoxPresence(box cage.BoxInfo) string {
	switch {
	case box.Running:
		return "running"
	case box.Exists:
		return "stopped"
	default:
		return "absent"
	}
}

// reportCheckpointDrift prints a one-line warning if the tree changed
// since the last checkpoint. Stays silent on errors or if no store
// exists.
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
	fmt.Printf("the working tree has changed since checkpoint %s; "+
		"`prison checkpoint diff` shows what\n", identifier)
}

// renderDirectorySize formats a byte count as a human-readable size
// string like "5.4G". Takes a byte count. Returns the formatted string.
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

// childDirectoryNames returns the names of subdirectories under
// directory. Returns nil if the directory cannot be read.
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

// directoryIsPresent reports whether path is an existing directory.
func directoryIsPresent(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// fileHasContent reports whether path is a non-empty regular file.
func fileHasContent(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

// namesOrNone joins names with spaces. Returns "none" if the list is
// empty.
func namesOrNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, " ")
}
