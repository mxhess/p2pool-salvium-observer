package sidechain

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"

	"git.gammaspectra.live/P2Pool/consensus/v4/merge_mining"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/address"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/crypto"
	p2pooltypes "git.gammaspectra.live/P2Pool/consensus/v4/p2pool/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
)

const MaxUncleCount = uint64(math.MaxUint64) / types.HashSize

type SideData struct {
	PublicKey              address.PackedAddress `json:"public_key"`
	CoinbasePrivateKeySeed types.Hash            `json:"coinbase_private_key_seed,omitempty"`
	// CoinbasePrivateKey filled or calculated on decoding
	CoinbasePrivateKey crypto.PrivateKeyBytes `json:"coinbase_private_key"`
	// Parent Template Id of the parent of this share, or zero if genesis
	Parent types.Hash `json:"parent"`
	// Uncles List of Template Ids of the uncles this share contains
	Uncles               []types.Hash     `json:"uncles,omitempty"`
	Height               uint64           `json:"height"`
	Difficulty           types.Difficulty `json:"difficulty"`
	CumulativeDifficulty types.Difficulty `json:"cumulative_difficulty"`

	// MerkleProof Merkle proof for merge mining, available in ShareVersion ShareVersion_V3 and above
	MerkleProof crypto.MerkleProof `json:"merkle_proof,omitempty"`

	// MergeMiningExtra vector of (chain ID, chain data) pairs
	// Chain data format is arbitrary and depends on the merge mined chain's requirements
	MergeMiningExtra MergeMiningExtra `json:"merge_mining_extra,omitempty"`

	// ExtraBuffer Arbitrary extra data, available in ShareVersion ShareVersion_V2 and above
	ExtraBuffer SideDataExtraBuffer `json:"extra_buffer,omitempty"`
}

type MergeMiningExtra []MergeMiningExtraData

var ExtraChainKeySubaddressViewPub = crypto.Keccak256Single([]byte("subaddress_viewpub"))

func (d MergeMiningExtra) Get(chainId types.Hash) ([]byte, bool) {
	for _, e := range d {
		if e.ChainId == chainId {
			return e.Data, true
		}
	}
	return nil, false
}

func (d MergeMiningExtra) Set(chainId types.Hash, data []byte) MergeMiningExtra {
	for i, e := range d {
		if e.ChainId == chainId {
			d[i].Data = data
			return d
		}
	}

	newSlice := append(d, MergeMiningExtraData{
		ChainId: chainId,
		Data:    data,
	})
	slices.SortFunc(newSlice, func(a, b MergeMiningExtraData) int {
		return a.ChainId.Compare(b.ChainId)
	})
	return newSlice
}

func (d MergeMiningExtra) BufferLength() (size int) {
	for i := range d {
		size += d[i].BufferLength()
	}
	return size + utils.UVarInt64Size(len(d))
}

type MergeMiningExtraData struct {
	ChainId types.Hash  `json:"chain_id"`
	Data    types.Bytes `json:"data,omitempty"`
}

func (d MergeMiningExtraData) BufferLength() (size int) {
	return types.HashSize + utils.UVarInt64Size(len(d.Data)) + len(d.Data)
}

type SideDataExtraBuffer struct {
	SoftwareId          p2pooltypes.SoftwareId      `json:"software_id"`
	SoftwareVersion     p2pooltypes.SoftwareVersion `json:"software_version"`
	RandomNumber        uint32                      `json:"random_number"`
	SideChainExtraNonce uint32                      `json:"side_chain_extra_nonce"`
}

func (b *SideData) BufferLength(version ShareVersion) (size int) {
	size = crypto.PublicKeySize*2 +
		types.HashSize +
		crypto.PrivateKeySize +
		utils.UVarInt64Size(len(b.Uncles)) + len(b.Uncles)*types.HashSize +
		utils.UVarInt64Size(b.Height) +
		utils.UVarInt64Size(b.Difficulty.Lo) + utils.UVarInt64Size(b.Difficulty.Hi) +
		utils.UVarInt64Size(b.CumulativeDifficulty.Lo) + utils.UVarInt64Size(b.CumulativeDifficulty.Hi)

	if version >= ShareVersion_V2 {
		// ExtraBuffer
		size += 4 * 4
	}
	if version >= ShareVersion_V3 {
		// MerkleProof + MergeMiningExtra
		size += utils.UVarInt64Size(len(b.MerkleProof)) + len(b.MerkleProof)*types.HashSize + b.MergeMiningExtra.BufferLength()
	}

	return size
}

func (b *SideData) MarshalBinary(version ShareVersion) (buf []byte, err error) {
	return b.AppendBinary(make([]byte, 0, b.BufferLength(version)), version)
}

func (b *SideData) AppendBinary(preAllocatedBuf []byte, version ShareVersion) (buf []byte, err error) {
	buf = preAllocatedBuf
	buf = append(buf, b.PublicKey[address.PackedAddressSpend][:]...)
	buf = append(buf, b.PublicKey[address.PackedAddressView][:]...)
	if version >= ShareVersion_V2 {
		buf = append(buf, b.CoinbasePrivateKeySeed[:]...)
	} else {
		buf = append(buf, b.CoinbasePrivateKey[:]...)
	}
	buf = append(buf, b.Parent[:]...)
	buf = binary.AppendUvarint(buf, uint64(len(b.Uncles)))
	for _, uId := range b.Uncles {
		buf = append(buf, uId[:]...)
	}
	buf = binary.AppendUvarint(buf, b.Height)
	buf = binary.AppendUvarint(buf, b.Difficulty.Lo)
	buf = binary.AppendUvarint(buf, b.Difficulty.Hi)
	buf = binary.AppendUvarint(buf, b.CumulativeDifficulty.Lo)
	buf = binary.AppendUvarint(buf, b.CumulativeDifficulty.Hi)

	if version >= ShareVersion_V3 {
		// merkle proof
		if len(b.MerkleProof) > merge_mining.MaxChainsLog2 {
			return nil, fmt.Errorf("merkle proof too large: %d > %d", len(b.MerkleProof), merge_mining.MaxChainsLog2)
		}
		buf = append(buf, uint8(len(b.MerkleProof)))
		for _, h := range b.MerkleProof {
			buf = append(buf, h[:]...)
		}

		// merge mining extra
		if len(b.MergeMiningExtra) > merge_mining.MaxChains {
			return nil, fmt.Errorf("merge mining extra size too big: %d > %d", len(b.MergeMiningExtra), merge_mining.MaxChains)
		}
		buf = binary.AppendUvarint(buf, uint64(len(b.MergeMiningExtra)))
		for i := range b.MergeMiningExtra {
			buf = append(buf, b.MergeMiningExtra[i].ChainId[:]...)
			buf = binary.AppendUvarint(buf, uint64(len(b.MergeMiningExtra[i].Data)))
			buf = append(buf, b.MergeMiningExtra[i].Data...)
		}
	}

	if version >= ShareVersion_V2 {
		buf = binary.LittleEndian.AppendUint32(buf, uint32(b.ExtraBuffer.SoftwareId))
		buf = binary.LittleEndian.AppendUint32(buf, uint32(b.ExtraBuffer.SoftwareVersion))
		buf = binary.LittleEndian.AppendUint32(buf, b.ExtraBuffer.RandomNumber)
		buf = binary.LittleEndian.AppendUint32(buf, b.ExtraBuffer.SideChainExtraNonce)
	}

	return buf, nil
}

func (b *SideData) FromReader(reader utils.ReaderAndByteReader, version ShareVersion) (err error) {
	var (
		uncleCount uint64
		uncleHash  types.Hash

		merkleProofSize          uint8
		mergeMiningExtraSize     uint64
		mergeMiningExtraDataSize uint64
	)

	if _, err = io.ReadFull(reader, b.PublicKey[address.PackedAddressSpend][:]); err != nil {
		return err
	}
	if _, err = io.ReadFull(reader, b.PublicKey[address.PackedAddressView][:]); err != nil {
		return err
	}

	if version >= ShareVersion_V2 {
		// Read private key seed instead of private key. Only on ShareVersion_V2 and above
		// needs preprocessing
		if _, err = io.ReadFull(reader, b.CoinbasePrivateKeySeed[:]); err != nil {
			return err
		}
	} else {
		if _, err = io.ReadFull(reader, b.CoinbasePrivateKey[:]); err != nil {
			return err
		}
	}

	if _, err = io.ReadFull(reader, b.Parent[:]); err != nil {
		return err
	}

	if uncleCount, err = utils.ReadCanonicalUvarint(reader); err != nil {
		return err
	} else if uncleCount > MaxUncleCount {
		return fmt.Errorf("uncle count too large: %d > %d", uncleCount, MaxUncleCount)
	} else if uncleCount > 0 {
		// preallocate for append, with 64 as soft limit
		b.Uncles = make([]types.Hash, 0, min(64, uncleCount))

		for i := 0; i < int(uncleCount); i++ {
			if _, err = io.ReadFull(reader, uncleHash[:]); err != nil {
				return err
			}
			b.Uncles = append(b.Uncles, uncleHash)
		}
	}

	if b.Height, err = utils.ReadCanonicalUvarint(reader); err != nil {
		return err
	}

	if b.Height > PoolBlockMaxSideChainHeight {
		return fmt.Errorf("side block height too high (%d > %d)", b.Height, PoolBlockMaxSideChainHeight)
	}

	{
		if b.Difficulty.Lo, err = utils.ReadCanonicalUvarint(reader); err != nil {
			return err
		}

		if b.Difficulty.Hi, err = utils.ReadCanonicalUvarint(reader); err != nil {
			return err
		}
	}

	{
		if b.CumulativeDifficulty.Lo, err = utils.ReadCanonicalUvarint(reader); err != nil {
			return err
		}

		if b.CumulativeDifficulty.Hi, err = utils.ReadCanonicalUvarint(reader); err != nil {
			return err
		}
	}

	if b.CumulativeDifficulty.Cmp(PoolBlockMaxCumulativeDifficulty) > 0 {
		return fmt.Errorf("side block cumulative difficulty too large (%s > %s)", b.CumulativeDifficulty.StringNumeric(), PoolBlockMaxCumulativeDifficulty.StringNumeric())
	}

	// Read merkle proof list of hashes. Only on ShareVersion_V3 and above
	if version >= ShareVersion_V3 {
		if merkleProofSize, err = reader.ReadByte(); err != nil {
			return err
		} else if merkleProofSize > merge_mining.MaxChainsLog2 {
			return fmt.Errorf("merkle proof too large: %d > %d", merkleProofSize, merge_mining.MaxChainsLog2)
		} else if merkleProofSize > 0 {
			// preallocate
			b.MerkleProof = make(crypto.MerkleProof, merkleProofSize)

			for i := 0; i < int(merkleProofSize); i++ {
				if _, err = io.ReadFull(reader, b.MerkleProof[i][:]); err != nil {
					return err
				}
			}
		}

		if mergeMiningExtraSize, err = utils.ReadCanonicalUvarint(reader); err != nil {
			return err
		} else if mergeMiningExtraSize > merge_mining.MaxChains {
			return fmt.Errorf("merge mining data too big: %d > %d", mergeMiningExtraSize, merge_mining.MaxChains)
		} else if mergeMiningExtraSize > 0 {
			// preallocate
			b.MergeMiningExtra = make(MergeMiningExtra, mergeMiningExtraSize)

			for i := 0; i < int(mergeMiningExtraSize); i++ {
				if _, err = io.ReadFull(reader, b.MergeMiningExtra[i].ChainId[:]); err != nil {
					return err
				} else if i > 0 && b.MergeMiningExtra[i-1].ChainId.Compare(b.MergeMiningExtra[i].ChainId) >= 0 {
					// IDs must be ordered to avoid duplicates
					return fmt.Errorf("duplicate or not ordered merge mining data chain id: %s > %s", b.MergeMiningExtra[i-1].ChainId, b.MergeMiningExtra[i].ChainId)
				} else if mergeMiningExtraDataSize, err = utils.ReadCanonicalUvarint(reader); err != nil {
					return err
				} else if mergeMiningExtraDataSize > PoolBlockMaxTemplateSize {
					return fmt.Errorf("merge mining data size too big: %d > %d", mergeMiningExtraDataSize, PoolBlockMaxTemplateSize)
				} else if mergeMiningExtraDataSize > 0 {
					b.MergeMiningExtra[i].Data = make(types.Bytes, mergeMiningExtraDataSize)
					if _, err = io.ReadFull(reader, b.MergeMiningExtra[i].Data); err != nil {
						return err
					}
				}
			}
		}
	}

	// Read share extra buffer. Only on ShareVersion_V2 and above
	if version >= ShareVersion_V2 {
		if err = binary.Read(reader, binary.LittleEndian, &b.ExtraBuffer.SoftwareId); err != nil {
			return fmt.Errorf("within extra buffer: %w", err)
		}
		if err = binary.Read(reader, binary.LittleEndian, &b.ExtraBuffer.SoftwareVersion); err != nil {
			return fmt.Errorf("within extra buffer: %w", err)
		}
		if err = binary.Read(reader, binary.LittleEndian, &b.ExtraBuffer.RandomNumber); err != nil {
			return fmt.Errorf("within extra buffer: %w", err)
		}
		if err = binary.Read(reader, binary.LittleEndian, &b.ExtraBuffer.SideChainExtraNonce); err != nil {
			return fmt.Errorf("within extra buffer: %w", err)
		}
	}

	return nil
}

func (b *SideData) UnmarshalBinary(data []byte, version ShareVersion) error {
	reader := bytes.NewReader(data)
	err := b.FromReader(reader, version)
	if err != nil {
		return err
	}
	if reader.Len() > 0 {
		return errors.New("leftover bytes in reader")
	}
	return nil
}
