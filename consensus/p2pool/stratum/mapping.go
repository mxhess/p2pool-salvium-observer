package stratum

import (
	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/sidechain"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"slices"
)

const ShuffleMappingZeroKeyIndex = 0

type ShuffleMapping struct {
	// Including the index mapping contains a new miner in the list
	Including []int
	// Excluding the index mapping doesn't contain a new miner.
	// len(Excluding) = len(Including) - 1 (unless len(Including) == 1, where it's also 1)
	Excluding []int
}

// BuildShuffleMapping Creates a mapping of source to destination miner output post shuffle
// This uses two mappings, one where a new miner is added to the list, and one where the count stays the same
// Usual usage will place Zero key in index 0
func BuildShuffleMapping(n int, shareVersion sidechain.ShareVersion, transactionPrivateKeySeed types.Hash, oldMappings ShuffleMapping) (mappings ShuffleMapping) {
	if n <= 1 {
		return ShuffleMapping{
			Including: []int{0},
			Excluding: []int{0},
		}
	}
	shuffleSequence1 := make([]int, n)
	for i := range shuffleSequence1 {
		shuffleSequence1[i] = i
	}
	shuffleSequence2 := make([]int, n-1)
	for i := range shuffleSequence2 {
		shuffleSequence2[i] = i
	}

	sidechain.ShuffleSequence(shareVersion, transactionPrivateKeySeed, n, func(i, j int) {
		shuffleSequence1[i], shuffleSequence1[j] = shuffleSequence1[j], shuffleSequence1[i]
	})
	sidechain.ShuffleSequence(shareVersion, transactionPrivateKeySeed, n-1, func(i, j int) {
		shuffleSequence2[i], shuffleSequence2[j] = shuffleSequence2[j], shuffleSequence2[i]
	})

	mappings.Including = slices.Grow(oldMappings.Including, n)[:n]
	mappings.Excluding = slices.Grow(oldMappings.Excluding, n-1)[:n-1]

	for i := range shuffleSequence1 {
		mappings.Including[shuffleSequence1[i]] = i
	}
	//Flip
	for i := range shuffleSequence2 {
		mappings.Excluding[shuffleSequence2[i]] = i
	}

	return mappings
}

// ApplyShuffleMapping Applies a shuffle mapping depending on source length
// Returns nil in case no source length matches shuffle mapping
func ApplyShuffleMapping[T any](v []T, mappings ShuffleMapping) []T {
	n := len(v)

	result := make([]T, n)

	if n == len(mappings.Including) {
		for i := range v {
			result[mappings.Including[i]] = v[i]
		}
	} else if n == len(mappings.Excluding) {
		for i := range v {
			result[mappings.Excluding[i]] = v[i]
		}
	} else {
		return nil
	}
	return result
}

type ShuffleMappingIndices [][3]int

func (m ShuffleMapping) RangePossibleIndices(f func(i, ix0, ix1, ix2 int)) {
	n := len(m.Including)
	var ix0, ix1, ix2 int
	for i := 0; i < n; i++ {
		// Count with all + miner
		ix0 = m.Including[i]
		if i > ShuffleMappingZeroKeyIndex {
			// Count with all + miner shifted to a slot before
			ix1 = m.Including[i-1]

			// Count with all miners minus one
			ix2 = m.Including[i-1]
		} else {
			ix1 = -1
			ix2 = -1
		}
		f(i, ix0, ix1, ix2)
	}
}
