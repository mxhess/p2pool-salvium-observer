package randomx

import (
	"crypto/subtle"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/crypto"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
)

type Hasher interface {
	Hash(key []byte, input []byte) (types.Hash, error)
	OptionFlags(flags ...Flag) error
	OptionNumberOfCachedStates(n int) error
	Close()
}

func SeedHeights(height uint64) (seedHeight, nextHeight uint64) {
	return SeedHeight(height), SeedHeight(height + SeedHashEpochLag)
}

func SeedHeight(height uint64) uint64 {
	if height <= SeedHashEpochBlocks+SeedHashEpochLag {
		return 0
	}

	return (height - SeedHashEpochLag - 1) & (^uint64(SeedHashEpochBlocks - 1))
}

type Flag int

const (
	FlagLargePages Flag = 1 << iota
	FlagFullMemory
	FlagSecure
)

const (
	SeedHashEpochLag    = 64
	SeedHashEpochBlocks = 2048
)

func consensusHash(scratchpad []byte) types.Hash {
	// Intentionally not a power of 2
	const ScratchpadSize = 1009

	const RandomxArgonMemory = 262144
	n := RandomxArgonMemory * 1024

	const Vec128Size = 128 / 8

	cachePtr := scratchpad[ScratchpadSize*Vec128Size:]
	scratchpadTopPtr := scratchpad[:ScratchpadSize*Vec128Size]
	for i := ScratchpadSize * Vec128Size; i < n; i += ScratchpadSize * Vec128Size {
		stride := ScratchpadSize * Vec128Size
		if stride > len(cachePtr) {
			stride = len(cachePtr)
		}
		subtle.XORBytes(scratchpadTopPtr, scratchpadTopPtr, cachePtr[:stride])
		cachePtr = cachePtr[stride:]
	}

	return crypto.Keccak256(scratchpadTopPtr)
}
