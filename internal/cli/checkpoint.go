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

func init() {
	registerGroup(addCheckpointCommands)
}

const checkpointSubcommands = "take, list, diff, merge, pick, restore, " +
	"back, forward, remove, or prune"

func addCheckpointCommands(root *cobra.Command) {
	group := &cobra.Command{
		Use:   "checkpoint",
		Short: "save and restore the project tree",
		Args: func(command *cobra.Command, arguments []string) error {
			if len(arguments) == 0 {
				return nil
			}
			return fmt.Errorf("unknown checkpoint subcommand %s. Use %s",
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

func newCheckpointTakeCommand() *cobra.Command {
	var label string
	command := &cobra.Command{
		Use:   "take [note...]",
		Short: "save the tree as a checkpoint",
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
		"checkpoint label")
	return command
}

func newCheckpointListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list the checkpoints",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runCheckpointList()
		},
	}
}

func newCheckpointDiffCommand() *cobra.Command {
	var full bool
	command := &cobra.Command{
		Use:   "diff [id]",
		Short: "show the changes since a checkpoint",
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
		"print full diffs with no line limits")
	return command
}

func newCheckpointRestoreCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <id>",
		Short: "restore the tree to a checkpoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runCheckpointRestore(arguments[0])
		},
	}
}

func newCheckpointMergeCommand() *cobra.Command {
	var base string
	var dryRun bool
	command := &cobra.Command{
		Use:   "merge <id>",
		Short: "merge the tree with a checkpoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runCheckpointMerge(arguments[0], base, dryRun)
		},
	}
	command.Flags().StringVar(&base, "base", "",
		"common base checkpoint")
	command.Flags().BoolVar(&dryRun, "dry-run", false,
		"show the result but write no files")
	return command
}

func newCheckpointPickCommand() *cobra.Command {
	var from string
	var dryRun bool
	command := &cobra.Command{
		Use:   "pick <id> [path...]",
		Short: "apply the changes of one checkpoint to the tree",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runCheckpointPick(
				arguments[0], from, arguments[1:], dryRun)
		},
	}
	command.Flags().StringVar(&from, "from", "",
		"checkpoint to compare against")
	command.Flags().BoolVar(&dryRun, "dry-run", false,
		"show the result but write no files")
	return command
}

func newCheckpointBackCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "back",
		Short: "restore the previous checkpoint",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runCheckpointStep(-1)
		},
	}
}

func newCheckpointForwardCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "forward",
		Short: "restore the next checkpoint",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runCheckpointStep(1)
		},
	}
}

func newCheckpointRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <id>",
		Short: "remove a checkpoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runCheckpointRemove(arguments[0])
		},
	}
}

func newCheckpointPruneCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "prune",
		Short: "delete unused stored data",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runCheckpointPrune()
		},
	}
}

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
		fmt.Println("no checkpoints")
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
		// Stdout is for the diff tool only, so this message goes to stderr.
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
	// A tool with a pager stops reading when the user quits it, so write
	// errors are ignored.
	_ = checkpoint.WriteUnified(input, os.Stderr, store, identifier, changes)
	input.Close()
	_ = command.Wait()
	return nil
}

func showCheckpointReport(report []byte, pager string, paging bool) error {
	if paging && bytes.Count(report, []byte("\n")) >= terminalRows() {
		return pipeThroughCommand(pager, bytes.NewReader(report))
	}
	_, err := os.Stdout.Write(report)
	return err
}

func terminalRows() int {
	rows, _ := ui.TerminalSize()
	return rows
}

func requireCheckpointCommand(commandLine, setting string) error {
	program := strings.Fields(commandLine)
	if len(program) == 0 {
		return nil
	}
	if _, err := exec.LookPath(program[0]); err != nil {
		return fmt.Errorf("cannot use %s `%s` because %s is not on PATH",
			setting, commandLine, program[0])
	}
	return nil
}

func reportCheckpointSkips(skipped []checkpoint.Skipped) {
	for _, entry := range skipped {
		ui.Warn("%s is not in the checkpoint: %s", entry.Path, entry.Reason)
	}
}

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
	return nil
}

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

func printCheckpointRestore(identifier string,
	result checkpoint.RestoreResult) {
	fmt.Printf("restored checkpoint %s: %d entries in place, %d removed\n",
		identifier, result.Written, result.Removed)
}

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
	return nil
}

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
		"%d path(s) changed since checkpoint %s. "+
			"Run `prison checkpoint diff`\n", changes.Count(), identifier)
}
