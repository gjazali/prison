package textdiff

import "fmt"

// Hunk offsets are zero-based.
type Hunk struct {
	AStart, ACount int
	BStart, BCount int
	Lines          []Edit
}

// Hunks puts two changes in one hunk when at most `2*context` equal lines
// separate them.
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

// hunkRange follows git. An empty range starts at the line before it, so
// its start stays zero-based.
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
