package merge_mining

import (
	"crypto/sha256"
	"encoding/binary"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
	"io"
)

type Tag struct {
	NumberAuxiliaryChains uint32
	Nonce                 uint32
	RootHash              types.Hash
}

// FromReader Decodes the merge mining tag located in coinbase transaction
// Format according to https://github.com/SChernykh/p2pool/blob/e6b8292d5b59692921af23613456674ccab4958b/docs/MERGE_MINING.MD
func (t *Tag) FromReader(reader utils.ReaderAndByteReader) error {
	merkleTreeData, err := utils.ReadCanonicalUvarint(reader)
	if err != nil {
		return err
	}

	k := uint32(merkleTreeData)
	n := 1 + (k & 7)

	t.NumberAuxiliaryChains = 1 + ((k >> 3) & ((1 << n) - 1))
	t.Nonce = uint32(merkleTreeData >> (3 + n))

	if _, err = io.ReadFull(reader, t.RootHash[:]); err != nil {
		return err
	}
	return nil
}

func (t *Tag) MarshalTreeData() uint64 {
	nBits := uint32(1)

	for (1<<nBits) >= t.NumberAuxiliaryChains && nBits < 8 {
		nBits++
	}
	merkleTreeData := (uint64(nBits) - 1) | (uint64(t.NumberAuxiliaryChains-1) << 3) | (uint64(t.Nonce) << (3 + nBits))

	return merkleTreeData
}

func (t *Tag) MarshalBinary() (buf []byte, err error) {
	merkleTreeData := t.MarshalTreeData()

	buf = make([]byte, utils.UVarInt64Size(merkleTreeData)+types.HashSize)
	n := binary.PutUvarint(buf, merkleTreeData)
	copy(buf[n:], t.RootHash[:])

	return buf, nil
}

// GetAuxiliarySlot Gets the slot for an auxiliary chain
func GetAuxiliarySlot(id types.Hash, nonce, numberAuxiliaryChains uint32) (auxiliarySlot uint32) {
	if numberAuxiliaryChains <= 1 {
		return 0
	}

	// HashKeyMergeMineSlot HASH_KEY_MM_SLOT
	const HashKeyMergeMineSlot = 'm'

	var buf [types.HashSize + 4 + 1]byte
	copy(buf[:], id[:])
	binary.LittleEndian.PutUint32(buf[types.HashSize:], nonce)
	buf[types.HashSize+4] = HashKeyMergeMineSlot

	//todo: optimize sha256
	result := sha256.Sum256(buf[:])

	return binary.LittleEndian.Uint32(result[:]) % numberAuxiliaryChains
}

func FindAuxiliaryNonce(auxId []types.Hash, maxNonce uint32) (nonce uint32, ok bool) {
	numberAuxiliaryChains := uint32(len(auxId))

	if numberAuxiliaryChains <= 1 {
		return 0, true
	}

	slots := make([]bool, numberAuxiliaryChains)
	for i := uint32(0); ; i++ {
		clear(slots)

		var j uint32
		for ; j < numberAuxiliaryChains; j++ {
			k := GetAuxiliarySlot(auxId[j], i, numberAuxiliaryChains)
			if slots[k] {
				break
			}
			slots[k] = true
		}

		if j >= numberAuxiliaryChains {
			return i, true
		}

		if i == maxNonce {
			return 0, false
		}
	}
}
