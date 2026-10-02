package checkpoint

import (
	"cmp"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"prison/internal/checkpoint/textdiff"
)

const defaultOurLabel = "working tree"

type MergeAction int

const (
	MergeKeep MergeAction = iota
	MergeWriteEntry
	MergeWriteLines
	MergeDelete
)

type MergePlanEntry struct {
	Path            string
	Action          MergeAction
	Label           string
	Note            string
	Conflicts       int
	Entry           Entry
	Lines           []string
	Mode            os.FileMode
	TrailingNewline bool
}

func (item MergePlanEntry) isDirectory() bool {
	return (item.Action == MergeWriteEntry || item.Action == MergeDelete) &&
		item.Entry.Kind == KindDirectory
}

type MergeOptions struct {
	BaseID   string
	DryRun   bool
	OurLabel string
}

type MergeResult struct {
	BaseID     string
	TheirsID   string
	Automatic  string
	Plan       []MergePlanEntry
	Merged     int
	Conflicted int
}

type mergeLabels struct {
	ours   string
	base   string
	theirs string
}

func (store *Store) PlanMerge(
	theirsWanted, baseWanted string) ([]MergePlanEntry, error) {
	theirsID, err := store.Resolve(theirsWanted)
	if err != nil {
		return nil, err
	}
	baseID, err := store.Resolve(baseWanted)
	if err != nil {
		return nil, err
	}
	return store.planMerge(theirsID, baseID, defaultOurLabel)
}

func (store *Store) planMerge(
	theirsID, baseID, ourLabel string) ([]MergePlanEntry, error) {
	base, err := store.Manifest(baseID)
	if err != nil {
		return nil, err
	}
	theirs, err := store.Manifest(theirsID)
	if err != nil {
		return nil, err
	}
	return store.planMergeManifests(base, theirs, mergeLabels{
		ours:   ourLabel,
		base:   "base " + baseID,
		theirs: "checkpoint " + theirsID,
	})
}

func (store *Store) planMergeManifests(base, theirs Manifest,
	labels mergeLabels) ([]MergePlanEntry, error) {
	ours, _, err := store.Scan()
	if err != nil {
		return nil, err
	}

	baseByPath := base.Index()
	ourByPath := ours.Index()
	theirByPath := theirs.Index()
	paths := make(map[string]bool,
		len(baseByPath)+len(ourByPath)+len(theirByPath))
	for _, index := range []map[string]Entry{baseByPath, ourByPath, theirByPath} {
		for path := range index {
			paths[path] = true
		}
	}
	var plan []MergePlanEntry
	for _, path := range slices.Sorted(maps.Keys(paths)) {
		item, needed := store.planMergedPath(path,
			lookupEntry(baseByPath, path), lookupEntry(ourByPath, path),
			lookupEntry(theirByPath, path), labels)
		if needed {
			plan = append(plan, item)
		}
	}
	return plan, nil
}

func lookupEntry(index map[string]Entry, path string) *Entry {
	entry, ok := index[path]
	if !ok {
		return nil
	}
	return &entry
}

func sameSignature(left, right *Entry) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Kind == right.Kind &&
		left.Mode.Perm() == right.Mode.Perm() &&
		left.Digest == right.Digest
}

func entryKindNote(entry Entry) string {
	switch entry.Kind {
	case KindDirectory:
		return "/"
	case KindLink:
		return " (symlink)"
	}
	return ""
}

func (store *Store) planMergedPath(path string, base, ours, theirs *Entry,
	labels mergeLabels) (MergePlanEntry, bool) {
	if sameSignature(theirs, base) || sameSignature(ours, theirs) {
		return MergePlanEntry{}, false
	}
	if sameSignature(ours, base) {
		if theirs == nil {
			return MergePlanEntry{
				Path: path, Action: MergeDelete, Label: "removed",
				Note: entryKindNote(*base), Entry: *base,
			}, true
		}
		label := "updated"
		if base == nil {
			label = "added"
		}
		return MergePlanEntry{
			Path: path, Action: MergeWriteEntry, Label: label,
			Note: entryKindNote(*theirs), Entry: *theirs,
		}, true
	}

	conflict := MergePlanEntry{
		Path: path, Action: MergeKeep, Label: "CONFLICT", Conflicts: 1,
	}
	switch {
	case ours == nil:
		conflict.Action = MergeWriteEntry
		conflict.Entry = *theirs
		conflict.Note = " (removed here, restored from the checkpoint)"
	case theirs == nil:
		conflict.Note = " (removed in the checkpoint, kept yours)"
	case ours.Kind != theirs.Kind:
		conflict.Note = fmt.Sprintf(" (%s here, %s in the checkpoint, kept yours)",
			ours.Kind, theirs.Kind)
	case ours.Kind == KindLink:
		conflict.Note = " (symlink targets differ, kept yours)"
	default:
		return store.planMergedFile(path, base, *ours, *theirs, labels), true
	}
	return conflict, true
}

func (store *Store) planMergedFile(path string, base *Entry,
	ours, theirs Entry, labels mergeLabels) MergePlanEntry {
	ourData, err := os.ReadFile(filepath.Join(store.Project, path))
	ourLines, ourIsText := textLines(ourData)
	if err != nil {
		ourIsText = false
	}
	theirData, err := store.Blob(theirs.Digest)
	theirLines, theirIsText := textLines(theirData)
	if err != nil {
		theirIsText = false
	}
	if !ourIsText || !theirIsText {
		return MergePlanEntry{
			Path: path, Action: MergeKeep, Label: "CONFLICT", Conflicts: 1,
			Note: " (binary, contents differ, kept yours)",
		}
	}

	var baseLines []string
	if base != nil && base.Kind == KindFile {
		baseLines, _ = store.storedText(base.Digest)
	}
	merged := textdiff.Merge3(baseLines, ourLines, theirLines,
		labels.ours, labels.base, labels.theirs)
	mode, modeNote := resolveMergedMode(base, ours.Mode, theirs.Mode)

	item := MergePlanEntry{
		Path:            path,
		Action:          MergeWriteLines,
		Label:           "merged",
		Note:            modeNote,
		Conflicts:       merged.Conflicts,
		Lines:           merged.Lines,
		Mode:            mode,
		TrailingNewline: endsWithNewline(ourData) || endsWithNewline(theirData),
	}
	if merged.Conflicts > 0 {
		item.Label = "CONFLICT"
		item.Note = fmt.Sprintf(" (%d hunk(s))%s", merged.Conflicts, modeNote)
	}
	return item
}

func endsWithNewline(data []byte) bool {
	return len(data) == 0 || data[len(data)-1] == '\n'
}

func resolveMergedMode(
	base *Entry, ourMode, theirMode os.FileMode) (os.FileMode, string) {
	ourMode, theirMode = ourMode.Perm(), theirMode.Perm()
	if ourMode == theirMode {
		return ourMode, ""
	}
	baseMode := ourMode
	if base != nil {
		baseMode = base.Mode.Perm()
	}
	switch {
	case ourMode == baseMode:
		return theirMode, ""
	case theirMode == baseMode:
		return ourMode, ""
	}
	return ourMode, fmt.Sprintf(" (permissions differ, kept %04o)",
		uint32(ourMode))
}

// Merge brings a checkpoint into the working tree and keeps local changes.
func (store *Store) Merge(
	theirsWanted string, options MergeOptions) (MergeResult, error) {
	theirsID, err := store.Resolve(theirsWanted)
	if err != nil {
		return MergeResult{}, err
	}
	baseID, err := store.mergeBase(theirsID, options.BaseID)
	if err != nil {
		return MergeResult{}, err
	}
	ourLabel := options.OurLabel
	if ourLabel == "" {
		ourLabel = defaultOurLabel
	}
	plan, err := store.planMerge(theirsID, baseID, ourLabel)
	if err != nil {
		return MergeResult{}, err
	}
	result := MergeResult{BaseID: baseID, TheirsID: theirsID}
	if len(plan) == 0 {
		return result, nil
	}
	if !options.DryRun {
		automatic, err := store.Take("automatic, before merging " + theirsID)
		if err != nil {
			return result, err
		}
		result.Automatic = automatic.ID
	}
	result.Plan, err = store.applyMergePlan(plan, options.DryRun)
	for _, item := range result.Plan {
		if item.Conflicts > 0 {
			result.Conflicted++
		} else {
			result.Merged++
		}
	}
	return result, err
}

// mergeBase requires a base. Without one, every difference is a conflict.
func (store *Store) mergeBase(theirsID, wanted string) (string, error) {
	if wanted == "" {
		head, ok := store.Head()
		if !ok {
			return "", fmt.Errorf("this tree has no base checkpoint. " +
				"Use `--base <id>`")
		}
		wanted = head
	}
	baseID, err := store.Resolve(wanted)
	if err != nil {
		return "", err
	}
	if baseID == theirsID {
		return "", fmt.Errorf("checkpoint %s is the base of this tree. "+
			"Run `prison checkpoint restore` to go back to it", theirsID)
	}
	return baseID, nil
}

// applyMergePlan creates directories first and removes them last and
// deepest first. So it never removes the parent of a kept path.
func (store *Store) applyMergePlan(
	plan []MergePlanEntry, dryRun bool) ([]MergePlanEntry, error) {
	var applied []MergePlanEntry
	finish := func(err error) ([]MergePlanEntry, error) {
		slices.SortFunc(applied, func(left, right MergePlanEntry) int {
			return cmp.Compare(left.Path, right.Path)
		})
		return applied, err
	}

	for _, item := range plan {
		if item.Action == MergeWriteEntry && item.isDirectory() {
			if !dryRun {
				if err := WriteEntry(store.Project, item.Entry, nil); err != nil {
					return finish(err)
				}
			}
			applied = append(applied, item)
		}
	}
	for _, item := range plan {
		if item.isDirectory() ||
			(item.Action != MergeWriteEntry && item.Action != MergeWriteLines) {
			continue
		}
		if !dryRun {
			if err := store.writeMergedPath(item); err != nil {
				return finish(err)
			}
		}
		applied = append(applied, item)
	}

	var drops []MergePlanEntry
	for _, item := range plan {
		if item.Action == MergeDelete {
			drops = append(drops, item)
		}
	}
	slices.SortFunc(drops, func(left, right MergePlanEntry) int {
		return cmp.Compare(right.Path, left.Path)
	})
	for _, item := range drops {
		dropped, err := store.dropMergedPath(item, dryRun)
		if err != nil {
			return finish(err)
		}
		if dropped {
			applied = append(applied, item)
		}
	}
	for _, item := range plan {
		if item.Action == MergeKeep {
			applied = append(applied, item)
		}
	}
	return finish(nil)
}

func (store *Store) writeMergedPath(item MergePlanEntry) error {
	if item.Action == MergeWriteEntry {
		content, err := store.Blob(item.Entry.Digest)
		if err != nil {
			return fmt.Errorf("the stored contents of %s are missing",
				item.Path)
		}
		return WriteEntry(store.Project, item.Entry, content)
	}
	entry := Entry{Kind: KindFile, Mode: item.Mode, Path: item.Path}
	body := textdiff.Join(item.Lines, item.TrailingNewline)
	return WriteEntry(store.Project, entry, []byte(body))
}

// dropMergedPath removes only empty directories, because the remaining
// paths are ones that the merge kept.
func (store *Store) dropMergedPath(
	item MergePlanEntry, dryRun bool) (bool, error) {
	absolute := filepath.Join(store.Project, item.Path)
	if item.Entry.Kind == KindDirectory {
		if dryRun {
			children, err := os.ReadDir(absolute)
			return err == nil && len(children) == 0, nil
		}
		return os.Remove(absolute) == nil, nil
	}
	if dryRun {
		return true, nil
	}
	if err := removePath(absolute); err != nil {
		return false, fmt.Errorf("cannot remove %s: %w", item.Path, err)
	}
	return true, nil
}

func WriteMergeReport(destination io.Writer, result MergeResult,
	baseID, theirsID string, dryRun bool) error {
	out := &lineWriter{destination: destination}
	if len(result.Plan) == 0 {
		out.line("checkpoint %s has no new changes", theirsID)
		return out.err
	}
	out.line("merging checkpoint %s into the working tree.", theirsID)
	out.line("")
	out.line("  base   checkpoint %s", baseID)
	out.line("  ours   the working tree")
	out.line("  theirs checkpoint %s", theirsID)
	out.line("")
	writeAppliedPlan(out, result, "merged", "merge", dryRun)
	return out.err
}

func writeAppliedPlan(out *lineWriter, result MergeResult,
	verb, noun string, dryRun bool) {
	for _, item := range result.Plan {
		out.line("%s", reportRow(item.Label, item.Path, item.Note))
	}
	out.line("")
	out.line("%d path(s) %s, %d conflicted.",
		result.Merged, verb, result.Conflicted)
	if dryRun {
		out.line("Dry run. Nothing was written.")
		return
	}
	if result.Conflicted > 0 {
		out.line("")
		out.line("Conflicted files have markers. Local lines come after " +
			"`<<<<<<<`,")
		out.line("the base after `|||||||`, and the checkpoint after `=======`.")
		out.line("Edit the files to remove the markers.")
	}
	out.line("")
	out.line("checkpoint %s is the tree from before this %s.",
		result.Automatic, noun)
}
