package subtitles

import "sort"

// Ratio is a faithful port of Python's difflib.SequenceMatcher.ratio() for two
// strings. The OpenSubtitles candidate thresholds in the proven `~/Projects/subs`
// pipeline (0.78 for titles, 0.90 for safety) were tuned against this metric, so
// CineRoute must use the same algorithm rather than a Levenshtein approximation.
//
// ratio == 2*M/T where M is the total size of the matching blocks and T is the
// combined length of both sequences.
func Ratio(a, b string) float64 {
	left := []rune(a)
	right := []rune(b)
	total := len(left) + len(right)
	if total == 0 {
		return 1
	}
	matches := matchingBlockTotal(left, right)
	return 2.0 * float64(matches) / float64(total)
}

// autojunkThreshold mirrors SequenceMatcher's default: when the second sequence
// is at least this long, elements that appear very often are ignored.
const autojunkThreshold = 200

func buildIndex(right []rune) map[rune][]int {
	index := make(map[rune][]int, len(right))
	for position, element := range right {
		index[element] = append(index[element], position)
	}
	if len(right) >= autojunkThreshold {
		limit := len(right)/100 + 1
		for element, positions := range index {
			if len(positions) > limit {
				delete(index, element)
			}
		}
	}
	return index
}

type matchBlock struct {
	a, b, size int
}

type matchRange struct {
	aLo, aHi, bLo, bHi int
}

// findLongestMatch ports SequenceMatcher.find_longest_match.
func findLongestMatch(left, right []rune, index map[rune][]int, aLo, aHi, bLo, bHi int) (int, int, int) {
	bestA, bestB, bestSize := aLo, bLo, 0
	j2len := map[int]int{}
	for i := aLo; i < aHi; i++ {
		newJ2len := map[int]int{}
		for _, j := range index[left[i]] {
			if j < bLo {
				continue
			}
			if j >= bHi {
				break
			}
			size := j2len[j-1] + 1
			newJ2len[j] = size
			if size > bestSize {
				bestA = i - size + 1
				bestB = j - size + 1
				bestSize = size
			}
		}
		j2len = newJ2len
	}
	return bestA, bestB, bestSize
}

// matchingBlocks ports SequenceMatcher.get_matching_blocks (with the final
// sentinel block omitted, since we only need the total matched size).
func matchingBlocks(left, right []rune) []matchBlock {
	index := buildIndex(right)
	var blocks []matchBlock
	queue := []matchRange{{0, len(left), 0, len(right)}}
	for len(queue) > 0 {
		current := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		i, j, size := findLongestMatch(left, right, index, current.aLo, current.aHi, current.bLo, current.bHi)
		if size == 0 {
			continue
		}
		blocks = append(blocks, matchBlock{i, j, size})
		if current.aLo < i && current.bLo < j {
			queue = append(queue, matchRange{current.aLo, i, current.bLo, j})
		}
		if i+size < current.aHi && j+size < current.bHi {
			queue = append(queue, matchRange{i + size, current.aHi, j + size, current.bHi})
		}
	}
	sort.Slice(blocks, func(x, y int) bool {
		if blocks[x].a != blocks[y].a {
			return blocks[x].a < blocks[y].a
		}
		return blocks[x].b < blocks[y].b
	})
	// Merge adjacent blocks the same way difflib does before summing sizes.
	var merged []matchBlock
	prevA, prevB, prevSize := 0, 0, 0
	for _, block := range blocks {
		if prevA+prevSize == block.a && prevB+prevSize == block.b {
			prevSize += block.size
			continue
		}
		if prevSize > 0 {
			merged = append(merged, matchBlock{prevA, prevB, prevSize})
		}
		prevA, prevB, prevSize = block.a, block.b, block.size
	}
	if prevSize > 0 {
		merged = append(merged, matchBlock{prevA, prevB, prevSize})
	}
	return merged
}

func matchingBlockTotal(left, right []rune) int {
	total := 0
	for _, block := range matchingBlocks(left, right) {
		total += block.size
	}
	return total
}
