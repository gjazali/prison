package textdiff

import (
	"math/rand"
	"slices"
	"strings"
	"testing"
)

// mergeWords merges three space-separated fixtures with fixed
// labels.
func mergeWords(base, ours, theirs string) MergeResult {
	return Merge3(words(base), words(ours), words(theirs),
		"ours", "base", "theirs")
}

func TestMerge3ClassicCases(t *testing.T) {
	cases := []struct {
		name               string
		base, ours, theirs string
		want               string
		conflicts          int
	}{
		{"both append different lines", "a b c", "a b c X", "a b c Y",
			"a b c <<<<<<<_ours X |||||||_base ======= Y >>>>>>>_theirs", 1},
		{"different regions", "a b c d e", "A b c d e", "a b c d E",
			"A b c d E", 0},
		{"same change", "a b c", "a X c", "a X c", "a X c", 0},
		{"conflicting change with base", "a b c", "a B1 c", "a B2 c",
			"a <<<<<<<_ours B1 |||||||_base b ======= B2 >>>>>>>_theirs c", 1},
		{"only ours", "a b c", "a c", "a b c", "a c", 0},
		{"only theirs", "a b c", "a b c", "b c d", "b c d", 0},
		{"delete versus edit", "a b c", "a c", "a B c",
			"a <<<<<<<_ours |||||||_base b ======= B >>>>>>>_theirs c", 1},
		{"empty base", "", "x", "y",
			"<<<<<<<_ours x |||||||_base ======= y >>>>>>>_theirs", 1},
		{"two conflicts", "a b c d e", "A b c d E", "1 b c d 5",
			"<<<<<<<_ours A |||||||_base a ======= 1 >>>>>>>_theirs b c d " +
				"<<<<<<<_ours E |||||||_base e ======= 5 >>>>>>>_theirs", 2},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := mergeWords(testCase.base, testCase.ours, testCase.theirs)
			want := words(testCase.want)
			for index, word := range want {
				want[index] = strings.ReplaceAll(word, "_", " ")
			}
			if !slices.Equal(result.Lines, want) {
				t.Errorf("lines = %q, want %q", result.Lines, want)
			}
			if result.Conflicts != testCase.conflicts {
				t.Errorf("conflicts = %d, want %d",
					result.Conflicts, testCase.conflicts)
			}
		})
	}
}

func TestMerge3BareMarkersWithoutLabels(t *testing.T) {
	result := Merge3(words("a"), words("x"), words("y"), "", "", "")
	want := words("<<<<<<< x ||||||| a ======= y >>>>>>>")
	if !slices.Equal(result.Lines, want) {
		t.Errorf("lines = %q, want %q", result.Lines, want)
	}
}

func TestMerge3Identities(t *testing.T) {
	random := rand.New(rand.NewSource(5))
	for iteration := 0; iteration < 500; iteration++ {
		base := randomLines(random, random.Intn(12), 1+random.Intn(3))
		other := randomLines(random, random.Intn(12), 1+random.Intn(3))
		checks := []struct {
			name   string
			result MergeResult
		}{
			{"ours changed", Merge3(base, other, base, "o", "b", "t")},
			{"theirs changed", Merge3(base, base, other, "o", "b", "t")},
			{"both identical", Merge3(base, other, other, "o", "b", "t")},
		}
		for _, check := range checks {
			if check.result.Conflicts != 0 ||
				!slices.Equal(check.result.Lines, other) {
				t.Fatalf("%s: Merge3 with base %v and %v = %+v, want %v",
					check.name, base, other, check.result, other)
			}
		}
	}
}
