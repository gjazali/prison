package textdiff

import "fmt"

// Hunk is one group of nearby changes with surrounding context.
// AStart and BStart are zero-based line offsets. ACount and BCount
// are line counts. Lines holds the edits in script order.
type Hunk struct {
	AStart, ACount int
	BStart, BCount int
	Lines          []Edit
}

// Hunks groups changes in edits into unified-diff hunks. Each hunk
// has up to context lines of padding on each side. Two changes
// share a hunk when at most 2*context equal lines separate them.
// Returns nil when there are no changes.
func Hunks(edits []Edit, context int) []Hunk {
	context = max(context, 0)
	var hunks []Hunk
	aPosition, bPosition := 0, 0
	index := 0
	for index < len(edits) {
		if edits[index].Op == OpEqual {
			aPosition++
			bPosition++
			index++
			continue
		}
		start := max(index-context, 0)
		end := index
		for end < len(edits) {
			if edits[end].Op != OpEqual {
				end++
				continue
			}
			gap := 0
			for end+gap < len(edits) && edits[end+gap].Op == OpEqual {
				gap++
			}
			if end+gap == len(edits) || gap > 2*context {
				end = min(end+context, len(edits))
				break
			}
			end += gap
		}
		hunk := Hunk{
			AStart: aPosition - (index - start),
			BStart: bPosition - (index - start),
			Lines:  edits[start:end],
		}
		for _, edit := range hunk.Lines {
			if edit.Op != OpInsert {
				hunk.ACount++
			}
			if edit.Op != OpDelete {
				hunk.BCount++
			}
		}
		hunks = append(hunks, hunk)
		aPosition = hunk.AStart + hunk.ACount
		bPosition = hunk.BStart + hunk.BCount
		index = end
	}
	return hunks
}

// Unified renders the diff between a and b as unified diff lines
// without terminators. Takes the two line slices, file names, and
// context size. Returns nil when a and b are equal.
func Unified(a, b []string, aName, bName string, context int) []string {
	hunks := Hunks(Diff(a, b), context)
	if len(hunks) == 0 {
		return nil
	}
	lines := []string{"--- " + aName, "+++ " + bName}
	for _, hunk := range hunks {
		lines = append(lines, fmt.Sprintf("@@ -%s +%s @@",
			hunkRange(hunk.AStart, hunk.ACount),
			hunkRange(hunk.BStart, hunk.BCount)))
		for _, edit := range hunk.Lines {
			lines = append(lines, editPrefix(edit.Op)+edit.Line)
		}
	}
	return lines
}

// hunkRange formats one side of an @@ header. Takes a zero-based
// start and a line count. Returns a string in unified diff format.
func hunkRange(start, count int) string {
	switch count {
	case 0:
		return fmt.Sprintf("%d,0", start)
	case 1:
		return fmt.Sprintf("%d", start+1)
	default:
		return fmt.Sprintf("%d,%d", start+1, count)
	}
}

// editPrefix returns the unified diff prefix for an operation:
// "-" for delete, "+" for insert, " " for equal.
func editPrefix(operation Op) string {
	switch operation {
	case OpDelete:
		return "-"
	case OpInsert:
		return "+"
	default:
		return " "
	}
}
