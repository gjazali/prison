package textdiff

import "slices"

// MergeResult holds merged lines and the number of conflicts found.
type MergeResult struct {
	Lines     []string
	Conflicts int
}

// syncRegion is a range of unchanged lines shared by base, ours, and
// theirs. Stored as half-open indices into each slice.
type syncRegion struct {
	baseStart, baseEnd   int
	ourStart, ourEnd     int
	theirStart, theirEnd int
}

// Merge3 performs a three-way merge of ours and theirs against base.
// Takes the three line slices and labels for conflict markers.
// Unchanged regions pass through. A change on one side wins. Equal
// changes on both sides take ours. Different changes on both sides
// produce a diff3-style conflict block. Returns the merged lines and
// a conflict count. Zero conflicts means a clean merge.
func Merge3(base, ours, theirs []string,
	ourLabel, baseLabel, theirLabel string) MergeResult {
	var result MergeResult
	baseIndex, ourIndex, theirIndex := 0, 0, 0
	for _, region := range syncRegions(base, ours, theirs) {
		regionBase := base[baseIndex:region.baseStart]
		regionOurs := ours[ourIndex:region.ourStart]
		regionTheirs := theirs[theirIndex:region.theirStart]
		switch {
		case slices.Equal(regionOurs, regionTheirs):
			result.Lines = append(result.Lines, regionOurs...)
		case slices.Equal(regionOurs, regionBase):
			result.Lines = append(result.Lines, regionTheirs...)
		case slices.Equal(regionTheirs, regionBase):
			result.Lines = append(result.Lines, regionOurs...)
		default:
			result.Conflicts++
			result.Lines = append(result.Lines, marker("<<<<<<<", ourLabel))
			result.Lines = append(result.Lines, regionOurs...)
			result.Lines = append(result.Lines, marker("|||||||", baseLabel))
			result.Lines = append(result.Lines, regionBase...)
			result.Lines = append(result.Lines, "=======")
			result.Lines = append(result.Lines, regionTheirs...)
			result.Lines = append(result.Lines, marker(">>>>>>>", theirLabel))
		}
		result.Lines = append(result.Lines,
			base[region.baseStart:region.baseEnd]...)
		baseIndex = region.baseEnd
		ourIndex = region.ourEnd
		theirIndex = region.theirEnd
	}
	return result
}

// syncRegions finds runs of lines common to all three slices.
// Returns them in order, with a sentinel empty region at the end so
// the last unstable stretch has a bound.
func syncRegions(base, ours, theirs []string) []syncRegion {
	ourBlocks := commonBlocks(base, ours)
	theirBlocks := commonBlocks(base, theirs)
	var regions []syncRegion
	ourIndex, theirIndex := 0, 0
	for ourIndex < len(ourBlocks) && theirIndex < len(theirBlocks) {
		ourBlock := ourBlocks[ourIndex]
		theirBlock := theirBlocks[theirIndex]
		ourEnd := ourBlock.aStart + ourBlock.length
		theirEnd := theirBlock.aStart + theirBlock.length
		overlapStart := max(ourBlock.aStart, theirBlock.aStart)
		overlapEnd := min(ourEnd, theirEnd)
		if overlapStart < overlapEnd {
			regions = append(regions, syncRegion{
				baseStart:  overlapStart,
				baseEnd:    overlapEnd,
				ourStart:   overlapStart - ourBlock.aStart + ourBlock.bStart,
				ourEnd:     overlapEnd - ourBlock.aStart + ourBlock.bStart,
				theirStart: overlapStart - theirBlock.aStart + theirBlock.bStart,
				theirEnd:   overlapEnd - theirBlock.aStart + theirBlock.bStart,
			})
		}
		if ourEnd < theirEnd {
			ourIndex++
		} else {
			theirIndex++
		}
	}
	return append(regions, syncRegion{
		baseStart: len(base), baseEnd: len(base),
		ourStart: len(ours), ourEnd: len(ours),
		theirStart: len(theirs), theirEnd: len(theirs),
	})
}

// marker returns a conflict marker line. Appends the label after a
// space if non-empty.
func marker(prefix, label string) string {
	if label == "" {
		return prefix
	}
	return prefix + " " + label
}
