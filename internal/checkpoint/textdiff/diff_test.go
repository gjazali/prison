package textdiff

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"
	"time"
)

// script renders edits as "=x", "-x", or "+x" strings.
func script(edits []Edit) []string {
	out := make([]string, len(edits))
	for i, e := range edits {
		prefix := "="
		if e.Op == OpDelete {
			prefix = "-"
		} else if e.Op == OpInsert {
			prefix = "+"
		}
		out[i] = prefix + e.Line
	}
	return out
}

// words splits text on whitespace. Returns nil for an empty string.
func words(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Fields(text)
}

func TestDiffHandCheckedCases(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want []string
	}{
		{"both empty", "", "", nil},
		{"insert into empty", "", "x y", []string{"+x", "+y"}},
		{"delete to empty", "x y", "", []string{"-x", "-y"}},
		{"identical", "a b c", "a b c", []string{"=a", "=b", "=c"}},
		{"all changed", "a b", "x y", []string{"-a", "-b", "+x", "+y"}},
		{"interleaved", "a b c d", "a x c y",
			[]string{"=a", "-b", "+x", "=c", "-d", "+y"}},
		{"insert middle", "a b", "a x b", []string{"=a", "+x", "=b"}},
		{"delete middle", "a x b", "a b", []string{"=a", "-x", "=b"}},
		{"append", "a", "a a", []string{"=a", "+a"}},
		{"reorder", "a b c", "c a b", []string{"+c", "=a", "=b", "-c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := script(Diff(words(tc.a), words(tc.b)))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("Diff = %v, want %v", got, tc.want)
			}
		})
	}
}

// longestCommonSubsequence computes the LCS length using quadratic
// DP. Used as a reference for property tests.
func longestCommonSubsequence(a, b []string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for _, la := range a {
		for i, lb := range b {
			if la == lb {
				cur[i+1] = prev[i] + 1
			} else {
				cur[i+1] = max(prev[i+1], cur[i])
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// checkScript checks that edits transform a into b. Returns the
// count of equal operations.
func checkScript(t *testing.T, a, b []string, edits []Edit) int {
	t.Helper()
	var gotA, gotB []string
	eq := 0
	for _, e := range edits {
		if e.Op != OpInsert {
			gotA = append(gotA, e.Line)
		}
		if e.Op != OpDelete {
			gotB = append(gotB, e.Line)
		}
		if e.Op == OpEqual {
			eq++
		}
	}
	if !slices.Equal(gotA, a) || !slices.Equal(gotB, b) {
		t.Fatalf("script %v does not rebuild %v and %v",
			script(edits), a, b)
	}
	return eq
}

// randomLines returns n random lines from an alphabet of the given
// size.
func randomLines(rng *rand.Rand, n, alphabet int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = string(rune('a' + rng.Intn(alphabet)))
	}
	return lines
}

func TestDiffIsShortestAgainstReference(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for range 3000 {
		a := randomLines(rng, rng.Intn(13), 1+rng.Intn(4))
		b := randomLines(rng, rng.Intn(13), 1+rng.Intn(4))
		edits := Diff(a, b)
		eq := checkScript(t, a, b, edits)
		if want := longestCommonSubsequence(a, b); eq != want {
			t.Fatalf("Diff(%v, %v) kept %d lines, want %d: %v",
				a, b, eq, want, script(edits))
		}
	}
}

func TestDiffDeletionsPrecedeInsertions(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	for range 500 {
		a := randomLines(rng, rng.Intn(10), 3)
		b := randomLines(rng, rng.Intn(10), 3)
		prev := OpEqual
		for _, e := range Diff(a, b) {
			if prev == OpInsert && e.Op == OpDelete {
				t.Fatalf("Diff(%v, %v) puts a deletion after an insertion",
					a, b)
			}
			prev = e.Op
		}
	}
}

func TestDiffCappedSearchStaysValid(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	a := make([]string, 5000)
	for i := range a {
		a[i] = fmt.Sprintf("line %d", i)
	}
	b := slices.Clone(a)
	rng.Shuffle(len(b), func(i, j int) { b[i], b[j] = b[j], b[i] })
	start := time.Now()
	edits := Diff(a, b)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("shuffled diff took %v", elapsed)
	}
	checkScript(t, a, b, edits)
}

func TestDiffLargeInputPerformance(t *testing.T) {
	a := make([]string, 50000)
	for i := range a {
		a[i] = fmt.Sprintf("line %d", i)
	}
	b := slices.Clone(a)
	b[100] = "changed 100"
	b[20000] = "changed 20000"
	b = slices.Delete(b, 30000, 30003)
	b = slices.Insert(b, 40000, "added one", "added two")
	b[len(b)-1] = "changed last"
	start := time.Now()
	edits := Diff(a, b)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("large diff took %v", elapsed)
	}
	eq := checkScript(t, a, b, edits)
	changes := len(edits) - eq
	if want := 2 + 2 + 3 + 2 + 2; changes != want {
		t.Fatalf("large diff has %d changes, want %d", changes, want)
	}
}
