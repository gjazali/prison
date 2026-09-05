package checkpoint

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildPickFixture creates a store with three checkpoints. 0002
// edits a.txt and adds notes/. 0003 undoes those and changes b.txt.
// Returns the store with the tree matching 0003.
func buildPickFixture(t *testing.T) *Store {
	t.Helper()
	store := newTestStore(t)
	writeProjectFile(t, store, "a.txt", "1\n2\n3\n", 0o644)
	writeProjectFile(t, store, "b.txt", "x\n", 0o644)
	mustTake(t, store, "first")
	writeProjectFile(t, store, "a.txt", "1\ntwo\n3\n", 0o644)
	writeProjectFile(t, store, "notes/new.txt", "note\n", 0o644)
	mustTake(t, store, "the change worth picking")
	writeProjectFile(t, store, "a.txt", "1\n2\n3\n", 0o644)
	removeProject(t, store, "notes")
	writeProjectFile(t, store, "b.txt", "y\n", 0o644)
	mustTake(t, store, "later work")
	return store
}

// TestPickTakesOnlyWhatOneCheckpointChanged checks that a pick
// applies only what the target checkpoint changed, leaves other
// paths alone, and records an automatic checkpoint.
func TestPickTakesOnlyWhatOneCheckpointChanged(t *testing.T) {
	store := buildPickFixture(t)
	result, err := store.Pick("0002", PickOptions{})
	if err != nil {
		t.Fatalf("picking 0002: %v", err)
	}
	if result.BaseID != "0001" || result.TheirsID != "0002" {
		t.Errorf("picked %s against %s, want 0002 against 0001",
			result.TheirsID, result.BaseID)
	}
	if result.Conflicted != 0 {
		t.Errorf("%d path(s) conflicted, want none", result.Conflicted)
	}
	if got := readProject(t, store, "a.txt"); got != "1\ntwo\n3\n" {
		t.Errorf("a.txt is %q, want the picked change", got)
	}
	if got := readProject(t, store, "notes/new.txt"); got != "note\n" {
		t.Errorf("notes/new.txt is %q, want the picked addition", got)
	}
	if got := readProject(t, store, "b.txt"); got != "y\n" {
		t.Errorf("b.txt is %q, want the later work left alone", got)
	}
	if result.Automatic == "" {
		t.Error("the tree before the pick was not recorded")
	}
}

// TestPickLimitedToPathsLeavesTheRestAlone checks that named paths
// narrow the pick, directories cover their children, and unmatched
// paths are reported.
func TestPickLimitedToPathsLeavesTheRestAlone(t *testing.T) {
	store := buildPickFixture(t)
	result, err := store.Pick("0002", PickOptions{
		Paths: []string{"notes", "nothing/here.txt"},
	})
	if err != nil {
		t.Fatalf("picking 0002: %v", err)
	}
	if got := readProject(t, store, "notes/new.txt"); got != "note\n" {
		t.Errorf("notes/new.txt is %q, want the picked addition", got)
	}
	if got := readProject(t, store, "a.txt"); got != "1\n2\n3\n" {
		t.Errorf("a.txt is %q, want it left alone by a limited pick", got)
	}
	if len(result.Unmatched) != 1 || result.Unmatched[0] != "nothing/here.txt" {
		t.Errorf("unmatched paths are %v, want nothing/here.txt",
			result.Unmatched)
	}
}

// TestPickFromNamesTheCheckpointTheChangeIsMeasuredAgainst checks
// that --from moves the base so the pick measures the change from a
// different starting point.
func TestPickFromNamesTheCheckpointTheChangeIsMeasuredAgainst(t *testing.T) {
	store := buildPickFixture(t)
	result, err := store.Pick("0002", PickOptions{From: "0003"})
	if err != nil {
		t.Fatalf("picking 0002 from 0003: %v", err)
	}
	if result.BaseID != "0003" {
		t.Errorf("measured against %s, want 0003", result.BaseID)
	}
	if got := readProject(t, store, "b.txt"); got != "x\n" {
		t.Errorf("b.txt is %q, want 0003's change to it undone", got)
	}
}

// TestPickRefusesTheCheckpointAsItsOwnBase checks that picking a
// checkpoint against itself fails and the error mentions --from.
func TestPickRefusesTheCheckpointAsItsOwnBase(t *testing.T) {
	store := buildPickFixture(t)
	_, err := store.Pick("0002", PickOptions{From: "2"})
	if err == nil {
		t.Fatal("picking a checkpoint against itself was allowed")
	}
	if !strings.Contains(err.Error(), "--from") {
		t.Errorf("the error is %q, want it to name `--from`", err)
	}
}

// TestPickSkipsARemovedCheckpointWhenLookingBack checks that the
// base falls back to the nearest surviving checkpoint.
func TestPickSkipsARemovedCheckpointWhenLookingBack(t *testing.T) {
	store := buildPickFixture(t)
	if err := store.Remove("0002"); err != nil {
		t.Fatalf("removing 0002: %v", err)
	}
	result, err := store.Pick("0003", PickOptions{})
	if err != nil {
		t.Fatalf("picking 0003: %v", err)
	}
	if result.BaseID != "0001" {
		t.Errorf("measured against %s, want 0001", result.BaseID)
	}
}

// TestPickTheFirstCheckpointAddsWhatItHolds checks that the first
// checkpoint is measured against an empty tree, treating all its
// entries as additions.
func TestPickTheFirstCheckpointAddsWhatItHolds(t *testing.T) {
	store := buildPickFixture(t)
	removeProject(t, store, "a.txt")
	result, err := store.Pick("0001", PickOptions{})
	if err != nil {
		t.Fatalf("picking 0001: %v", err)
	}
	if result.BaseID != "" {
		t.Errorf("measured against %q, want an empty tree", result.BaseID)
	}
	if got := readProject(t, store, "a.txt"); got != "1\n2\n3\n" {
		t.Errorf("a.txt is %q, want the first checkpoint's copy back", got)
	}
}

// TestPickRehearsalWritesNothing checks that a rehearsal plans
// the pick but leaves the tree and store unchanged.
func TestPickRehearsalWritesNothing(t *testing.T) {
	store := buildPickFixture(t)
	result, err := store.Pick("0002", PickOptions{DryRun: true})
	if err != nil {
		t.Fatalf("rehearsing the pick of 0002: %v", err)
	}
	if len(result.Plan) == 0 {
		t.Fatal("the rehearsal planned nothing")
	}
	if result.Automatic != "" {
		t.Errorf("the rehearsal recorded checkpoint %s", result.Automatic)
	}
	if got := readProject(t, store, "a.txt"); got != "1\n2\n3\n" {
		t.Errorf("a.txt is %q, want the rehearsal to have left it", got)
	}
	if _, err := os.Lstat(
		filepath.Join(store.Project, "notes")); !os.IsNotExist(err) {
		t.Error("the rehearsal created notes/")
	}
}

// TestPickMarksWhatBothSidesChanged checks that lines changed by
// both sides produce a conflict with diff3 markers.
func TestPickMarksWhatBothSidesChanged(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "a.txt", "1\n2\n3\n", 0o644)
	mustTake(t, store, "first")
	writeProjectFile(t, store, "a.txt", "1\ntheirs\n3\n", 0o644)
	mustTake(t, store, "theirs")
	writeProjectFile(t, store, "a.txt", "1\nours\n3\n", 0o644)
	result, err := store.Pick("0002", PickOptions{})
	if err != nil {
		t.Fatalf("picking 0002: %v", err)
	}
	if result.Conflicted != 1 {
		t.Fatalf("%d path(s) conflicted, want 1", result.Conflicted)
	}
	body := readProject(t, store, "a.txt")
	for _, marker := range []string{
		"<<<<<<< working tree", "||||||| base 0001",
		"=======", ">>>>>>> checkpoint 0002",
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("a.txt does not carry %q:\n%s", marker, body)
		}
	}
}

// TestPickReportNamesTheChangeItTook checks the pick report's
// wording: header, sides, counts, and closing line.
func TestPickReportNamesTheChangeItTook(t *testing.T) {
	store := buildPickFixture(t)
	result, err := store.Pick("0002", PickOptions{})
	if err != nil {
		t.Fatalf("picking 0002: %v", err)
	}
	var output bytes.Buffer
	if err := WritePickReport(&output, result, false); err != nil {
		t.Fatalf("writing the report: %v", err)
	}
	report := output.String()
	for _, want := range []string{
		"picking checkpoint 0002 into the working tree.",
		"  base   checkpoint 0001",
		"  theirs checkpoint 0002",
		"3 path(s) picked, 0 conflicted.",
		"is the tree from before this pick",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not hold %q:\n%s", want, report)
		}
	}
}

// TestPickReportSaysWhenAPathHeldNothing checks that a pick limited
// to an untouched path prints that the checkpoint changed nothing.
func TestPickReportSaysWhenAPathHeldNothing(t *testing.T) {
	store := buildPickFixture(t)
	result, err := store.Pick("0002", PickOptions{Paths: []string{"b.txt"}})
	if err != nil {
		t.Fatalf("picking 0002: %v", err)
	}
	var output bytes.Buffer
	if err := WritePickReport(&output, result, false); err != nil {
		t.Fatalf("writing the report: %v", err)
	}
	want := "checkpoint 0002 changed nothing at b.txt\n"
	if output.String() != want {
		t.Errorf("the report is %q, want %q", output.String(), want)
	}
}

// TestRelativePathKeepsPathsInsideTheProject checks that relative
// and absolute arguments resolve to project-relative slash paths,
// and that paths outside the project are refused.
func TestRelativePathKeepsPathsInsideTheProject(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "here.txt", "here\n", 0o644)
	cases := []struct{ argument, want string }{
		{"a.txt", "a.txt"},
		{"./src/main.go", "src/main.go"},
		{"src/", "src"},
		{".", "."},
		{filepath.Join(store.Project, "src", "main.go"), "src/main.go"},
		{filepath.Join(store.Project, "here.txt"), "here.txt"},
		{filepath.Join(store.Project, "gone", "deep.txt"), "gone/deep.txt"},
	}
	for _, testCase := range cases {
		got, err := store.RelativePath(testCase.argument)
		if err != nil {
			t.Errorf("%s: %v", testCase.argument, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s became %q, want %q",
				testCase.argument, got, testCase.want)
		}
	}
	for _, argument := range []string{"..", "../elsewhere", "/etc/passwd"} {
		if _, err := store.RelativePath(argument); err == nil {
			t.Errorf("%s was accepted as a path inside the project", argument)
		}
	}
}
