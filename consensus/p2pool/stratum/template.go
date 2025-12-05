package stratum

import (
	"encoding/binary"
	"errors"
	"io"

	"git.gammaspectra.live/P2Pool/consensus/v4/merge_mining"
	mainblock "git.gammaspectra.live/P2Pool/consensus/v4/monero/block"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/crypto"
	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/sidechain"
	p2pooltypes "git.gammaspectra.live/P2Pool/consensus/v4/p2pool/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
	"git.gammaspectra.live/P2Pool/sha3"
)

type Template struct {
	Buffer []byte

	// NonceOffset offset of an uint32
	NonceOffset int

	CoinbaseOffset int

	// ExtraNonceOffset offset of an uint32
	ExtraNonceOffset int

	// MerkleRootOffset offset of a types.Hash for merge mining id
	MerkleRootOffset int

	// TransactionsOffset Start of transactions section
	TransactionsOffset int

	// TemplateSideDataOffset Start of side data section
	TemplateSideDataOffset int

	MainHeight uint64
	MainParent types.Hash

	SideHeight     uint64
	SideParent     types.Hash
	SideDifficulty types.Difficulty

	MerkleTreeMainBranch []types.Hash
}

func (tpl *Template) BufferLength(consensus *sidechain.Consensus, merkleProof crypto.MerkleProof, mmExtra sidechain.MergeMiningExtra) int {
	if version := tpl.ShareVersion(consensus); version >= sidechain.ShareVersion_V3 {
		return len(tpl.Buffer) + 4*4 +
			1 + len(merkleProof)*types.HashSize + mmExtra.BufferLength()
	} else if version >= sidechain.ShareVersion_V2 {
		return len(tpl.Buffer) + 4*4
	}
	return len(tpl.Buffer)
}

func (tpl *Template) Write(writer io.Writer, consensus *sidechain.Consensus, nonce, extraNonce, sideRandomNumber, sideExtraNonce uint32, merkleRoot types.Hash, merkleProof crypto.MerkleProof, mmExtra sidechain.MergeMiningExtra, softwareId p2pooltypes.SoftwareId, softwareVersion p2pooltypes.SoftwareVersion) error {
	var uint32Buf [4]byte

	// write main data just before nonce
	if _, err := writer.Write(tpl.Buffer[:tpl.NonceOffset]); err != nil {
		return err
	}

	binary.LittleEndian.PutUint32(uint32Buf[:], nonce)
	if _, err := writer.Write(uint32Buf[:]); err != nil {
		return err
	}

	// write main data just before extra nonce in coinbase
	if _, err := writer.Write(tpl.Buffer[tpl.NonceOffset+4 : tpl.ExtraNonceOffset]); err != nil {
		return err
	}

	binary.LittleEndian.PutUint32(uint32Buf[:], extraNonce)
	if _, err := writer.Write(uint32Buf[:]); err != nil {
		return err
	}

	// write remaining main data, then write side data just before merge mining tag in coinbase
	if _, err := writer.Write(tpl.Buffer[tpl.ExtraNonceOffset+4 : tpl.MerkleRootOffset]); err != nil {
		return err
	}

	if _, err := writer.Write(merkleRoot[:]); err != nil {
		return err
	}

	// write main data and side data up to the end of side data extra
	if _, err := writer.Write(tpl.Buffer[tpl.MerkleRootOffset+types.HashSize:]); err != nil {
		return err
	}

	version := tpl.ShareVersion(consensus)
	if version >= sidechain.ShareVersion_V3 {
		if len(merkleProof) > merge_mining.MaxChainsLog2 {
			return errors.New("merkle proof too large")
		}
		uint32Buf[0] = uint8(len(merkleProof))
		if _, err := writer.Write(uint32Buf[:1]); err != nil {
			return err
		}

		for _, e := range merkleProof {
			if _, err := writer.Write(e[:]); err != nil {
				return err
			}
		}

		if len(mmExtra) > merge_mining.MaxChains {
			return errors.New("merge mining extra too large")
		}
		n := binary.PutUvarint(uint32Buf[:], uint64(len(mmExtra)))
		if _, err := writer.Write(uint32Buf[:n]); err != nil {
			return err
		}

		for _, extra := range mmExtra {
			if _, err := writer.Write(extra.ChainId[:]); err != nil {
				return err
			}

			if len(extra.Data) > sidechain.PoolBlockMaxTemplateSize {
				return errors.New("merge mining extra data too large")
			}

			n = binary.PutUvarint(uint32Buf[:], uint64(len(extra.Data)))
			if _, err := writer.Write(uint32Buf[:n]); err != nil {
				return err
			}
			if _, err := writer.Write(extra.Data[:]); err != nil {
				return err
			}
		}
	}

	if version >= sidechain.ShareVersion_V2 {
		binary.LittleEndian.PutUint32(uint32Buf[:], uint32(softwareId))
		if _, err := writer.Write(uint32Buf[:]); err != nil {
			return err
		}

		binary.LittleEndian.PutUint32(uint32Buf[:], uint32(softwareVersion))
		if _, err := writer.Write(uint32Buf[:]); err != nil {
			return err
		}

		binary.LittleEndian.PutUint32(uint32Buf[:], sideRandomNumber)
		if _, err := writer.Write(uint32Buf[:]); err != nil {
			return err
		}

		binary.LittleEndian.PutUint32(uint32Buf[:], sideExtraNonce)
		if _, err := writer.Write(uint32Buf[:]); err != nil {
			return err
		}
	}

	return nil
}

func (tpl *Template) Blob(preAllocatedBuffer []byte, consensus *sidechain.Consensus, nonce, extraNonce, sideRandomNumber, sideExtraNonce uint32, merkleRoot types.Hash, merkleProof crypto.MerkleProof, mmExtra sidechain.MergeMiningExtra, softwareId p2pooltypes.SoftwareId, softwareVersion p2pooltypes.SoftwareVersion) []byte {
	buf := append(preAllocatedBuffer, tpl.Buffer...)

	// Overwrite nonce
	binary.LittleEndian.PutUint32(buf[tpl.NonceOffset:], nonce)
	// Overwrite extra nonce
	binary.LittleEndian.PutUint32(buf[tpl.ExtraNonceOffset:], extraNonce)
	// Overwrite template id
	copy(buf[tpl.MerkleRootOffset:], merkleRoot[:])

	version := tpl.ShareVersion(consensus)

	if version >= sidechain.ShareVersion_V3 {
		if len(merkleProof) > merge_mining.MaxChainsLog2 {
			return nil
		}
		buf = append(buf, uint8(len(merkleProof)))

		for _, e := range merkleProof {
			buf = append(buf, e[:]...)
		}

		if len(mmExtra) > merge_mining.MaxChains {
			return nil
		}
		buf = binary.AppendUvarint(buf, uint64(len(mmExtra)))

		for _, extra := range mmExtra {
			buf = append(buf, extra.ChainId[:]...)

			if len(extra.Data) > sidechain.PoolBlockMaxTemplateSize {
				return nil
			}

			buf = binary.AppendUvarint(buf, uint64(len(extra.Data)))
			buf = append(buf, extra.Data[:]...)
		}
	}

	if version >= sidechain.ShareVersion_V2 {
		buf = binary.LittleEndian.AppendUint32(buf, uint32(softwareId))
		buf = binary.LittleEndian.AppendUint32(buf, uint32(softwareVersion))
		// Overwrite sidechain random number
		buf = binary.LittleEndian.AppendUint32(buf, sideRandomNumber)
		// Overwrite sidechain extra nonce number
		buf = binary.LittleEndian.AppendUint32(buf, sideExtraNonce)
	}

	return buf
}

func (tpl *Template) TemplateId(hasher *sha3.HasherState, preAllocatedBuffer []byte, consensus *sidechain.Consensus, sideRandomNumber, sideExtraNonce uint32, merkleProof crypto.MerkleProof, mmExtra sidechain.MergeMiningExtra, softwareId p2pooltypes.SoftwareId, softwareVersion p2pooltypes.SoftwareVersion, result *types.Hash) {
	buf := tpl.Blob(preAllocatedBuffer, consensus, 0, 0, sideRandomNumber, sideExtraNonce, types.ZeroHash, merkleProof, mmExtra, softwareId, softwareVersion)

	_, _ = hasher.Write(buf)
	_, _ = hasher.Write(consensus.Id[:])

	crypto.HashFastSum(hasher, (*result)[:])
	hasher.Reset()
}

func (tpl *Template) MainBlock() (b mainblock.Block, err error) {
	err = b.UnmarshalBinary(tpl.Buffer, false, nil)
	if err != nil {
		return b, err
	}
	return b, nil
}

func (tpl *Template) Timestamp() uint64 {
	t, _ := utils.CanonicalUvarint(tpl.Buffer[2:])
	return t
}

func (tpl *Template) ShareVersion(consensus *sidechain.Consensus) sidechain.ShareVersion {
	return sidechain.P2PoolShareVersion(consensus, tpl.Timestamp())
}

func (tpl *Template) CoinbaseBufferLength() int {
	return tpl.TransactionsOffset - tpl.CoinbaseOffset
}

func (tpl *Template) CoinbaseBlob(preAllocatedBuffer []byte, extraNonce uint32, merkleRoot types.Hash) []byte {
	buf := append(preAllocatedBuffer, tpl.Buffer[tpl.CoinbaseOffset:tpl.TransactionsOffset]...)

	// Overwrite extra nonce
	binary.LittleEndian.PutUint32(buf[tpl.ExtraNonceOffset-tpl.CoinbaseOffset:], extraNonce)
	// Overwrite merkle root
	copy(buf[tpl.MerkleRootOffset-tpl.CoinbaseOffset:], merkleRoot[:])

	return buf
}

func (tpl *Template) CoinbaseBlobId(hasher *sha3.HasherState, preAllocatedBuffer []byte, extraNonce uint32, templateId types.Hash, result *types.Hash) {

	buf := tpl.CoinbaseBlob(preAllocatedBuffer, extraNonce, templateId)
	_, _ = hasher.Write(buf[:len(buf)-1])
	crypto.HashFastSum(hasher, (*result)[:])
	hasher.Reset()

	CoinbaseIdHash(hasher, *result, result)
}

func (tpl *Template) CoinbaseId(hasher *sha3.HasherState, extraNonce uint32, merkleRoot types.Hash, result *types.Hash) {

	var extraNonceBuf [4]byte

	_, _ = hasher.Write(tpl.Buffer[tpl.CoinbaseOffset:tpl.ExtraNonceOffset])
	// extra nonce
	binary.LittleEndian.PutUint32(extraNonceBuf[:], extraNonce)
	_, _ = hasher.Write(extraNonceBuf[:])

	_, _ = hasher.Write(tpl.Buffer[tpl.ExtraNonceOffset+4 : tpl.MerkleRootOffset])
	// merkle root
	_, _ = hasher.Write(merkleRoot[:])

	_, _ = hasher.Write(tpl.Buffer[tpl.MerkleRootOffset+types.HashSize : tpl.TransactionsOffset-1])

	crypto.HashFastSum(hasher, (*result)[:])
	hasher.Reset()

	CoinbaseIdHash(hasher, *result, result)
}

var zeroExtraBaseRCTHash = crypto.PooledKeccak256([]byte{0})

func CoinbaseIdHash(hasher *sha3.HasherState, coinbaseBlobMinusBaseRTC types.Hash, result *types.Hash) {
	_, _ = hasher.Write(coinbaseBlobMinusBaseRTC[:])
	// Base RCT, single 0 byte in miner tx
	_, _ = hasher.Write(zeroExtraBaseRCTHash[:])
	// Prunable RCT, empty in miner tx
	_, _ = hasher.Write(types.ZeroHash[:])
	crypto.HashFastSum(hasher, (*result)[:])
	hasher.Reset()
}

func (tpl *Template) HashingBlobBufferLength() int {
	_, n := utils.CanonicalUvarint(tpl.Buffer[tpl.TransactionsOffset:])

	return tpl.NonceOffset + 4 + types.HashSize + n
}

func (tpl *Template) HashingBlob(hasher *sha3.HasherState, preAllocatedBuffer []byte, nonce, extraNonce uint32, merkleRoot types.Hash) []byte {

	var rootHash types.Hash
	tpl.CoinbaseId(hasher, extraNonce, merkleRoot, &rootHash)

	buf := append(preAllocatedBuffer, tpl.Buffer[:tpl.NonceOffset]...)
	buf = binary.LittleEndian.AppendUint32(buf, nonce)

	numTransactions, n := utils.CanonicalUvarint(tpl.Buffer[tpl.TransactionsOffset:])

	if numTransactions < 1 {
	} else if numTransactions < 2 {
		_, _ = hasher.Write(rootHash[:])
		_, _ = hasher.Write(tpl.Buffer[tpl.TransactionsOffset+n : tpl.TransactionsOffset+n+types.HashSize])
		crypto.HashFastSum(hasher, rootHash[:])
		hasher.Reset()
	} else {
		for i := range tpl.MerkleTreeMainBranch {
			_, _ = hasher.Write(rootHash[:])
			_, _ = hasher.Write(tpl.MerkleTreeMainBranch[i][:])
			crypto.HashFastSum(hasher, rootHash[:])
			hasher.Reset()
		}
	}

	buf = append(buf, rootHash[:]...)
	buf = binary.AppendUvarint(buf, numTransactions+1)
	return buf
}

func TemplateFromPoolBlock(consensus *sidechain.Consensus, b *sidechain.PoolBlock) (tpl *Template, err error) {
	version := b.ShareVersion()
	if version < sidechain.ShareVersion_V1 || version > sidechain.ShareVersion_V3 {
		return nil, errors.New("unsupported share version")
	}

	buf := make([]byte, 0, b.BufferLength())
	if buf, err = b.AppendBinaryFlags(buf, false, false); err != nil {
		return nil, err
	}

	tpl = &Template{}

	const (
		CoinbaseExtraNonceIndex       = 1
		CoinbaseExtraMergeMiningIndex = 2
	)

	mainBufferLength := b.Main.BufferLength()
	coinbaseLength := b.Main.Coinbase.BufferLength()
	tpl.NonceOffset = mainBufferLength - (4 + coinbaseLength + utils.UVarInt64Size(len(b.Main.Transactions)) + types.HashSize*len(b.Main.Transactions))

	tpl.CoinbaseOffset = mainBufferLength - (coinbaseLength + utils.UVarInt64Size(len(b.Main.Transactions)) + types.HashSize*len(b.Main.Transactions))

	tpl.TransactionsOffset = mainBufferLength - (utils.UVarInt64Size(len(b.Main.Transactions)) + types.HashSize*len(b.Main.Transactions))

	tpl.ExtraNonceOffset = tpl.NonceOffset + 4 + (coinbaseLength - (b.Main.Coinbase.Extra[CoinbaseExtraNonceIndex].BufferLength() + b.Main.Coinbase.Extra[CoinbaseExtraMergeMiningIndex].BufferLength() + 1)) + 1 + utils.UVarInt64Size(b.Main.Coinbase.Extra[CoinbaseExtraNonceIndex].VarInt)

	if version >= sidechain.ShareVersion_V3 {
		tpl.MerkleRootOffset = tpl.NonceOffset + 4 + (coinbaseLength - 1 /*extra base rct*/ - types.HashSize)
	} else {
		tpl.MerkleRootOffset = tpl.NonceOffset + 4 + (coinbaseLength - (b.Main.Coinbase.Extra[CoinbaseExtraMergeMiningIndex].BufferLength() + 1)) + 1 + utils.UVarInt64Size(b.Main.Coinbase.Extra[CoinbaseExtraMergeMiningIndex].VarInt)
	}

	tpl.TemplateSideDataOffset = mainBufferLength

	var bufOffset int
	if version >= sidechain.ShareVersion_V3 {
		bufOffset = utils.UVarInt64Size(len(b.Side.MerkleProof)) + len(b.Side.MerkleProof)*types.HashSize +
			b.Side.MergeMiningExtra.BufferLength() +
			4*4
	} else if version >= sidechain.ShareVersion_V2 {
		bufOffset = 4 * 4
	}

	tpl.Buffer = buf[:len(buf)-bufOffset]

	// Set places to zeroes where necessary
	tpl.Buffer = tpl.Blob(make([]byte, 0, len(buf)), consensus, 0, 0, 0, 0, types.ZeroHash, b.Side.MerkleProof, b.Side.MergeMiningExtra, 0, 0)[:len(buf)-bufOffset]

	if len(b.Main.Transactions) > 1 {
		merkleTree := make(crypto.BinaryTreeHash, len(b.Main.Transactions)+1)
		copy(merkleTree[1:], b.Main.Transactions)
		tpl.MerkleTreeMainBranch = merkleTree.MainBranch()
	}

	tpl.MainHeight = b.Main.Coinbase.GenHeight
	tpl.MainParent = b.Main.PreviousId

	tpl.SideHeight = b.Side.Height
	tpl.SideParent = b.Side.Parent
	tpl.SideDifficulty = b.Side.Difficulty

	return tpl, nil
}
