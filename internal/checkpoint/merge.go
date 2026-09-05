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

// defaultOurLabel is the default name for the working tree in conflict
// markers.
const defaultOurLabel = "working tree"

// MergeAction is what happens to one path during a merge.
type MergeAction int

// The four merge actions.
const (
	MergeKeep MergeAction = iota
	MergeWriteEntry
	MergeWriteLines
	MergeDelete
)

// MergePlanEntry is one path's planned resolution in a merge.
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

// isDirectory returns true if this entry creates or removes a
// directory.
func (item MergePlanEntry) isDirectory() bool {
	return (item.Action == MergeWriteEntry || item.Action == MergeDelete) &&
		item.Entry.Kind == KindDirectory
}

// MergeOptions controls how a merge runs.
type MergeOptions struct {
	BaseID   string
	DryRun   bool
	OurLabel string
}

// MergeResult holds the outcome of a merge.
type MergeResult struct {
	BaseID     string
	TheirsID   string
	Automatic  string
	Plan       []MergePlanEntry
	Merged     int
	Conflicted int
}

// mergeLabels names the three sides in conflict markers.
type mergeLabels struct {
	ours   string
	base   string
	theirs string
}

// PlanMerge plans a merge without writing anything. Takes the
// checkpoint to merge and the base checkpoint, both in any identifier
// form. Returns the planned resolutions in path order.
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

// planMerge builds the plan from resolved identifiers.
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

// planMergeManifests does a three-way comparison of base, theirs,
// and the working tree. Takes the base manifest, the theirs manifest,
// and the conflict marker labels. Returns planned resolutions for
// paths that need action.
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

// lookupEntry returns the entry at a path, or nil if absent.
func lookupEntry(index map[string]Entry, path string) *Entry {
	entry, ok := index[path]
	if !ok {
		return nil
	}
	return &entry
}

// sameSignature returns true if two entries match in kind, mode,
// and digest. Two nil entries match.
func sameSignature(left, right *Entry) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Kind == right.Kind &&
		left.Mode.Perm() == right.Mode.Perm() &&
		left.Digest == right.Digest
}

// entryKindNote returns "/" for directories, " (symlink)" for links,
// or "" for files.
func entryKindNote(entry Entry) string {
	switch entry.Kind {
	case KindDirectory:
		return "/"
	case KindLink:
		return " (symlink)"
	}
	return ""
}

// planMergedPath decides what to do with one path. Takes the path,
// the entry from each side (nil if absent), and the marker labels.
// Returns the resolution and whether any action is needed.
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
		conflict.Note = " (you removed it; the checkpoint's copy is back)"
	case theirs == nil:
		conflict.Note = " (the checkpoint removed it; your copy stands)"
	case ours.Kind != theirs.Kind:
		conflict.Note = fmt.Sprintf(" (%s here, %s in the checkpoint; "+
			"yours stands)", ours.Kind, theirs.Kind)
	case ours.Kind == KindLink:
		conflict.Note = " (symlink targets differ; yours stands)"
	default:
		return store.planMergedFile(path, base, *ours, *theirs, labels), true
	}
	return conflict, true
}

// planMergedFile resolves a file both sides changed. Takes the path,
// the base entry (or nil), both sides' entries, and the marker
// labels. Returns the resolution.
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
			Note: " (binary, contents differ; yours stands)",
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
		Path:      path,
		Action:    MergeWriteLines,
		Label:     "merged",
		Note:      modeNote,
		Conflicts: merged.Conflicts,
		Lines:     merged.Lines,
		Mode:      mode,
		// The trailing newline comes from the sides, not the merged result.
		TrailingNewline: endsWithNewline(ourData) || endsWithNewline(theirData),
	}
	if merged.Conflicts > 0 {
		item.Label = "CONFLICT"
		item.Note = fmt.Sprintf(" (%d hunk(s))%s", merged.Conflicts, modeNote)
	}
	return item
}

// endsWithNewline returns true if data ends with a newline. Empty
// data returns true.
func endsWithNewline(data []byte) bool {
	return len(data) == 0 || data[len(data)-1] == '\n'
}

// resolveMergedMode picks the file mode after a merge. Takes the
// base entry (or nil) and each side's mode. Returns the chosen mode
// and a note if the modes disagree.
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

// Merge brings a checkpoint into the working tree while keeping
// local changes. Takes the checkpoint identifier and merge options.
// Returns a MergeResult with the resolutions and conflict count.
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

// mergeBase returns the resolved checkpoint a merge measures both
// sides against. Takes the resolved checkpoint being merged in and what
// the caller named, falling back to HEAD when that is empty. It fails
// when neither names a checkpoint, since a merge with no common
// ancestor would read every difference as a conflict, and when the base
// is the checkpoint itself, since then there is nothing to bring in.
func (store *Store) mergeBase(theirsID, wanted string) (string, error) {
	if wanted == "" {
		head, ok := store.Head()
		if !ok {
			return "", fmt.Errorf("this tree has not been restored to a " +
				"checkpoint, so a merge has nothing to measure both sides " +
				"against; name the checkpoint they both came from with " +
				"`--base <id>`")
		}
		wanted = head
	}
	baseID, err := store.Resolve(wanted)
	if err != nil {
		return "", err
	}
	if baseID == theirsID {
		return "", fmt.Errorf("checkpoint %s is where this tree started, so "+
			"there is nothing in it to merge; `prison checkpoint diff` shows "+
			"what has happened since, and `prison checkpoint restore` puts it "+
			"back", theirsID)
	}
	return baseID, nil
}

// applyMergePlan carries a plan out and returns the resolutions that
// took effect, in path order. Takes the plan and whether to rehearse
// rather than write. Directories are created before the files that
// land in them and dropped after, deepest first, so a parent is never
// removed out from under something the merge kept. A directory that is
// not empty is left and not reported. On a write error the resolutions
// applied so far are returned with it.
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

// writeMergedPath puts one non-directory resolution into the working
// tree: the recorded entry's contents for MergeWriteEntry, the
// reconciled lines for MergeWriteLines.
func (store *Store) writeMergedPath(item MergePlanEntry) error {
	if item.Action == MergeWriteEntry {
		content, err := store.Blob(item.Entry.Digest)
		if err != nil {
			return fmt.Errorf("the stored contents of %s are missing from the "+
				"object store, so the checkpoint cannot be merged in full",
				item.Path)
		}
		return WriteEntry(store.Project, item.Entry, content)
	}
	entry := Entry{Kind: KindFile, Mode: item.Mode, Path: item.Path}
	body := textdiff.Join(item.Lines, item.TrailingNewline)
	return WriteEntry(store.Project, entry, []byte(body))
}

// dropMergedPath removes one path the merge dropped and reports whether
// it went. Takes the resolution and whether this is a rehearsal. A
// directory only goes when nothing is left in it, since whatever
// remains is something the merge kept; a rehearsal reports whether it
// would go.
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

// WriteMergeReport prints what a merge came to: the header naming the
// three sides, one row per path, the merged and conflicted counts, and
// what to do next. Takes the destination, the result, the resolved base
// and checkpoint identifiers, and whether this was a rehearsal. A
// result with an empty plan prints the one line saying the checkpoint
// held nothing new. Returns the first write error.
func WriteMergeReport(destination io.Writer, result MergeResult,
	baseID, theirsID string, dryRun bool) error {
	out := &lineWriter{destination: destination}
	if len(result.Plan) == 0 {
		out.line("checkpoint %s holds nothing this tree does not already have",
			theirsID)
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

// writeAppliedPlan prints the part of a report a merge and a pick
// share: one row per path, the counts, how to settle a conflict, and
// the closing lines naming the checkpoint the previous tree went into.
// Takes the writer, the result, the past-tense verb the count line uses
// and the noun the closing lines use ("merged" and "merge", "picked"
// and "pick"), and whether this was a rehearsal. A rehearsal says so
// and stops, since nothing was written and there is nothing to undo.
func writeAppliedPlan(out *lineWriter, result MergeResult,
	verb, noun string, dryRun bool) {
	for _, item := range result.Plan {
		out.line("%s", reportRow(item.Label, item.Path, item.Note))
	}
	out.line("")
	out.line("%d path(s) %s, %d conflicted.",
		result.Merged, verb, result.Conflicted)
	if dryRun {
		out.line("Nothing was written; this was a rehearsal.")
		return
	}
	if result.Conflicted > 0 {
		out.line("")
		out.line("The conflicted paths are marked above. Where both sides " +
			"changed the same")
		out.line("lines, the file now holds yours between `<<<<<<<` and " +
			"`|||||||`, the base")
		out.line("between `|||||||` and `=======`, and the checkpoint's up " +
			"to `>>>>>>>`;")
		out.line("editing those out is what settles it.")
	}
	out.line("")
	out.line("checkpoint %s is the tree from before this %s, and "+
		"`prison checkpoint diff`", result.Automatic, noun)
	out.line("now shows what the %s changed.", noun)
}
