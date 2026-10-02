package checkpoint

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildPickFixture makes three checkpoints. `0002` edits `a.txt` and adds
// `notes/`. `0003` reverts them and changes `b.txt`.
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

func TestPickTakesOnlyWhatOneCheckpointChanged(t *testing.T) {
	store := buildPickFixture(t)
	result, err := store.Pick("0002", PickOptions{})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if result.BaseID != "0001" || result.TheirsID != "0002" {
		t.Errorf("Pick = %s with base %s, want 0002 with base 0001",
			result.TheirsID, result.BaseID)
	}
	if result.Conflicted != 0 {
		t.Errorf("Conflicted = %d, want 0", result.Conflicted)
	}
	if got := readProject(t, store, "a.txt"); got != "1\ntwo\n3\n" {
		t.Errorf("a.txt = %q, want the picked change", got)
	}
	if got := readProject(t, store, "notes/new.txt"); got != "note\n" {
		t.Errorf("notes/new.txt = %q, want the picked file", got)
	}
	if got := readProject(t, store, "b.txt"); got != "y\n" {
		t.Errorf("b.txt = %q, want it unchanged", got)
	}
	if result.Automatic == "" {
		t.Error("Automatic = empty, want a checkpoint")
	}
}

func TestPickLimitedToPathsLeavesTheRestAlone(t *testing.T) {
	store := buildPickFixture(t)
	result, err := store.Pick("0002", PickOptions{
		Paths: []string{"notes", "nothing/here.txt"},
	})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if got := readProject(t, store, "notes/new.txt"); got != "note\n" {
		t.Errorf("notes/new.txt = %q, want the picked file", got)
	}
	if got := readProject(t, store, "a.txt"); got != "1\n2\n3\n" {
		t.Errorf("a.txt = %q, want it unchanged", got)
	}
	if len(result.Unmatched) != 1 || result.Unmatched[0] != "nothing/here.txt" {
		t.Errorf("Unmatched = %v, want nothing/here.txt",
			result.Unmatched)
	}
}

func TestPickFromNamesTheCheckpointTheChangeIsMeasuredAgainst(t *testing.T) {
	store := buildPickFixture(t)
	result, err := store.Pick("0002", PickOptions{From: "0003"})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if result.BaseID != "0003" {
		t.Errorf("BaseID = %s, want 0003", result.BaseID)
	}
	if got := readProject(t, store, "b.txt"); got != "x\n" {
		t.Errorf("b.txt = %q, want x", got)
	}
}

func TestPickRefusesTheCheckpointAsItsOwnBase(t *testing.T) {
	store := buildPickFixture(t)
	_, err := store.Pick("0002", PickOptions{From: "2"})
	if err == nil {
		t.Fatal("Pick with From 2 = nil, want an error")
	}
	if !strings.Contains(err.Error(), "--from") {
		t.Errorf("Pick = %q, want an error that names `--from`", err)
	}
}

func TestPickSkipsARemovedCheckpointWhenLookingBack(t *testing.T) {
	store := buildPickFixture(t)
	if err := store.Remove("0002"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	result, err := store.Pick("0003", PickOptions{})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if result.BaseID != "0001" {
		t.Errorf("BaseID = %s, want 0001", result.BaseID)
	}
}

func TestPickTheFirstCheckpointAddsWhatItHolds(t *testing.T) {
	store := buildPickFixture(t)
	removeProject(t, store, "a.txt")
	result, err := store.Pick("0001", PickOptions{})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if result.BaseID != "" {
		t.Errorf("BaseID = %q, want empty", result.BaseID)
	}
	if got := readProject(t, store, "a.txt"); got != "1\n2\n3\n" {
		t.Errorf("a.txt = %q, want the copy in 0001", got)
	}
}

func TestPickRehearsalWritesNothing(t *testing.T) {
	store := buildPickFixture(t)
	result, err := store.Pick("0002", PickOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if len(result.Plan) == 0 {
		t.Fatal("Plan = empty, want changes")
	}
	if result.Automatic != "" {
		t.Errorf("Automatic = %q, want empty", result.Automatic)
	}
	if got := readProject(t, store, "a.txt"); got != "1\n2\n3\n" {
		t.Errorf("a.txt = %q, want it unchanged", got)
	}
	if _, err := os.Lstat(
		filepath.Join(store.Project, "notes")); !os.IsNotExist(err) {
		t.Error("notes/ exists, want no change in a dry run")
	}
}

func TestPickMarksWhatBothSidesChanged(t *testing.T) {
	store := newTestStore(t)
	writeProjectFile(t, store, "a.txt", "1\n2\n3\n", 0o644)
	mustTake(t, store, "first")
	writeProjectFile(t, store, "a.txt", "1\ntheirs\n3\n", 0o644)
	mustTake(t, store, "theirs")
	writeProjectFile(t, store, "a.txt", "1\nours\n3\n", 0o644)
	result, err := store.Pick("0002", PickOptions{})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if result.Conflicted != 1 {
		t.Fatalf("Conflicted = %d, want 1", result.Conflicted)
	}
	body := readProject(t, store, "a.txt")
	for _, marker := range []string{
		"<<<<<<< working tree", "||||||| base 0001",
		"=======", ">>>>>>> checkpoint 0002",
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("a.txt is missing %q:\n%s", marker, body)
		}
	}
}

func TestPickReportNamesTheChangeItTook(t *testing.T) {
	store := buildPickFixture(t)
	result, err := store.Pick("0002", PickOptions{})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	var output bytes.Buffer
	if err := WritePickReport(&output, result, false); err != nil {
		t.Fatalf("WritePickReport: %v", err)
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
			t.Errorf("WritePickReport is missing %q:\n%s", want, report)
		}
	}
}

func TestPickReportSaysWhenAPathHeldNothing(t *testing.T) {
	store := buildPickFixture(t)
	result, err := store.Pick("0002", PickOptions{Paths: []string{"b.txt"}})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	var output bytes.Buffer
	if err := WritePickReport(&output, result, false); err != nil {
		t.Fatalf("WritePickReport: %v", err)
	}
	want := "checkpoint 0002 has no changes in b.txt\n"
	if output.String() != want {
		t.Errorf("WritePickReport = %q, want %q", output.String(), want)
	}
}

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
			t.Errorf("RelativePath(%s): %v", testCase.argument, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("RelativePath(%s) = %q, want %q",
				testCase.argument, got, testCase.want)
		}
	}
	for _, argument := range []string{"..", "../elsewhere", "/etc/passwd"} {
		if _, err := store.RelativePath(argument); err == nil {
			t.Errorf("RelativePath(%s) = nil error, want an error", argument)
		}
	}
}
