package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"prison/internal/checkpoint"
	"prison/internal/config"
	"prison/internal/session"
	"prison/internal/ui"
)

// init registers the checkpoint commands with the root.
func init() {
	registerGroup(addCheckpointCommands)
}

// checkpointSubcommands lists valid subcommand names for error
// messages.
const checkpointSubcommands = "take, list, diff, merge, pick, restore, " +
	"back, forward, remove, or prune"

// addCheckpointCommands attaches `prison checkpoint` and its
// subcommands to the root command.
func addCheckpointCommands(root *cobra.Command) {
	group := &cobra.Command{
		Use:   "checkpoint",
		Short: "record the project tree, and move between recordings",
		Args: func(command *cobra.Command, arguments []string) error {
			if len(arguments) == 0 {
				return nil
			}
			return fmt.Errorf("unknown checkpoint subcommand: %s; try %s",
				arguments[0], checkpointSubcommands)
		},
		RunE: func(command *cobra.Command, arguments []string) error {
			return runCheckpointTake("")
		},
	}
	group.AddCommand(
		newCheckpointTakeCommand(),
		newCheckpointListCommand(),
		newCheckpointDiffCommand(),
		newCheckpointRestoreCommand(),
		newCheckpointMergeCommand(),
		newCheckpointPickCommand(),
		newCheckpointBackCommand(),
		newCheckpointForwardCommand(),
		newCheckpointRemoveCommand(),
		newCheckpointPruneCommand(),
	)
	root.AddCommand(group)
}

// newCheckpointTakeCommand builds `prison checkpoint take`. Returns
// the command.
func newCheckpointTakeCommand() *cobra.Command {
	var label string
	command := &cobra.Command{
		Use:   "take [note...]",
		Short: "record the tree now; bare words become the label",
		Args:  cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			note := label
			if note == "" {
				note = strings.Join(arguments, " ")
			}
			return runCheckpointTake(note)
		},
	}
	command.Flags().StringVar(&label, "label", "",
		"The note the listing shows for this checkpoint")
	return command
}

// newCheckpointListCommand builds `prison checkpoint list`. Returns
// the command.
func newCheckpointListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "every recording, marking where you are",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runCheckpointList()
		},
	}
}

// newCheckpointDiffCommand builds `prison checkpoint diff`. Returns
// the command. Exits 1 when changes exist.
func newCheckpointDiffCommand() *cobra.Command {
	var full bool
	command := &cobra.Command{
		Use:   "diff [id]",
		Short: "what changed since a checkpoint, or since the last one",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			identifier := ""
			if len(arguments) > 0 {
				identifier = arguments[0]
			}
			return runCheckpointDiff(identifier, full)
		},
	}
	command.Flags().BoolVar(&full, "full", false,
		"Print every diff body whole, without the line caps")
	return command
}

// newCheckpointRestoreCommand builds `prison checkpoint restore`.
// Returns the command.
func newCheckpointRestoreCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <id>",
		Short: "put the tree back to that recording",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runCheckpointRestore(arguments[0])
		},
	}
}

// newCheckpointMergeCommand builds `prison checkpoint merge`. Returns
// the command. Exits 1 on conflicts.
func newCheckpointMergeCommand() *cobra.Command {
	var base string
	var dryRun bool
	command := &cobra.Command{
		Use:   "merge <id>",
		Short: "Merge the tree with a checkpoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runCheckpointMerge(arguments[0], base, dryRun)
		},
	}
	command.Flags().StringVar(&base, "base", "",
		"The checkpoint both sides came from")
	command.Flags().BoolVar(&dryRun, "dry-run", false,
		"Report what the merge would do without writing anything")
	return command
}

// newCheckpointPickCommand builds `prison checkpoint pick`. Returns
// the command. Exits 1 on conflicts.
func newCheckpointPickCommand() *cobra.Command {
	var from string
	var dryRun bool
	command := &cobra.Command{
		Use:   "pick <id> [path...]",
		Short: "take what one recording changed, into the tree",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runCheckpointPick(
				arguments[0], from, arguments[1:], dryRun)
		},
	}
	command.Flags().StringVar(&from, "from", "",
		"The checkpoint the change is measured against, "+
			"when it is not the one before")
	command.Flags().BoolVar(&dryRun, "dry-run", false,
		"Report what the pick would do without writing anything")
	return command
}

// newCheckpointBackCommand builds `prison checkpoint back`. Returns
// the command.
func newCheckpointBackCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "back",
		Short: "restore the recording before the current one",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runCheckpointStep(-1)
		},
	}
}

// newCheckpointForwardCommand builds `prison checkpoint forward`.
// Returns the command.
func newCheckpointForwardCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "forward",
		Short: "restore the recording after the current one",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runCheckpointStep(1)
		},
	}
}

// newCheckpointRemoveCommand builds `prison checkpoint remove`.
// Returns the command.
func newCheckpointRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <id>",
		Short: "forget a recording",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runCheckpointRemove(arguments[0])
		},
	}
}

// newCheckpointPruneCommand builds `prison checkpoint prune`. Returns
// the command.
func newCheckpointPruneCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "prune",
		Short: "reclaim what nothing refers to",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runCheckpointPrune()
		},
	}
}

// openCheckpointStore opens the checkpoint store for the session's
// project. Returns the store or an error.
func openCheckpointStore(current *session.Session) (*checkpoint.Store, error) {
	patterns := append([]string(nil), checkpoint.DefaultIgnores...)
	if current.Config != nil {
		patterns = append(patterns, current.Config.Checkpoint.Ignore...)
	}
	for _, shadowed := range current.ShadowPaths() {
		patterns = append(patterns, shadowed+"/")
	}
	return checkpoint.Open(current.Project.CheckpointsDir(), current.Directory,
		checkpoint.NewIgnore(patterns))
}

// openCheckpointSession opens the session and its checkpoint store.
// Returns the session, store, or an error.
func openCheckpointSession() (*session.Session, *checkpoint.Store, error) {
	current, err := openSession()
	if err != nil {
		return nil, nil, err
	}
	current.WarnAboutUntrustedConfiguration()
	store, err := openCheckpointStore(current)
	if err != nil {
		return nil, nil, err
	}
	return current, store, nil
}

// runCheckpointTake records the working tree. Takes a label for the
// listing, which may be empty.
func runCheckpointTake(label string) error {
	_, store, err := openCheckpointSession()
	if err != nil {
		return err
	}
	recorded, err := store.Take(label)
	if err != nil {
		return err
	}
	fmt.Printf("checkpoint %s recorded, %d entries\n",
		recorded.ID, recorded.Entries)
	return nil
}

// runCheckpointList prints every checkpoint. Marks the current one
// with an arrow.
func runCheckpointList() error {
	_, store, err := openCheckpointSession()
	if err != nil {
		return err
	}
	recorded, err := store.List()
	if err != nil {
		return err
	}
	if len(recorded) == 0 {
		fmt.Println("no checkpoints for this project yet")
		return nil
	}
	head, hasHead := store.Head()
	for _, entry := range recorded {
		marker := "  "
		if hasHead && entry.ID == head {
			marker = "->"
		}
		moment := "?"
		if !entry.Time.IsZero() {
			moment = entry.Time.Format(time.RFC3339)
		}
		row := fmt.Sprintf("%s %s  %-26s%7d entries  %s",
			marker, entry.ID, moment, entry.Entries, entry.Label)
		fmt.Println(strings.TrimRight(row, " "))
	}
	return nil
}

// runCheckpointDiff shows what changed since a checkpoint. Takes the
// checkpoint identifier (empty for current) and whether to show full
// output. Exits 1 when anything differs.
func runCheckpointDiff(wanted string, full bool) error {
	current, store, err := openCheckpointSession()
	if err != nil {
		return err
	}
	diffTool := config.ResolveDiffTool(current.Overrides, current.Global)
	pager := config.ResolvePager(current.Overrides, current.Global,
		os.LookupEnv)
	paging := diffTool == "" && pager != "" && ui.StdoutIsTerminal()
	if diffTool != "" {
		err = requireCheckpointCommand(diffTool, "the checkpoint diff tool")
	} else if paging {
		err = requireCheckpointCommand(pager, "the checkpoint pager")
	}
	if err != nil {
		return err
	}

	identifier, err := store.DiffTarget(wanted)
	if err != nil {
		return err
	}
	changes, _, _, skipped, err := store.Diff(identifier)
	if err != nil {
		return err
	}
	reportCheckpointSkips(skipped)
	if changes.Empty() {
		// Under a diff tool the stream is what the tool reads, so the
		// verdict goes beside it rather than into it.
		if diffTool != "" {
			fmt.Fprintf(os.Stderr, "no changes since checkpoint %s\n",
				identifier)
			return nil
		}
		fmt.Printf("no changes since checkpoint %s\n", identifier)
		return nil
	}

	if diffTool != "" {
		if err := renderCheckpointDiffThroughTool(
			diffTool, store, identifier, changes); err != nil {
			return err
		}
		return ui.Exit(1, "")
	}

	var report bytes.Buffer
	if err := checkpoint.WriteReport(&report, store, identifier, changes,
		checkpoint.ReportOptions{Full: full || paging}); err != nil {
		return err
	}
	if err := showCheckpointReport(report.Bytes(), pager, paging); err != nil {
		return err
	}
	return ui.Exit(1, "")
}

// renderCheckpointDiffThroughTool pipes a unified diff into the
// configured tool. Takes the command line, store, identifier, and
// changes.
func renderCheckpointDiffThroughTool(commandLine string,
	store *checkpoint.Store, identifier string,
	changes checkpoint.Changes) error {
	command := exec.Command("sh", "-c", commandLine)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	input, err := command.StdinPipe()
	if err != nil {
		return fmt.Errorf("cannot run `%s`: %w", commandLine, err)
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("cannot run `%s`: %w", commandLine, err)
	}
	// A tool that runs its own pager stops reading when the reader
	// quits, so a write that fails here is the tool's decision.
	_ = checkpoint.WriteUnified(input, os.Stderr, store, identifier, changes)
	input.Close()
	_ = command.Wait()
	return nil
}

// showCheckpointReport prints the report, paging it if the terminal
// is too short. Takes the report bytes, pager command, and whether
// to page.
func showCheckpointReport(report []byte, pager string, paging bool) error {
	if paging && bytes.Count(report, []byte("\n")) >= terminalRows() {
		return pipeThroughCommand(pager, bytes.NewReader(report))
	}
	_, err := os.Stdout.Write(report)
	return err
}

// terminalRows returns the terminal height in lines.
func terminalRows() int {
	rows, _ := ui.TerminalSize()
	return rows
}

// requireCheckpointCommand returns an error if the program in the
// command line is not on the PATH. Takes the command and setting
// name.
func requireCheckpointCommand(commandLine, setting string) error {
	program := strings.Fields(commandLine)
	if len(program) == 0 {
		return nil
	}
	if _, err := exec.LookPath(program[0]); err != nil {
		return fmt.Errorf("%s is set to `%s`, but %s is not on your PATH; "+
			"install it, or clear the setting",
			setting, commandLine, program[0])
	}
	return nil
}

// reportCheckpointSkips warns about each path that could not be
// recorded.
func reportCheckpointSkips(skipped []checkpoint.Skipped) {
	for _, entry := range skipped {
		ui.Warn("%s is not in the checkpoint: %s", entry.Path, entry.Reason)
	}
}

// runCheckpointRestore puts the tree back to a checkpoint. Records
// the current tree first. Takes the checkpoint identifier.
func runCheckpointRestore(wanted string) error {
	_, store, err := openCheckpointSession()
	if err != nil {
		return err
	}
	identifier, err := store.Resolve(wanted)
	if err != nil {
		return err
	}
	result, restoreErr := store.Restore(identifier)
	if result.Automatic != "" {
		fmt.Printf("current tree recorded as checkpoint %s\n",
			result.Automatic)
	}
	if restoreErr != nil {
		return restoreErr
	}
	printCheckpointRestore(identifier, result)
	fmt.Printf("checkpoint %s is where you were, if you want it back\n",
		result.Automatic)
	return nil
}

// runCheckpointStep moves one checkpoint in the given direction.
// Takes -1 for back and 1 for forward.
func runCheckpointStep(direction int) error {
	_, store, err := openCheckpointSession()
	if err != nil {
		return err
	}
	identifier, err := store.Step(direction)
	if err != nil {
		return err
	}
	result, err := store.RestoreWithoutAutomatic(identifier)
	if err != nil {
		return err
	}
	printCheckpointRestore(identifier, result)
	return nil
}

// printCheckpointRestore prints the restore result.
func printCheckpointRestore(identifier string,
	result checkpoint.RestoreResult) {
	fmt.Printf("restored checkpoint %s: %d entries in place, %d removed\n",
		identifier, result.Written, result.Removed)
}

// runCheckpointMerge merges a checkpoint into the tree. Takes the
// checkpoint, base identifier, and dry-run flag. Exits 1 on
// conflicts.
func runCheckpointMerge(wanted, base string, dryRun bool) error {
	_, store, err := openCheckpointSession()
	if err != nil {
		return err
	}
	result, err := store.Merge(wanted, checkpoint.MergeOptions{
		BaseID: base,
		DryRun: dryRun,
	})
	if err != nil {
		return err
	}
	if err := checkpoint.WriteMergeReport(
		os.Stdout, result, result.BaseID, result.TheirsID, dryRun); err != nil {
		return err
	}
	if result.Conflicted > 0 {
		return ui.Exit(1, "")
	}
	return nil
}

// runCheckpointPick applies what one checkpoint changed. Takes the
// checkpoint, from identifier, path filters, and dry-run flag.
// Exits 1 on conflicts.
func runCheckpointPick(
	wanted, from string, paths []string, dryRun bool) error {
	_, store, err := openCheckpointSession()
	if err != nil {
		return err
	}
	limited := make([]string, 0, len(paths))
	for _, path := range paths {
		relative, err := store.RelativePath(path)
		if err != nil {
			return err
		}
		limited = append(limited, relative)
	}
	result, err := store.Pick(wanted, checkpoint.PickOptions{
		From:   from,
		Paths:  limited,
		DryRun: dryRun,
	})
	if err != nil {
		return err
	}
	if err := checkpoint.WritePickReport(
		os.Stdout, result, dryRun); err != nil {
		return err
	}
	if result.Conflicted > 0 {
		return ui.Exit(1, "")
	}
	return nil
}

// runCheckpointRemove forgets one checkpoint. Takes the identifier.
func runCheckpointRemove(wanted string) error {
	_, store, err := openCheckpointSession()
	if err != nil {
		return err
	}
	identifier, err := store.Resolve(wanted)
	if err != nil {
		return err
	}
	if err := store.Remove(identifier); err != nil {
		return err
	}
	fmt.Printf("removed checkpoint %s\n", identifier)
	fmt.Println("stored contents are shared between checkpoints and are " +
		"left alone;")
	fmt.Println("`prison checkpoint prune` reclaims what nothing refers to")
	return nil
}

// runCheckpointPrune drops stored contents no checkpoint refers to.
// Prints how much was reclaimed.
func runCheckpointPrune() error {
	_, store, err := openCheckpointSession()
	if err != nil {
		return err
	}
	removed, reclaimed, err := store.Prune()
	if err != nil {
		return err
	}
	fmt.Printf("removed %d stored object(s), %dKB\n", removed, reclaimed/1024)
	return nil
}

// checkpointDriftReminder prints a warning to stderr if the tree
// changed since the last checkpoint. Silent on errors.
func checkpointDriftReminder(current *session.Session) {
	if current == nil || current.Project == nil {
		return
	}
	snapshots := filepath.Join(current.Project.CheckpointsDir(), "snapshots")
	if information, err := os.Stat(snapshots); err != nil ||
		!information.IsDir() {
		return
	}
	store, err := openCheckpointStore(current)
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
	fmt.Fprintf(os.Stderr,
		"%d path(s) changed since checkpoint %s; run `prison checkpoint "+
			"diff`\n", changes.Count(), identifier)
}
