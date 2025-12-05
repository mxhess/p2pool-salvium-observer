package stratum

import (
	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/sidechain"
	"slices"
	"testing"
)

func TestShuffleMapping(t *testing.T) {
	const n = 16
	// Shuffle only exists on ShareVersion_V2 and above
	// TODO: whenever different consensus shuffle is added, add test for it
	const shareVersion = sidechain.ShareVersion_V2
	var seed = zeroExtraBaseRCTHash
	mappings := BuildShuffleMapping(n, shareVersion, seed, ShuffleMapping{})

	seq := make([]int, n)
	for i := range seq {
		seq[i] = i
	}

	seq1 := slices.Clone(seq)

	//test that regular shuffle will correspond to a mapping applied shuffle
	sidechain.ShuffleShares(seq1, shareVersion, seed)
	seq2 := ApplyShuffleMapping(seq, mappings)

	if slices.Compare(seq1, seq2) != 0 {
		for i := range seq1 {
			if seq1[i] != seq2[i] {
				t.Logf("%d %d *** @ %d", seq1[i], seq2[i], i)
			} else {
				t.Logf("%d %d @ %d", seq1[i], seq2[i], i)
			}
		}

		t.Fatal()
	}

}
