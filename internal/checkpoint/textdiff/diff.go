// Package textdiff computes line-oriented diffs and three-way
// merges. All functions work with lines that have no terminators.
// Split and Join convert between text and lines.
package textdiff

import "math"

// Op is the kind of edit in an edit script.
type Op int

const (
	// OpEqual marks a line present in both inputs.
	OpEqual Op = iota
	// OpDelete marks a line present only in the first input.
	OpDelete
	// OpInsert marks a line present only in the second input.
	OpInsert
)

// Edit is one step in an edit script. Holds an operation and a line.
type Edit struct {
	Op   Op
	Line string
}

// block is a shared run of lines between two inputs. Stores the
// start position in each input and the run length.
type block struct {
	aStart, bStart, length int
}

// Diff computes an edit script that turns a into b. Takes two line
// slices. Returns a slice of Edit values in input order. Shared
// lines are OpEqual, lines only in a are OpDelete, lines only in
// b are OpInsert. The script is shortest for most inputs. Very
// large diffs may produce a valid but non-minimal script.
func Diff(a, b []string) []Edit {
	blocks := commonBlocks(a, b)
	shared := 0
	for _, run := range blocks {
		shared += run.length
	}
	edits := make([]Edit, 0, len(a)+len(b)-shared)
	aIndex, bIndex := 0, 0
	for _, run := range blocks {
		for ; aIndex < run.aStart; aIndex++ {
			edits = append(edits, Edit{OpDelete, a[aIndex]})
		}
		for ; bIndex < run.bStart; bIndex++ {
			edits = append(edits, Edit{OpInsert, b[bIndex]})
		}
		for ; aIndex < run.aStart+run.length; aIndex++ {
			edits = append(edits, Edit{OpEqual, a[aIndex]})
		}
		bIndex = run.bStart + run.length
	}
	for ; aIndex < len(a); aIndex++ {
		edits = append(edits, Edit{OpDelete, a[aIndex]})
	}
	for ; bIndex < len(b); bIndex++ {
		edits = append(edits, Edit{OpInsert, b[bIndex]})
	}
	return edits
}

// commonBlocks returns the shared runs between a and b in order.
// Matches the common prefix and suffix first, then aligns the
// rest with a Myers search.
func commonBlocks(a, b []string) []block {
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix &&
		a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	var blocks []block
	if prefix > 0 {
		blocks = append(blocks, block{0, 0, prefix})
	}
	blocks = appendMiddleBlocks(blocks,
		a[prefix:len(a)-suffix], b[prefix:len(b)-suffix], prefix)
	if suffix > 0 {
		blocks = appendBlock(blocks, len(a)-suffix, len(b)-suffix, suffix)
	}
	return blocks
}

// appendBlock appends a shared run to blocks. Extends the last
// block if the run continues it in both inputs. Returns the
// updated slice.
func appendBlock(blocks []block, aStart, bStart, length int) []block {
	if last := len(blocks) - 1; last >= 0 {
		previous := &blocks[last]
		if previous.aStart+previous.length == aStart &&
			previous.bStart+previous.length == bStart {
			previous.length += length
			return blocks
		}
	}
	return append(blocks, block{aStart, bStart, length})
}

// appendMiddleBlocks aligns a and b after their shared prefix and
// suffix have been removed. Appends the shared runs to blocks
// with offset added to each position. Filters out lines that
// appear on only one side before running the Myers search.
func appendMiddleBlocks(blocks []block, a, b []string, offset int) []block {
	if len(a) == 0 || len(b) == 0 {
		return blocks
	}
	identifierOf := make(map[string]int, len(a))
	aIdentifiers := make([]int, len(a))
	for index, line := range a {
		identifier, known := identifierOf[line]
		if !known {
			identifier = len(identifierOf)
			identifierOf[line] = identifier
		}
		aIdentifiers[index] = identifier
	}
	sharedIdentifier := make([]bool, len(identifierOf))
	var keptB, keptBIndex []int
	for index, line := range b {
		identifier, known := identifierOf[line]
		if !known {
			continue
		}
		sharedIdentifier[identifier] = true
		keptB = append(keptB, identifier)
		keptBIndex = append(keptBIndex, index)
	}
	var keptA, keptAIndex []int
	for index, identifier := range aIdentifiers {
		if sharedIdentifier[identifier] {
			keptA = append(keptA, identifier)
			keptAIndex = append(keptAIndex, index)
		}
	}
	if len(keptA) == 0 || len(keptB) == 0 {
		return blocks
	}
	for _, run := range alignIdentifiers(keptA, keptB) {
		for position := 0; position < run.length; position++ {
			blocks = appendBlock(blocks,
				keptAIndex[run.aStart+position]+offset,
				keptBIndex[run.bStart+position]+offset, 1)
		}
	}
	return blocks
}

// aligner holds the state of a divide-and-conquer Myers search
// over two integer sequences.
type aligner struct {
	a, b    []int
	forward []int
	reverse []int
	runs    []block
}

// alignIdentifiers returns the shared runs of a and b in order.
// Uses the linear-space Myers algorithm on integer identifiers.
func alignIdentifiers(a, b []int) []block {
	size := 2*((len(a)+len(b)+1)/2) + 3
	state := &aligner{
		a: a, b: b,
		forward: make([]int, size),
		reverse: make([]int, size),
	}
	state.align(0, len(a), 0, len(b))
	return state.runs
}

// align records the shared runs of a[aLow:aHigh] and
// b[bLow:bHigh]. Matches the common prefix and suffix, then
// splits the rest at a point on the shortest edit path.
func (state *aligner) align(aLow, aHigh, bLow, bHigh int) {
	prefix := 0
	for aLow+prefix < aHigh && bLow+prefix < bHigh &&
		state.a[aLow+prefix] == state.b[bLow+prefix] {
		prefix++
	}
	if prefix > 0 {
		state.runs = appendBlock(state.runs, aLow, bLow, prefix)
		aLow += prefix
		bLow += prefix
	}
	suffix := 0
	for aHigh-suffix > aLow && bHigh-suffix > bLow &&
		state.a[aHigh-1-suffix] == state.b[bHigh-1-suffix] {
		suffix++
	}
	aHigh -= suffix
	bHigh -= suffix
	if aLow < aHigh && bLow < bHigh {
		aSplit, bSplit := state.split(aLow, aHigh, bLow, bHigh)
		state.align(aLow, aSplit, bLow, bSplit)
		state.align(aSplit, aHigh, bSplit, bHigh)
	}
	if suffix > 0 {
		state.runs = appendBlock(state.runs, aHigh, bHigh, suffix)
	}
}

// split finds a point on the shortest edit path between
// a[aLow:aHigh] and b[bLow:bHigh]. Returns indices in a and b
// that divide the problem for recursive alignment. Both ranges
// must be non-empty. Falls back to the furthest forward point
// past a step limit.
func (state *aligner) split(aLow, aHigh, bLow, bHigh int) (int, int) {
	a := state.a[aLow:aHigh]
	b := state.b[bLow:bHigh]
	n, m := len(a), len(b)
	delta := n - m
	deltaOdd := delta&1 != 0
	maxSteps := (n + m + 1) / 2
	offset := maxSteps + 1
	forward := state.forward[:2*maxSteps+3]
	reverse := state.reverse[:2*maxSteps+3]
	for index := range forward {
		forward[index] = -1
		reverse[index] = -1
	}
	forward[offset] = 0
	reverse[offset] = 0
	stepLimit := max(1024, 4*int(math.Sqrt(float64(n+m))))
	bestX, bestY := 0, 0

	for step := 1; step <= maxSteps && step <= stepLimit; step++ {
		lowest, highest := diagonalBounds(step, n, m)
		for k := lowest; k <= highest; k += 2 {
			x := reachable(forward, offset, k, n, m)
			forward[offset+k] = x
			if x < 0 {
				continue
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			forward[offset+k] = x
			if x+y > bestX+bestY {
				bestX, bestY = x, y
			}
			mirror := offset + delta - k
			if deltaOdd && mirror >= 0 && mirror < len(reverse) &&
				reverse[mirror] >= 0 && x+reverse[mirror] >= n {
				return aLow + x, bLow + y
			}
		}
		for k := lowest; k <= highest; k += 2 {
			x := reachable(reverse, offset, k, n, m)
			reverse[offset+k] = x
			if x < 0 {
				continue
			}
			y := x - k
			for x < n && y < m && a[n-1-x] == b[m-1-y] {
				x++
				y++
			}
			reverse[offset+k] = x
			mirror := offset + delta - k
			if !deltaOdd && mirror >= 0 && mirror < len(forward) &&
				forward[mirror] >= 0 && forward[mirror]+x >= n {
				return aLow + forward[mirror],
					bLow + forward[mirror] - (delta - k)
			}
		}
	}
	return aLow + bestX, bLow + bestY
}

// diagonalBounds returns the lowest and highest diagonal reachable
// with step edits in an n by m edit graph.
func diagonalBounds(step, n, m int) (int, int) {
	lowest := max(-step, -m)
	highest := min(step, n)
	if (lowest+step)&1 != 0 {
		lowest++
	}
	if (highest+step)&1 != 0 {
		highest--
	}
	return lowest, highest
}

// reachable returns the x coordinate where a path of the current
// step arrives on diagonal k. Uses the furthest points from the
// previous step stored in paths. Returns -1 when no path reaches
// the diagonal inside the n by m graph.
func reachable(paths []int, offset, k, n, m int) int {
	x := -1
	if lower := paths[offset+k-1]; lower >= 0 && lower < n {
		x = lower + 1
	}
	if upper := paths[offset+k+1]; upper >= 0 && upper-(k+1) < m &&
		upper > x {
		x = upper
	}
	return x
}
