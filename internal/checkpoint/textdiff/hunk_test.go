package textdiff

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// numbered returns count lines labeled "line 1" through "line N".
func numbered(count int) []string {
	lines := make([]string, count)
	for index := range lines {
		lines[index] = fmt.Sprintf("line %d", index+1)
	}
	return lines
}

// hunkSummary formats a hunk's ranges and line count as a string.
func hunkSummary(hunk Hunk) string {
	return fmt.Sprintf("-%d,%d +%d,%d (%d)", hunk.AStart, hunk.ACount,
		hunk.BStart, hunk.BCount, len(hunk.Lines))
}

func TestHunksGroupByContext(t *testing.T) {
	a := numbered(20)
	b := slices.Clone(a)
	b[2] = "changed 3"
	b[8] = "changed 9"
	b = slices.Delete(b, 16, 17)
	edits := Diff(a, b)
	cases := []struct {
		context int
		want    []string
	}{
		{0, []string{"-2,1 +2,1 (2)", "-8,1 +8,1 (2)", "-16,1 +16,0 (1)"}},
		{2, []string{"-0,5 +0,5 (6)", "-6,5 +6,5 (6)", "-14,5 +14,4 (5)"}},
		{3, []string{"-0,12 +0,12 (14)", "-13,7 +13,6 (7)"}},
		{5, []string{"-0,20 +0,19 (22)"}},
	}
	for _, testCase := range cases {
		hunks := Hunks(edits, testCase.context)
		var got []string
		for _, hunk := range hunks {
			got = append(got, hunkSummary(hunk))
		}
		if !slices.Equal(got, testCase.want) {
			t.Errorf("Hunks(context %d) = %v, want %v",
				testCase.context, got, testCase.want)
		}
	}
	if hunks := Hunks(Diff(a, a), 3); hunks != nil {
		t.Errorf("Hunks of an unchanged script = %v, want nil", hunks)
	}
}

func TestUnifiedFormatsHeadersAndRanges(t *testing.T) {
	got := Unified(nil, []string{"only"}, "a/f", "b/f", 3)
	want := []string{"--- a/f", "+++ b/f", "@@ -0,0 +1 @@", "+only"}
	if !slices.Equal(got, want) {
		t.Errorf("Unified(added) = %q, want %q", got, want)
	}
	got = Unified([]string{"x", "y"}, nil, "a/f", "b/f", 3)
	want = []string{"--- a/f", "+++ b/f", "@@ -1,2 +0,0 @@", "-x", "-y"}
	if !slices.Equal(got, want) {
		t.Errorf("Unified(removed) = %q, want %q", got, want)
	}
	if got := Unified([]string{"x"}, []string{"x"}, "a", "b", 3); got != nil {
		t.Errorf("Unified(identical) = %q, want nil", got)
	}
}

// systemDiff runs the system `diff -U` on two files. Returns the
// output lines after the headers, or false when diff is not
// installed.
func systemDiff(t *testing.T, a, b []string, context int) ([]string, bool) {
	t.Helper()
	diffPath, err := exec.LookPath("diff")
	if err != nil {
		return nil, false
	}
	directory := t.TempDir()
	aPath := filepath.Join(directory, "a")
	bPath := filepath.Join(directory, "b")
	if err := os.WriteFile(aPath, []byte(Join(a, true)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bPath, []byte(Join(b, true)), 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(diffPath, "-U", fmt.Sprint(context), aPath, bPath)
	output, err := command.Output()
	var exitError *exec.ExitError
	if err != nil && !(errors.As(err, &exitError) && exitError.ExitCode() == 1) {
		t.Fatalf("diff failed: %v", err)
	}
	lines, _ := Split(string(output))
	if len(lines) < 2 {
		return nil, true
	}
	return lines[2:], true
}

func TestUnifiedMatchesSystemDiff(t *testing.T) {
	base := numbered(30)
	modified := slices.Clone(base)
	modified[4] = "changed 5"
	modified = slices.Insert(modified, 12, "added after 12", "and another")
	modified = slices.Delete(modified, 20, 22)
	modified[len(modified)-1] = "changed last"
	fixtures := []struct {
		name string
		a, b []string
	}{
		{"scattered", base, modified},
		{"added file", nil, numbered(3)},
		{"removed file", numbered(3), nil},
		{"first line", base, append([]string{"new first"}, base[1:]...)},
		{"appended", base, append(slices.Clone(base), "tail")},
		{"block replaced", numbered(8),
			append(append(slices.Clone(numbered(2)), "p", "q", "r"),
				numbered(8)[6:]...)},
	}
	for _, fixture := range fixtures {
		for _, context := range []int{0, 2, 3} {
			want, available := systemDiff(t, fixture.a, fixture.b, context)
			if !available {
				t.Skip("system diff is not installed")
			}
			got := Unified(fixture.a, fixture.b, "a", "b", context)
			if len(got) > 2 {
				got = got[2:]
			}
			if !slices.Equal(got, want) {
				t.Errorf("%s with context %d:\n got:\n%s\nwant:\n%s",
					fixture.name, context, strings.Join(got, "\n"),
					strings.Join(want, "\n"))
			}
		}
	}
}
