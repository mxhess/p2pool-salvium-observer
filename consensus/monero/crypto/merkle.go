package crypto

import (
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
	"git.gammaspectra.live/P2Pool/sha3"
)

type BinaryTreeHash []types.Hash

func leafHash(data []types.Hash, hasher *sha3.HasherState) (rootHash types.Hash) {
	switch len(data) {
	case 0:
		panic("unsupported length")
	case 1:
		return data[0]
	default:
		//only hash the next two items
		hasher.Reset()
		_, _ = hasher.Write(data[0][:])
		_, _ = hasher.Write(data[1][:])
		HashFastSum(hasher, rootHash[:])
		return rootHash
	}
}

// RootHash Calculates the Merkle root hash of the tree
func (t BinaryTreeHash) RootHash() (rootHash types.Hash) {
	hasher := GetKeccak256Hasher()
	defer PutKeccak256Hasher(hasher)

	count := len(t)
	if count <= 2 {
		return leafHash(t, hasher)
	}

	pow2cnt := utils.PreviousPowerOfTwo(uint64(count))
	offset := pow2cnt*2 - count

	temporaryTree := make(BinaryTreeHash, pow2cnt)
	copy(temporaryTree, t[:offset])

	//TODO: maybe can be done zero-alloc
	//temporaryTree := t[:max(pow2cnt, offset)]

	offsetTree := temporaryTree[offset:]
	for i := range offsetTree {
		offsetTree[i] = leafHash(t[offset+i*2:], hasher)
	}

	for pow2cnt >>= 1; pow2cnt > 1; pow2cnt >>= 1 {
		for i := range temporaryTree[:pow2cnt] {
			temporaryTree[i] = leafHash(temporaryTree[i*2:], hasher)
		}
	}

	rootHash = leafHash(temporaryTree, hasher)

	return
}

func (t BinaryTreeHash) MainBranch() (mainBranch []types.Hash) {
	count := len(t)
	if count <= 2 {
		return nil
	}

	hasher := GetKeccak256Hasher()
	defer PutKeccak256Hasher(hasher)

	pow2cnt := utils.PreviousPowerOfTwo(uint64(count))
	offset := pow2cnt*2 - count

	temporaryTree := make(BinaryTreeHash, pow2cnt)
	copy(temporaryTree, t[:offset])

	offsetTree := temporaryTree[offset:]

	for i := range offsetTree {
		if (offset + i*2) == 0 {
			mainBranch = append(mainBranch, t[1])
		}
		offsetTree[i] = leafHash(t[offset+i*2:], hasher)
	}

	for pow2cnt >>= 1; pow2cnt > 1; pow2cnt >>= 1 {
		for i := range temporaryTree[:pow2cnt] {
			if i == 0 {
				mainBranch = append(mainBranch, temporaryTree[1])
			}

			temporaryTree[i] = leafHash(temporaryTree[i*2:], hasher)
		}
	}

	mainBranch = append(mainBranch, temporaryTree[1])

	return
}

type MerkleProof []types.Hash

func (proof MerkleProof) Verify(h types.Hash, index, count int, rootHash types.Hash) bool {
	return proof.GetRoot(h, index, count) == rootHash
}

func pairHash(index int, h, p types.Hash, hasher *sha3.HasherState) (out types.Hash) {
	hasher.Reset()

	if index&1 > 0 {
		_, _ = hasher.Write(p[:])
		_, _ = hasher.Write(h[:])
	} else {
		_, _ = hasher.Write(h[:])
		_, _ = hasher.Write(p[:])
	}

	HashFastSum(hasher, out[:])
	return out
}

func (proof MerkleProof) GetRoot(h types.Hash, index, count int) types.Hash {
	if count == 1 {
		return h
	}

	if index >= count {
		return types.ZeroHash
	}

	hasher := GetKeccak256Hasher()
	defer PutKeccak256Hasher(hasher)

	if count == 2 {
		if len(proof) == 0 {
			return types.ZeroHash
		}

		h = pairHash(index, h, proof[0], hasher)
	} else {
		pow2cnt := utils.PreviousPowerOfTwo(uint64(count))
		k := pow2cnt*2 - count

		var proofIndex int

		if index >= k {
			index -= k

			if len(proof) == 0 {
				return types.ZeroHash
			}

			h = pairHash(index, h, proof[0], hasher)

			index = (index >> 1) + k
			proofIndex = 1

		}

		for ; pow2cnt >= 2; proofIndex, index, pow2cnt = proofIndex+1, index>>1, pow2cnt>>1 {
			if proofIndex >= len(proof) {
				return types.ZeroHash
			}

			h = pairHash(index, h, proof[proofIndex], hasher)
		}
	}

	return h
}
