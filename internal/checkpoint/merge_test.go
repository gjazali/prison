package checkpoint

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildMergeFixture covers every row of the merge decision table. HEAD is
// `0001`.
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
		t.Fatalf("RestoreWithoutAutomatic: %v", err)
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

var expectedMergeRows = map[string]string{
	"added.txt":    "added|",
	"b.bin":        "CONFLICT| (binary, contents differ, kept yours)",
	"c.txt":        "CONFLICT| (1 hunk(s))",
	"de1.txt":      "CONFLICT| (removed here, restored from the checkpoint)",
	"de2.txt":      "CONFLICT| (removed in the checkpoint, kept yours)",
	"gone.txt":     "removed|",
	"k":            "CONFLICT| (dir here, link in the checkpoint, kept yours)",
	"m.txt":        "merged|",
	"mode1.sh":     "updated|",
	"mode2.sh":     "merged|",
	"mode3.sh":     "merged| (permissions differ, kept 0755)",
	"newdir":       "added|/",
	"newdir/f.txt": "added|",
	"olddir":       "removed|/",
	"sl":           "CONFLICT| (symlink targets differ, kept yours)",
	"t.txt":        "updated|",
}

func checkMergeRows(t *testing.T, plan []MergePlanEntry) {
	t.Helper()
	seen := map[string]string{}
	previous := ""
	for _, item := range plan {
		if item.Path <= previous {
			t.Errorf("plan = %s after %s, want path order", item.Path, previous)
		}
		previous = item.Path
		seen[item.Path] = item.Label + "|" + item.Note
	}
	for path, want := range expectedMergeRows {
		if got, ok := seen[path]; !ok {
			t.Errorf("row %s = missing, want %q", path, want)
		} else if got != want {
			t.Errorf("row %s = %q, want %q", path, got, want)
		}
	}
	for path, got := range seen {
		if _, ok := expectedMergeRows[path]; !ok {
			t.Errorf("row %s = %q, want none", path, got)
		}
	}
}

func TestMergeDecisionTable(t *testing.T) {
	store := buildMergeFixture(t)
	beforeMerge, _, err := store.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	result, err := store.Merge("2", MergeOptions{})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if result.BaseID != "0001" || result.TheirsID != "0002" {
		t.Errorf("Merge = base %s, theirs %s, want 0001 and 0002",
			result.BaseID, result.TheirsID)
	}
	checkMergeRows(t, result.Plan)
	if result.Merged != 10 || result.Conflicted != 6 {
		t.Errorf("Merge = %d merged, %d conflicted, want 10 and 6",
			result.Merged, result.Conflicted)
	}

	if result.Automatic != "0003" {
		t.Fatalf("Automatic = %q, want 0003", result.Automatic)
	}
	metadata, err := store.Metadata("0003")
	if err != nil || metadata.Label != "automatic, before merging 0002" {
		t.Errorf("Label = %q, %v, want %q", metadata.Label, err,
			"automatic, before merging 0002")
	}
	if !Compare(beforeMerge, mustManifest(t, store, "0003")).Empty() {
		t.Error("Compare(before, 0003) = changes, want none")
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
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	for _, path := range []string{"gone.txt", "olddir"} {
		if _, err := os.Lstat(filepath.Join(store.Project, path)); err == nil {
			t.Errorf("%s exists, want removed", path)
		}
	}
	if target, err := os.Readlink(
		filepath.Join(store.Project, "sl")); err != nil || target != "b" {
		t.Errorf("Readlink(sl) = %q, %v, want b", target, err)
	}
	if information, err := os.Lstat(
		filepath.Join(store.Project, "k")); err != nil || !information.IsDir() {
		t.Errorf("k = not a directory, %v, want the local directory", err)
	}
	for _, path := range []string{"mode1.sh", "mode2.sh", "mode3.sh"} {
		information, err := os.Stat(filepath.Join(store.Project, path))
		if err != nil || information.Mode().Perm() != 0o755 {
			t.Errorf("%s mode = %v, %v, want 0755",
				path, information.Mode(), err)
		}
	}
}

func TestMergeDryRunWritesNothing(t *testing.T) {
	store := buildMergeFixture(t)
	before, _, err := store.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	result, err := store.Merge("2", MergeOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	checkMergeRows(t, result.Plan)
	if result.Automatic != "" {
		t.Errorf("Automatic = %q, want empty", result.Automatic)
	}
	after, _, err := store.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if changes := Compare(before, after); !changes.Empty() {
		t.Errorf("Compare = %+v, want no changes", changes)
	}
	checkpoints, err := store.List()
	if err != nil || len(checkpoints) != 2 {
		t.Errorf("List = %d checkpoints, %v, want 2",
			len(checkpoints), err)
	}
	if got := readProject(t, store, "c.txt"); got != "a\nB\nc\n" {
		t.Errorf("c.txt = %q, want it unchanged", got)
	}
}

func TestMergeReportWording(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "t.txt", "base\n", 0o644)
	writeProjectFile(t, store, "u.txt", "u\n", 0o644)
	mustTake(t, store, "base")
	writeProjectFile(t, store, "t.txt", "theirs\n", 0o644)
	mustTake(t, store, "theirs")
	writeProjectFile(t, store, "t.txt", "base\n", 0o644)
	if err := store.SetHead("0001"); err != nil {
		t.Fatalf("SetHead: %v", err)
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
		t.Fatalf("Merge: %v", err)
	}
	var output bytes.Buffer
	if err := WriteMergeReport(&output, rehearsal, rehearsal.BaseID,
		rehearsal.TheirsID, true); err != nil {
		t.Fatalf("WriteMergeReport: %v", err)
	}
	want := header + "Dry run. Nothing was written.\n"
	if output.String() != want {
		t.Errorf("WriteMergeReport =\n%s\nwant:\n%s", output.String(), want)
	}

	result, err := store.Merge("2", MergeOptions{})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	output.Reset()
	if err := WriteMergeReport(&output, result, result.BaseID,
		result.TheirsID, false); err != nil {
		t.Fatalf("WriteMergeReport: %v", err)
	}
	want = header + "\n" +
		"checkpoint 0003 is the tree from before this merge.\n"
	if output.String() != want {
		t.Errorf("WriteMergeReport =\n%s\nwant:\n%s", output.String(), want)
	}

	again, err := store.Merge("2", MergeOptions{BaseID: "1"})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(again.Plan) != 0 || again.Automatic != "" {
		t.Errorf("Merge = %+v, want an empty plan", again)
	}
	output.Reset()
	if err := WriteMergeReport(&output, again, again.BaseID, again.TheirsID,
		false); err != nil {
		t.Fatalf("WriteMergeReport: %v", err)
	}
	want = "checkpoint 0002 has no new changes\n"
	if output.String() != want {
		t.Errorf("WriteMergeReport =\n%s\nwant:\n%s", output.String(), want)
	}
}

func TestMergeConflictReportParagraph(t *testing.T) {
	store := buildMergeFixture(t)
	result, err := store.Merge("2", MergeOptions{})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	var output bytes.Buffer
	if err := WriteMergeReport(&output, result, result.BaseID,
		result.TheirsID, false); err != nil {
		t.Fatalf("WriteMergeReport: %v", err)
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
			t.Errorf("WriteMergeReport is missing %q:\n%s", want, text)
		}
	}
}

func TestMergeBaseResolution(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "t.txt", "base\n", 0o644)
	mustTake(t, store, "base")
	writeProjectFile(t, store, "t.txt", "theirs\n", 0o644)
	mustTake(t, store, "theirs")
	writeProjectFile(t, store, "t.txt", "base\n", 0o644)
	if err := store.clearHead(); err != nil {
		t.Fatalf("clearHead: %v", err)
	}

	_, err := store.Merge("2", MergeOptions{})
	if err == nil || !strings.Contains(err.Error(), "`--base <id>`") {
		t.Errorf("Merge without a base = %v, want a `--base` error", err)
	}
	_, err = store.Merge("2", MergeOptions{BaseID: "2"})
	if err == nil || !strings.Contains(err.Error(), "is the base of this tree") {
		t.Errorf("Merge with base 2 = %v, want an error", err)
	}
	result, err := store.Merge("2", MergeOptions{BaseID: "1", DryRun: true})
	if err != nil {
		t.Fatalf("Merge with --base: %v", err)
	}
	if len(result.Plan) != 1 || result.Plan[0].Label != "updated" {
		t.Errorf("Merge with --base = %+v, want t.txt updated",
			result.Plan)
	}
}

func TestPlanMergeCustomLabel(t *testing.T) {
	store := buildMergeFixture(t)
	plan, err := store.PlanMerge("2", "1")
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	var conflicted *MergePlanEntry
	for index := range plan {
		if plan[index].Path == "c.txt" {
			conflicted = &plan[index]
		}
	}
	if conflicted == nil || conflicted.Action != MergeWriteLines ||
		conflicted.Lines[1] != "<<<<<<< working tree" {
		t.Errorf("PlanMerge c.txt = %+v, want conflict markers", conflicted)
	}
	result, err := store.Merge("2", MergeOptions{OurLabel: "my tree"})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !strings.Contains(readProject(t, store, "c.txt"),
		"<<<<<<< my tree\n") {
		t.Error("c.txt = no \"<<<<<<< my tree\" marker, want one")
	}
	if result.Conflicted != 6 {
		t.Errorf("Conflicted = %d, want 6", result.Conflicted)
	}
}
