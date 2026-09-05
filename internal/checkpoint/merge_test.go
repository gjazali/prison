package checkpoint

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildMergeFixture creates a store with a base (0001), a changed
// tree (0002), and local edits against the base. Returns the store
// with HEAD on 0001 and every merge decision row exercised.
func buildMergeFixture(t *testing.T) *Store {
	t.Helper()
	store := newTestStore(t)
	writeProjectFile(t, store, "t.txt", "base\n", 0o644)
	writeProjectFile(t, store, "gone.txt", "bye\n", 0o644)
	writeProjectFile(t, store, "o.txt", "base\n", 0o644)
	writeProjectFile(t, store, "c.txt", "a\nb\nc\n", 0o644)
	writeProjectFile(t, store, "same.txt", "s\n", 0o644)
	writeProjectFile(t, store, "m.txt", "1\n2\n3\n4\n5\n", 0o644)
	writeProjectFile(t, store, "de1.txt", "one\n", 0o644)
	writeProjectFile(t, store, "de2.txt", "two\n", 0o644)
	linkProject(t, store, "sl", "a")
	writeProjectFile(t, store, "b.bin", "\x00base", 0o644)
	writeProjectFile(t, store, "k", "kind\n", 0o644)
	writeProjectFile(t, store, "mode1.sh", "#!/bin/sh\n", 0o644)
	writeProjectFile(t, store, "mode2.sh", "x\n", 0o644)
	writeProjectFile(t, store, "mode3.sh", "1\n2\n3\n4\n5\n", 0o644)
	mkdirProject(t, store, "olddir")
	writeProjectFile(t, store, "untouched.txt", "u\n", 0o644)
	mustTake(t, store, "base")

	writeProjectFile(t, store, "t.txt", "theirs\n", 0o644)
	writeProjectFile(t, store, "added.txt", "added\n", 0o644)
	removeProject(t, store, "gone.txt")
	writeProjectFile(t, store, "c.txt", "a\nb2\nc\n", 0o644)
	writeProjectFile(t, store, "same.txt", "S\n", 0o644)
	writeProjectFile(t, store, "m.txt", "1\n2\n3\n4\nfive\n", 0o644)
	writeProjectFile(t, store, "de1.txt", "one edited\n", 0o644)
	removeProject(t, store, "de2.txt")
	removeProject(t, store, "sl")
	linkProject(t, store, "sl", "c")
	writeProjectFile(t, store, "b.bin", "\x00theirs", 0o644)
	removeProject(t, store, "k")
	linkProject(t, store, "k", "t.txt")
	chmodProject(t, store, "mode1.sh", 0o755)
	chmodProject(t, store, "mode2.sh", 0o755)
	writeProjectFile(t, store, "mode3.sh", "1\n2\n3\n4\nfive\n", 0o700)
	removeProject(t, store, "olddir")
	writeProjectFile(t, store, "newdir/f.txt", "f\n", 0o644)
	mustTake(t, store, "theirs")

	if _, err := store.RestoreWithoutAutomatic("0001"); err != nil {
		t.Fatalf("putting the tree back to the base: %v", err)
	}
	writeProjectFile(t, store, "o.txt", "ours\n", 0o644)
	writeProjectFile(t, store, "c.txt", "a\nB\nc\n", 0o644)
	writeProjectFile(t, store, "same.txt", "S\n", 0o644)
	writeProjectFile(t, store, "m.txt", "one\n2\n3\n4\n5\n", 0o644)
	removeProject(t, store, "de1.txt")
	writeProjectFile(t, store, "de2.txt", "two edited\n", 0o644)
	removeProject(t, store, "sl")
	linkProject(t, store, "sl", "b")
	writeProjectFile(t, store, "b.bin", "\x00ours", 0o644)
	removeProject(t, store, "k")
	mkdirProject(t, store, "k")
	writeProjectFile(t, store, "mode2.sh", "x\ny\n", 0o644)
	writeProjectFile(t, store, "mode3.sh", "one\n2\n3\n4\n5\n", 0o755)
	return store
}

// expectedMergeRows maps each path to its label and note, joined by
// a bar. Paths unchanged or moved only by one side are absent.
var expectedMergeRows = map[string]string{
	"added.txt":    "added|",
	"b.bin":        "CONFLICT| (binary, contents differ; yours stands)",
	"c.txt":        "CONFLICT| (1 hunk(s))",
	"de1.txt":      "CONFLICT| (you removed it; the checkpoint's copy is back)",
	"de2.txt":      "CONFLICT| (the checkpoint removed it; your copy stands)",
	"gone.txt":     "removed|",
	"k":            "CONFLICT| (dir here, link in the checkpoint; yours stands)",
	"m.txt":        "merged|",
	"mode1.sh":     "updated|",
	"mode2.sh":     "merged|",
	"mode3.sh":     "merged| (permissions differ, kept 0755)",
	"newdir":       "added|/",
	"newdir/f.txt": "added|",
	"olddir":       "removed|/",
	"sl":           "CONFLICT| (symlink targets differ; yours stands)",
	"t.txt":        "updated|",
}

// checkMergeRows compares plan rows against expectedMergeRows in
// both directions and checks path order.
func checkMergeRows(t *testing.T, plan []MergePlanEntry) {
	t.Helper()
	seen := map[string]string{}
	previous := ""
	for _, item := range plan {
		if item.Path <= previous {
			t.Errorf("plan row %s follows %s; want path order", item.Path, previous)
		}
		previous = item.Path
		seen[item.Path] = item.Label + "|" + item.Note
	}
	for path, want := range expectedMergeRows {
		if got, ok := seen[path]; !ok {
			t.Errorf("no row for %s, want %q", path, want)
		} else if got != want {
			t.Errorf("row for %s is %q, want %q", path, got, want)
		}
	}
	for path, got := range seen {
		if _, ok := expectedMergeRows[path]; !ok {
			t.Errorf("unexpected row for %s: %q", path, got)
		}
	}
}

// TestMergeDecisionTable runs the fixture merge and checks every
// row, the counts, each path's final contents, and the automatic
// checkpoint.
func TestMergeDecisionTable(t *testing.T) {
	store := buildMergeFixture(t)
	beforeMerge, _, err := store.Scan()
	if err != nil {
		t.Fatalf("scanning before the merge: %v", err)
	}

	result, err := store.Merge("2", MergeOptions{})
	if err != nil {
		t.Fatalf("merging: %v", err)
	}
	if result.BaseID != "0001" || result.TheirsID != "0002" {
		t.Errorf("resolved %s and %s, want 0001 and 0002",
			result.BaseID, result.TheirsID)
	}
	checkMergeRows(t, result.Plan)
	if result.Merged != 10 || result.Conflicted != 6 {
		t.Errorf("%d merged, %d conflicted, want 10 and 6",
			result.Merged, result.Conflicted)
	}

	if result.Automatic != "0003" {
		t.Fatalf("the automatic checkpoint is %q, want 0003", result.Automatic)
	}
	metadata, err := store.Metadata("0003")
	if err != nil || metadata.Label != "automatic, before merging 0002" {
		t.Errorf("the automatic label is %q, %v", metadata.Label, err)
	}
	if !Compare(beforeMerge, mustManifest(t, store, "0003")).Empty() {
		t.Error("the automatic checkpoint does not match the tree before " +
			"the merge")
	}

	wantContents := map[string]string{
		"t.txt":        "theirs\n",
		"added.txt":    "added\n",
		"o.txt":        "ours\n",
		"same.txt":     "S\n",
		"m.txt":        "one\n2\n3\n4\nfive\n",
		"de1.txt":      "one edited\n",
		"de2.txt":      "two edited\n",
		"b.bin":        "\x00ours",
		"mode2.sh":     "x\ny\n",
		"mode3.sh":     "one\n2\n3\n4\nfive\n",
		"newdir/f.txt": "f\n",
		"c.txt": strings.Join([]string{
			"a",
			"<<<<<<< working tree",
			"B",
			"||||||| base 0001",
			"b",
			"=======",
			"b2",
			">>>>>>> checkpoint 0002",
			"c",
			"",
		}, "\n"),
	}
	for path, want := range wantContents {
		if got := readProject(t, store, path); got != want {
			t.Errorf("%s holds %q, want %q", path, got, want)
		}
	}
	for _, path := range []string{"gone.txt", "olddir"} {
		if _, err := os.Lstat(filepath.Join(store.Project, path)); err == nil {
			t.Errorf("%s survived the merge", path)
		}
	}
	if target, err := os.Readlink(
		filepath.Join(store.Project, "sl")); err != nil || target != "b" {
		t.Errorf("sl points at %q, %v, want b", target, err)
	}
	if information, err := os.Lstat(
		filepath.Join(store.Project, "k")); err != nil || !information.IsDir() {
		t.Errorf("k is not the directory the working tree made: %v", err)
	}
	for _, path := range []string{"mode1.sh", "mode2.sh", "mode3.sh"} {
		information, err := os.Stat(filepath.Join(store.Project, path))
		if err != nil || information.Mode().Perm() != 0o755 {
			t.Errorf("%s has mode %v, %v, want 0755",
				path, information.Mode(), err)
		}
	}
}

// TestMergeDryRunWritesNothing checks that a rehearsal reports the
// same rows but leaves the tree and store unchanged.
func TestMergeDryRunWritesNothing(t *testing.T) {
	store := buildMergeFixture(t)
	before, _, err := store.Scan()
	if err != nil {
		t.Fatalf("scanning before the rehearsal: %v", err)
	}
	result, err := store.Merge("2", MergeOptions{DryRun: true})
	if err != nil {
		t.Fatalf("rehearsing the merge: %v", err)
	}
	checkMergeRows(t, result.Plan)
	if result.Automatic != "" {
		t.Errorf("a rehearsal recorded checkpoint %s", result.Automatic)
	}
	after, _, err := store.Scan()
	if err != nil {
		t.Fatalf("scanning after the rehearsal: %v", err)
	}
	if changes := Compare(before, after); !changes.Empty() {
		t.Errorf("the rehearsal changed the tree: %+v", changes)
	}
	checkpoints, err := store.List()
	if err != nil || len(checkpoints) != 2 {
		t.Errorf("List = %d checkpoints, %v, want the original 2",
			len(checkpoints), err)
	}
	if got := readProject(t, store, "c.txt"); got != "a\nB\nc\n" {
		t.Errorf("c.txt holds %q after a rehearsal", got)
	}
}

// TestMergeReportWording checks the exact report text for a
// rehearsal, a real merge, and a no-op merge.
func TestMergeReportWording(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "t.txt", "base\n", 0o644)
	writeProjectFile(t, store, "u.txt", "u\n", 0o644)
	mustTake(t, store, "base")
	writeProjectFile(t, store, "t.txt", "theirs\n", 0o644)
	mustTake(t, store, "theirs")
	writeProjectFile(t, store, "t.txt", "base\n", 0o644)
	if err := store.SetHead("0001"); err != nil {
		t.Fatalf("setting HEAD: %v", err)
	}
	header := strings.Join([]string{
		"merging checkpoint 0002 into the working tree.",
		"",
		"  base   checkpoint 0001",
		"  ours   the working tree",
		"  theirs checkpoint 0002",
		"",
		"  updated        t.txt",
		"",
		"1 path(s) merged, 0 conflicted.",
	}, "\n") + "\n"

	rehearsal, err := store.Merge("2", MergeOptions{DryRun: true})
	if err != nil {
		t.Fatalf("rehearsing: %v", err)
	}
	var output bytes.Buffer
	if err := WriteMergeReport(&output, rehearsal, rehearsal.BaseID,
		rehearsal.TheirsID, true); err != nil {
		t.Fatalf("writing the rehearsal report: %v", err)
	}
	want := header + "Nothing was written; this was a rehearsal.\n"
	if output.String() != want {
		t.Errorf("rehearsal report:\n%s\nwant:\n%s", output.String(), want)
	}

	result, err := store.Merge("2", MergeOptions{})
	if err != nil {
		t.Fatalf("merging: %v", err)
	}
	output.Reset()
	if err := WriteMergeReport(&output, result, result.BaseID,
		result.TheirsID, false); err != nil {
		t.Fatalf("writing the merge report: %v", err)
	}
	want = header + "\n" +
		"checkpoint 0003 is the tree from before this merge, and " +
		"`prison checkpoint diff`\n" +
		"now shows what the merge changed.\n"
	if output.String() != want {
		t.Errorf("merge report:\n%s\nwant:\n%s", output.String(), want)
	}

	again, err := store.Merge("2", MergeOptions{BaseID: "1"})
	if err != nil {
		t.Fatalf("merging a second time: %v", err)
	}
	if len(again.Plan) != 0 || again.Automatic != "" {
		t.Errorf("a merge with nothing to do gave %+v", again)
	}
	output.Reset()
	if err := WriteMergeReport(&output, again, again.BaseID, again.TheirsID,
		false); err != nil {
		t.Fatalf("writing the empty report: %v", err)
	}
	want = "checkpoint 0002 holds nothing this tree does not already have\n"
	if output.String() != want {
		t.Errorf("empty report:\n%s\nwant:\n%s", output.String(), want)
	}
}

// TestMergeConflictReportParagraph checks that a conflicted merge
// report explains the diff3 markers.
func TestMergeConflictReportParagraph(t *testing.T) {
	store := buildMergeFixture(t)
	result, err := store.Merge("2", MergeOptions{})
	if err != nil {
		t.Fatalf("merging: %v", err)
	}
	var output bytes.Buffer
	if err := WriteMergeReport(&output, result, result.BaseID,
		result.TheirsID, false); err != nil {
		t.Fatalf("writing the report: %v", err)
	}
	text := output.String()
	for _, want := range []string{
		"  CONFLICT       c.txt (1 hunk(s))\n",
		"  merged         mode3.sh (permissions differ, kept 0755)\n",
		"10 path(s) merged, 6 conflicted.\n",
		"`|||||||`",
		"checkpoint 0003 is the tree from before this merge",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the report lacks %q:\n%s", want, text)
		}
	}
}

// TestMergeBaseResolution checks errors for a missing base, a base
// equal to the checkpoint, and the --base override.
func TestMergeBaseResolution(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "t.txt", "base\n", 0o644)
	mustTake(t, store, "base")
	writeProjectFile(t, store, "t.txt", "theirs\n", 0o644)
	mustTake(t, store, "theirs")
	writeProjectFile(t, store, "t.txt", "base\n", 0o644)
	if err := store.clearHead(); err != nil {
		t.Fatalf("clearing HEAD: %v", err)
	}

	_, err := store.Merge("2", MergeOptions{})
	if err == nil || !strings.Contains(err.Error(), "`--base <id>`") {
		t.Errorf("Merge without a base gave %v", err)
	}
	_, err = store.Merge("2", MergeOptions{BaseID: "2"})
	if err == nil || !strings.Contains(err.Error(), "nothing in it to merge") {
		t.Errorf("Merge with base equal to theirs gave %v", err)
	}
	result, err := store.Merge("2", MergeOptions{BaseID: "1", DryRun: true})
	if err != nil {
		t.Fatalf("Merge with --base: %v", err)
	}
	if len(result.Plan) != 1 || result.Plan[0].Label != "updated" {
		t.Errorf("Merge with --base planned %+v, want t.txt updated",
			result.Plan)
	}
}

// TestPlanMergeCustomLabel checks that Merge uses a custom label in
// conflict markers while PlanMerge uses the default.
func TestPlanMergeCustomLabel(t *testing.T) {
	store := buildMergeFixture(t)
	plan, err := store.PlanMerge("2", "1")
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	var conflicted *MergePlanEntry
	for index := range plan {
		if plan[index].Path == "c.txt" {
			conflicted = &plan[index]
		}
	}
	if conflicted == nil || conflicted.Action != MergeWriteLines ||
		conflicted.Lines[1] != "<<<<<<< working tree" {
		t.Errorf("PlanMerge planned %+v for c.txt", conflicted)
	}
	result, err := store.Merge("2", MergeOptions{OurLabel: "my tree"})
	if err != nil {
		t.Fatalf("merging: %v", err)
	}
	if !strings.Contains(readProject(t, store, "c.txt"),
		"<<<<<<< my tree\n") {
		t.Error("the custom label did not reach the conflict markers")
	}
	if result.Conflicted != 6 {
		t.Errorf("%d conflicted, want 6", result.Conflicted)
	}
}
