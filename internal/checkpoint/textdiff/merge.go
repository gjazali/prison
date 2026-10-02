package textdiff

import "slices"

type MergeResult struct {
	Lines     []string
	Conflicts int
}

type syncRegion struct {
	baseStart, baseEnd   int
	ourStart, ourEnd     int
	theirStart, theirEnd int
}

// Merge3 writes a diff3-style conflict block when both sides change the
// same region in different ways.
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

// syncRegions ends with an empty sentinel region, so the last changed
// stretch has an end.
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

func marker(prefix, label string) string {
	if label == "" {
		return prefix
	}
	return prefix + " " + label
}
