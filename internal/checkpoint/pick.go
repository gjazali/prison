package checkpoint

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
)

// emptyBaseLabel is the label for picks with no prior checkpoint.
// Everything in the picked checkpoint counts as an addition.
const emptyBaseLabel = "an empty tree"

// PickOptions controls how a pick runs. From is the base checkpoint,
// Paths limits to specific subtrees, and DryRun skips writing.
type PickOptions struct {
	From   string
	Paths  []string
	DryRun bool
}

// PickResult describes what a pick did. Embeds MergeResult. Unmatched
// holds path arguments that matched nothing in the checkpoint.
type PickResult struct {
	MergeResult
	Unmatched []string
}

// Pick applies the changes from one checkpoint to the working tree.
// Takes a checkpoint identifier and PickOptions. Records an automatic
// checkpoint before writing unless dry-run. Returns a PickResult with
// merge counts and unmatched paths.
func (store *Store) Pick(
	wanted string, options PickOptions) (PickResult, error) {
	theirsID, err := store.Resolve(wanted)
	if err != nil {
		return PickResult{}, err
	}
	baseID, base, err := store.pickBase(theirsID, options.From)
	if err != nil {
		return PickResult{}, err
	}
	theirs, err := store.Manifest(theirsID)
	if err != nil {
		return PickResult{}, err
	}
	baseLabel := emptyBaseLabel
	if baseID != "" {
		baseLabel = "base " + baseID
	}
	plan, err := store.planMergeManifests(base, theirs, mergeLabels{
		ours:   defaultOurLabel,
		base:   baseLabel,
		theirs: "checkpoint " + theirsID,
	})
	if err != nil {
		return PickResult{}, err
	}
	result := PickResult{
		MergeResult: MergeResult{BaseID: baseID, TheirsID: theirsID},
	}
	plan, result.Unmatched = filterPlanByPaths(plan, options.Paths)
	if len(plan) == 0 {
		return result, nil
	}
	if !options.DryRun {
		automatic, err := store.Take("automatic, before picking " + theirsID)
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

// pickBase returns the base checkpoint for a pick as an identifier and
// manifest. Takes the picked checkpoint id and the wanted base (empty
// for the previous one). Returns empty id and nil manifest for the
// first checkpoint. Fails if the base equals the picked checkpoint.
func (store *Store) pickBase(
	theirsID, wanted string) (string, Manifest, error) {
	if wanted == "" {
		previous, err := store.previousIdentifier(theirsID)
		if err != nil {
			return "", nil, err
		}
		if previous == "" {
			return "", nil, nil
		}
		wanted = previous
	}
	baseID, err := store.Resolve(wanted)
	if err != nil {
		return "", nil, err
	}
	if baseID == theirsID {
		return "", nil, fmt.Errorf("checkpoint %s cannot be the checkpoint "+
			"its own change is measured against; name another one with "+
			"`--from <id>`", theirsID)
	}
	manifest, err := store.Manifest(baseID)
	if err != nil {
		return "", nil, err
	}
	return baseID, manifest, nil
}

// previousIdentifier returns the checkpoint before the given one in
// numeric order, or empty if it is the first or not found.
func (store *Store) previousIdentifier(identifier string) (string, error) {
	identifiers, err := store.identifiers()
	if err != nil {
		return "", err
	}
	position := slices.Index(identifiers, identifier)
	if position <= 0 {
		return "", nil
	}
	return identifiers[position-1], nil
}

// RelativePath converts a path argument to the project-relative,
// slash-separated form used by manifests. Takes a relative or absolute
// path. Fails if the path lands outside the project.
func (store *Store) RelativePath(argument string) (string, error) {
	cleaned := filepath.Clean(argument)
	if filepath.IsAbs(cleaned) {
		relative, inside := relativeToProject(store.Project, cleaned)
		if !inside {
			return "", fmt.Errorf("%s is not a path inside this project",
				argument)
		}
		cleaned = relative
	}
	slashed := filepath.ToSlash(cleaned)
	if slashed == ".." || strings.HasPrefix(slashed, "../") {
		return "", fmt.Errorf("%s is not a path inside this project", argument)
	}
	return slashed, nil
}

// relativeToProject returns a path relative to the project root and
// whether it landed inside. Tries both the raw path and symlink-resolved
// forms.
func relativeToProject(project, absolute string) (string, bool) {
	root := resolvedPath(project)
	for _, candidate := range []string{absolute, resolvedPath(absolute)} {
		relative, err := filepath.Rel(root, candidate)
		if err != nil || relative == ".." ||
			strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		return relative, true
	}
	return "", false
}

// resolvedPath resolves symlinks as far down as the filesystem goes.
// Returns the original path if nothing can be resolved.
func resolvedPath(path string) string {
	rest := ""
	for current := path; ; {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path
		}
		rest = filepath.Join(filepath.Base(current), rest)
		current = parent
	}
}

// filterPlanByPaths keeps only plan entries under the given paths.
// Returns the filtered plan and any paths that matched nothing. An
// empty path list keeps the whole plan.
func filterPlanByPaths(
	plan []MergePlanEntry, paths []string) ([]MergePlanEntry, []string) {
	if len(paths) == 0 {
		return plan, nil
	}
	kept := make([]MergePlanEntry, 0, len(plan))
	matched := make(map[string]bool, len(paths))
	for _, item := range plan {
		for _, wanted := range paths {
			if pathIsUnder(item.Path, wanted) {
				kept = append(kept, item)
				matched[wanted] = true
				break
			}
		}
	}
	var unmatched []string
	for _, wanted := range paths {
		if !matched[wanted] && !slices.Contains(unmatched, wanted) {
			unmatched = append(unmatched, wanted)
		}
	}
	return kept, unmatched
}

// pathIsUnder returns true if path is at or below wanted. "." matches
// everything.
func pathIsUnder(path, wanted string) bool {
	return wanted == "." || path == wanted ||
		strings.HasPrefix(path, wanted+"/")
}

// WritePickReport prints the pick results. Takes a writer, the
// PickResult, and whether this was a dry run. Returns the first write
// error.
func WritePickReport(
	destination io.Writer, result PickResult, dryRun bool) error {
	out := &lineWriter{destination: destination}
	for _, path := range result.Unmatched {
		out.line("checkpoint %s changed nothing at %s",
			result.TheirsID, path)
	}
	if len(result.Plan) == 0 {
		if len(result.Unmatched) == 0 {
			out.line("checkpoint %s holds nothing this tree does not "+
				"already have", result.TheirsID)
		}
		return out.err
	}
	out.line("picking checkpoint %s into the working tree.", result.TheirsID)
	out.line("")
	out.line("  base   %s", pickBaseText(result.BaseID))
	out.line("  ours   the working tree")
	out.line("  theirs checkpoint %s", result.TheirsID)
	out.line("")
	writeAppliedPlan(out, result.MergeResult, "picked", "pick", dryRun)
	return out.err
}

// pickBaseText names the base in the report header: the checkpoint the
// change was measured against, or an empty tree when there was none.
func pickBaseText(baseID string) string {
	if baseID == "" {
		return emptyBaseLabel
	}
	return "checkpoint " + baseID
}
