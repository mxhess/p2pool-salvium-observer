package sidechain

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/netip"
	"slices"
	"sync/atomic"
	"time"
	"unsafe"

	"git.gammaspectra.live/P2Pool/consensus/v4/merge_mining"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/address"
	mainblock "git.gammaspectra.live/P2Pool/consensus/v4/monero/block"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/crypto"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/randomx"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/transaction"
	p2poolcrypto "git.gammaspectra.live/P2Pool/consensus/v4/p2pool/crypto"
	p2pooltypes "git.gammaspectra.live/P2Pool/consensus/v4/p2pool/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
	fasthex "github.com/tmthrgd/go-hex"
)

type CoinbaseExtraTag int

const SideExtraNonceSize = 4
const SideExtraNonceMaxSize = SideExtraNonceSize + 10

const (
	SideCoinbasePublicKey = transaction.TxExtraTagPubKey
	SideExtraNonce        = transaction.TxExtraTagNonce

	// SideIdentifierHash Depending on version, this can be a PoolBlock TemplateId or Merkle Root Hash
	SideIdentifierHash = transaction.TxExtraTagMergeMining
)

// PoolBlockMaxTemplateSize Max P2P message size (128 KB) minus BLOCK_RESPONSE header (5 bytes)
const PoolBlockMaxTemplateSize = 128*1024 - (1 + 4)

// PoolBlockMaxSideChainHeight 1000 years at 1 block/second. It should be enough for any normal use.
const PoolBlockMaxSideChainHeight = 31556952000

// PoolBlockMaxCumulativeDifficulty 1000 years at 1 TH/s. It should be enough for any normal use.
var PoolBlockMaxCumulativeDifficulty = types.NewDifficulty(13019633956666736640, 1710)

type UniquePoolBlockSlice []*PoolBlock

func (s UniquePoolBlockSlice) Get(consensus *Consensus, id types.Hash) *PoolBlock {
	if i := slices.IndexFunc(s, func(p *PoolBlock) bool {
		return p.FastSideTemplateId(consensus) == id
	}); i != -1 {
		return s[i]
	}
	return nil
}

func (s UniquePoolBlockSlice) GetHeight(height uint64) (result UniquePoolBlockSlice) {
	for _, b := range s {
		if b.Side.Height == height {
			result = append(result, b)
		}
	}
	return result
}

// IterationCache Used for fast scan backwards, and of uncles
// Only maybe available in verified blocks
type IterationCache struct {
	Parent *PoolBlock
	Uncles []*PoolBlock
}

type PoolBlock struct {
	Main mainblock.Block `json:"main"`

	Side SideData `json:"side"`

	//Temporary data structures
	mergeMiningTag merge_mining.Tag

	cache    poolBlockCache
	Depth    atomic.Uint64 `json:"-"`
	Verified atomic.Bool   `json:"-"`
	Invalid  atomic.Bool   `json:"-"`

	WantBroadcast atomic.Bool `json:"-"`
	Broadcasted   atomic.Bool `json:"-"`
	Thinned       atomic.Bool `json:"-"`

	Metadata PoolBlockReceptionMetadata `json:"metadata"`

	CachedShareVersion ShareVersion `json:"share_version"`

	iterationCache *IterationCache
}

type PoolBlockReceptionMetadata struct {
	// LocalTime Moment the block was received from a source
	LocalTime time.Time `json:"local_time,omitempty"`
	// AddressPort The address and port of the peer who broadcasted or sent us this block
	// If the peer specified a listen port, the port will be that instead of current connection one
	AddressPort netip.AddrPort `json:"address_port,omitempty"`
	// PeerId The peer id of the peer who broadcasted or sent us this block
	PeerId uint64 `json:"peer_id,omitempty"`

	SoftwareId      uint32 `json:"software_id"`
	SoftwareVersion uint32 `json:"software_version"`
}

func (m *PoolBlockReceptionMetadata) UnmarshalBinary(buf []byte) error {
	r := bytes.NewReader(buf)
	s, err := utils.ReadCanonicalUvarint(r)
	if err != nil {
		return err
	}
	ns, err := utils.ReadCanonicalUvarint(r)
	if err != nil {
		return err
	}

	m.LocalTime = time.Unix(int64(s), int64(ns)).UTC()

	l, err := utils.ReadCanonicalUvarint(r)
	if err != nil {
		return err
	}
	if l > 256 {
		return errors.New("too large ip")
	}
	ip := make([]byte, l)
	_, err = io.ReadFull(r, ip)
	if err != nil {
		return err
	}

	err = m.AddressPort.UnmarshalBinary(ip)
	if err != nil {
		return err
	}

	m.PeerId, err = utils.ReadCanonicalUvarint(r)
	if err != nil {
		return err
	}

	sId, err := utils.ReadCanonicalUvarint(r)
	if err != nil {
		return err
	}

	sVer, err := utils.ReadCanonicalUvarint(r)
	if err != nil {
		return err
	}

	m.SoftwareId = uint32(sId)
	m.SoftwareVersion = uint32(sVer)

	if r.Len() > 0 {
		return errors.New("leftover bytes in reader")
	}

	return nil
}

func (m *PoolBlockReceptionMetadata) MarshalBinary() ([]byte, error) {
	s := uint64(m.LocalTime.Unix())
	ns := uint64(m.LocalTime.Nanosecond())

	ip, _ := m.AddressPort.MarshalBinary()
	out := make([]byte, 0,
		utils.UVarInt64Size(s)+utils.UVarInt64Size(ns)+
			utils.UVarInt64Size(len(ip))+len(ip)+
			utils.UVarInt64Size(m.PeerId)+
			utils.UVarInt64Size(uint64(m.SoftwareId))+
			utils.UVarInt64Size(uint64(m.SoftwareVersion)))

	out = binary.AppendUvarint(out, s)
	out = binary.AppendUvarint(out, ns)
	out = binary.AppendUvarint(out, uint64(len(ip)))
	out = append(out, ip...)
	out = binary.AppendUvarint(out, m.PeerId)
	out = binary.AppendUvarint(out, uint64(m.SoftwareId))
	out = binary.AppendUvarint(out, uint64(m.SoftwareVersion))
	return out, nil
}

func (b *PoolBlock) iteratorGetParent(getByTemplateId GetByTemplateIdFunc) *PoolBlock {
	if b.iterationCache == nil {
		return getByTemplateId(b.Side.Parent)
	}
	return b.iterationCache.Parent
}

func (b *PoolBlock) iteratorUncles(getByTemplateId GetByTemplateIdFunc, uncleFunc func(uncle *PoolBlock)) error {
	if len(b.Side.Uncles) == 0 {
		return nil
	}
	if b.iterationCache == nil {
		for _, uncleId := range b.Side.Uncles {
			uncle := getByTemplateId(uncleId)
			if uncle == nil {
				return fmt.Errorf("could not find uncle %x", uncleId.Slice())
			}
			uncleFunc(uncle)
		}
	} else {
		for _, uncle := range b.iterationCache.Uncles {
			uncleFunc(uncle)
		}
	}

	return nil
}

func (b *PoolBlock) NeedsCompactTransactionFilling() bool {
	return len(b.Main.TransactionParentIndices) > 0 && len(b.Main.TransactionParentIndices) == len(b.Main.Transactions) && slices.Contains(b.Main.Transactions, types.ZeroHash)
}

func (b *PoolBlock) FillTransactionsFromTransactionParentIndices(consensus *Consensus, parent *PoolBlock) error {
	if b.NeedsCompactTransactionFilling() {
		if parent != nil && ((parent.NeedsPreProcess() && (parent.Side.Height == (b.Side.Height - 1))) || parent.FastSideTemplateId(consensus) == b.Side.Parent) {
			for i, parentIndex := range b.Main.TransactionParentIndices {
				if parentIndex != 0 {
					// p2pool stores coinbase transaction hash as well, decrease
					actualIndex := parentIndex - 1
					if actualIndex > uint64(len(parent.Main.Transactions)) {
						return errors.New("index of parent transaction out of bounds")
					}
					if parent.Main.Transactions[actualIndex] == types.ZeroHash {
						return errors.New("parent transaction is zero")
					}
					b.Main.Transactions[i] = parent.Main.Transactions[actualIndex]
				}
			}
		} else if parent == nil {
			return errors.New("parent is nil")
		} else {
			return errors.New("mismatched parent height or template id")
		}
	}

	return nil
}

func (b *PoolBlock) FillTransactionParentIndices(consensus *Consensus, parent *PoolBlock) bool {
	if len(b.Main.Transactions) != len(b.Main.TransactionParentIndices) {
		if parent != nil && parent.FastSideTemplateId(consensus) == b.Side.Parent {
			b.Main.TransactionParentIndices = make([]uint64, len(b.Main.Transactions))
			//do not fail if not found
			for i, txHash := range b.Main.Transactions {
				if parentIndex := slices.Index(parent.Main.Transactions, txHash); parentIndex != -1 {
					//increase as p2pool stores tx hash as well
					b.Main.TransactionParentIndices[i] = uint64(parentIndex + 1)
				}
			}
			return true
		}

		return false
	}

	return true
}

func (b *PoolBlock) CalculateShareVersion(consensus *Consensus) ShareVersion {
	return P2PoolShareVersion(consensus, b.Main.Timestamp)
}

func (b *PoolBlock) ShareVersion() ShareVersion {
	return b.CachedShareVersion
}

func (b *PoolBlock) ShareVersionSignaling() ShareVersion {
	// Signaling before V2 hardfork
	if b.ShareVersion() == ShareVersion_V1 && (b.ExtraNonce()&0xFF000000 == 0xFF000000) {
		return ShareVersion_V2
	}

	// Implicit signaling based on share software id and version
	if b.ShareVersion() == ShareVersion_V2 &&
		((b.Side.ExtraBuffer.SoftwareId == p2pooltypes.SoftwareIdP2Pool && b.Side.ExtraBuffer.SoftwareVersion.Major() >= 4) ||
			(b.Side.ExtraBuffer.SoftwareId == p2pooltypes.SoftwareIdGoObserver && b.Side.ExtraBuffer.SoftwareVersion.Major() >= 4)) {
		return ShareVersion_V3
	}

	return ShareVersion_None
}

func (b *PoolBlock) ExtraNonce() uint32 {
	extraNonce := b.CoinbaseExtra(SideExtraNonce)
	if len(extraNonce) < SideExtraNonceSize {
		return 0
	}
	return binary.LittleEndian.Uint32(extraNonce)
}

// FastSideTemplateId Returns SideTemplateId from either coinbase extra tags or pruned data, or main block if not pruned
func (b *PoolBlock) FastSideTemplateId(consensus *Consensus) types.Hash {
	if b.ShareVersion() >= ShareVersion_V3 {
		if b.Main.Coinbase.AuxiliaryData.TemplateId != types.ZeroHash {
			return b.Main.Coinbase.AuxiliaryData.TemplateId
		} else {
			// not merge mining, hash should be equal
			mmTag := b.MergeMiningTag()
			if mmTag.NumberAuxiliaryChains == 1 {
				return mmTag.RootHash
			}

			//fallback to full calculation
			return b.SideTemplateId(consensus)
		}
	} else {
		return types.HashFromBytes(b.CoinbaseExtra(SideIdentifierHash))
	}
}

func (b *PoolBlock) CoinbaseExtra(tag CoinbaseExtraTag) []byte {
	switch tag {
	case SideExtraNonce:
		if t := b.Main.Coinbase.Extra.GetTag(uint8(tag)); t != nil {
			if len(t.Data) < SideExtraNonceSize || len(t.Data) > SideExtraNonceMaxSize {
				return nil
			}
			return t.Data
		}
	case SideIdentifierHash:
		if t := b.Main.Coinbase.Extra.GetTag(uint8(tag)); t != nil {
			if b.ShareVersion() >= ShareVersion_V3 {
				// new merge mining tag
				mergeMineReader := bytes.NewReader(t.Data)
				var mergeMiningTag merge_mining.Tag
				if err := mergeMiningTag.FromReader(mergeMineReader); err != nil || mergeMineReader.Len() != 0 {
					return nil
				}

				return mergeMiningTag.RootHash[:]
			} else {
				if t.VarInt != types.HashSize || len(t.Data) != types.HashSize {
					return nil
				}
				return t.Data
			}
		}
	case SideCoinbasePublicKey:
		if t := b.Main.Coinbase.Extra.GetTag(uint8(tag)); t != nil {
			if len(t.Data) != crypto.PublicKeySize {
				return nil
			}
			return t.Data
		}
	}

	return nil
}

func (b *PoolBlock) MainId() types.Hash {
	return b.Main.Id()
}

func (b *PoolBlock) FullId(consensus *Consensus) FullId {
	var buf FullId

	sidechainId := b.FastSideTemplateId(consensus)
	copy(buf[:], sidechainId[:])
	binary.LittleEndian.PutUint32(buf[types.HashSize:], b.Main.Nonce)
	binary.LittleEndian.PutUint32(buf[types.HashSize+unsafe.Sizeof(b.Main.Nonce):], b.ExtraNonce())
	return buf
}

const FullIdSize = int(types.HashSize + unsafe.Sizeof(uint32(0)) + SideExtraNonceSize)

var zeroFullId FullId

type FullId [FullIdSize]byte

func FullIdFromString(s string) (FullId, error) {
	var h FullId
	if buf, err := fasthex.DecodeString(s); err != nil {
		return h, err
	} else {
		if len(buf) != FullIdSize {
			return h, errors.New("wrong hash size")
		}
		copy(h[:], buf)
		return h, nil
	}
}

func (id FullId) TemplateId() (h types.Hash) {
	return types.Hash(id[:types.HashSize])
}

func (id FullId) Nonce() uint32 {
	return binary.LittleEndian.Uint32(id[types.HashSize:])
}

func (id FullId) ExtraNonce() uint32 {
	return binary.LittleEndian.Uint32(id[types.HashSize+unsafe.Sizeof(uint32(0)):])
}

func (id FullId) String() string {
	return fasthex.EncodeToString(id[:])
}

func (b *PoolBlock) CalculateFullId(consensus *Consensus) FullId {
	var buf FullId
	sidechainId := b.SideTemplateId(consensus)
	copy(buf[:], sidechainId[:])
	binary.LittleEndian.PutUint32(buf[types.HashSize:], b.Main.Nonce)
	binary.LittleEndian.PutUint32(buf[types.HashSize+unsafe.Sizeof(b.Main.Nonce):], b.ExtraNonce())
	return buf
}

func (b *PoolBlock) MainDifficulty(f mainblock.GetDifficultyByHeightFunc) types.Difficulty {
	return b.Main.Difficulty(f)
}

func (b *PoolBlock) SideTemplateId(consensus *Consensus) types.Hash {
	if h := b.cache.templateId.Load(); h != nil {
		return *h
	} else {
		if b.NeedsPreProcess() {
			return types.ZeroHash
		}
		hash := consensus.CalculateSideTemplateId(b)
		if hash == types.ZeroHash {
			return types.ZeroHash
		}
		b.cache.templateId.Store(&hash)
		return hash
	}
}

func (b *PoolBlock) CoinbaseId() types.Hash {
	if h := b.cache.coinbaseId.Load(); h != nil {
		return *h
	} else {
		hash := b.Main.Coinbase.CalculateId()
		if hash == types.ZeroHash {
			return types.ZeroHash
		}
		b.cache.coinbaseId.Store(&hash)
		return hash
	}
}

func (b *PoolBlock) MergeMiningTag() merge_mining.Tag {
	return b.mergeMiningTag
}

func (b *PoolBlock) PowHash(hasher randomx.Hasher, f mainblock.GetSeedByHeightFunc) types.Hash {
	h, _ := b.PowHashWithError(hasher, f)
	return h
}

func (b *PoolBlock) PowHashWithError(hasher randomx.Hasher, f mainblock.GetSeedByHeightFunc) (powHash types.Hash, err error) {
	if h := b.cache.powHash.Load(); h != nil {
		powHash = *h
	} else {
		powHash, err = b.Main.PowHashWithError(hasher, f)
		if powHash == types.ZeroHash {
			return types.ZeroHash, err
		}
		b.cache.powHash.Store(&powHash)
	}

	return powHash, nil
}

func (b *PoolBlock) UnmarshalBinary(consensus *Consensus, derivationCache DerivationCacheInterface, data []byte) error {
	if len(data) > PoolBlockMaxTemplateSize {
		return errors.New("buffer too large")
	}
	reader := bytes.NewReader(data)
	err := b.FromReader(consensus, derivationCache, reader)
	if err != nil {
		return err
	}
	if reader.Len() > 0 {
		return errors.New("leftover bytes in reader")
	}
	return nil
}

func (b *PoolBlock) BufferLength() int {
	return b.Main.BufferLength() + b.Side.BufferLength(b.ShareVersion())
}

func (b *PoolBlock) MarshalBinary() ([]byte, error) {
	return b.MarshalBinaryFlags(false, false)
}

func (b *PoolBlock) MarshalBinaryFlags(pruned, compact bool) ([]byte, error) {
	return b.AppendBinaryFlags(make([]byte, 0, b.BufferLength()), pruned, compact)
}

func (b *PoolBlock) AppendBinaryFlags(preAllocatedBuf []byte, pruned, compact bool) (buf []byte, err error) {
	buf = preAllocatedBuf

	if buf, err = b.Main.AppendBinaryFlags(buf, compact, pruned, b.ShareVersion() >= ShareVersion_V3); err != nil {
		return nil, err
	} else if buf, err = b.Side.AppendBinary(buf, b.ShareVersion()); err != nil {
		return nil, err
	} else {
		if len(buf) > PoolBlockMaxTemplateSize {
			return nil, errors.New("buffer too large")
		}
		return buf, nil
	}
}

func (b *PoolBlock) FromReader(consensus *Consensus, derivationCache DerivationCacheInterface, reader utils.ReaderAndByteReader) (err error) {
	if err = b.Main.FromReader(reader, true, func() (containsAuxiliaryTemplateId bool) {
		return b.CalculateShareVersion(consensus) >= ShareVersion_V3
	}); err != nil {
		return err
	}

	return b.consensusDecode(consensus, derivationCache, reader)
}

// FromCompactReader used in Protocol 1.1 and above
func (b *PoolBlock) FromCompactReader(consensus *Consensus, derivationCache DerivationCacheInterface, reader utils.ReaderAndByteReader) (err error) {
	if err = b.Main.FromCompactReader(reader, true, func() (containsAuxiliaryTemplateId bool) {
		return b.CalculateShareVersion(consensus) >= ShareVersion_V3
	}); err != nil {
		return err
	}

	return b.consensusDecode(consensus, derivationCache, reader)
}

func (b *PoolBlock) UnmarshalJSON(buf []byte) (err error) {
	type poolBlock PoolBlock
	// unalias types
	err = utils.UnmarshalJSON(buf, (*poolBlock)(b))
	if err != nil {
		return err
	}

	return b.consensusMergeMiningTag()
}

func (b *PoolBlock) consensusMergeMiningTag() (err error) {
	mergeMineTag := b.Main.Coinbase.Extra.GetTag(transaction.TxExtraTagMergeMining)

	if mergeMineTag == nil {
		return errors.New("missing merge mining tag")
	}

	if mergeMineTag.VarInt > transaction.TxExtraTagMergeMiningMaxCount {
		return errors.New("merge mining is too big")
	}

	if b.ShareVersion() <= ShareVersion_V2 {
		//TODO: this is to comply with non-standard p2pool serialization, see https://github.com/SChernykh/p2pool/issues/249
		if mergeMineTag.VarInt != types.HashSize {
			return errors.New("wrong merge mining tag depth")
		}
	} else {
		//properly decode merge mining tag
		mergeMineReader := bytes.NewReader(mergeMineTag.Data)
		if err = b.mergeMiningTag.FromReader(mergeMineReader); err != nil {
			return err
		}
		if mergeMineReader.Len() != 0 {
			return errors.New("wrong merge mining tag len")
		}
	}
	return nil
}

func (b *PoolBlock) consensusDecode(consensus *Consensus, derivationCache DerivationCacheInterface, reader utils.ReaderAndByteReader) (err error) {
	if expectedMajorVersion := monero.NetworkMajorVersion(consensus.NetworkType.MustAddressNetwork(), b.Main.Coinbase.GenHeight); expectedMajorVersion != b.Main.MajorVersion {
		return fmt.Errorf("expected major version %d at height %d, got %d", expectedMajorVersion, b.Main.Coinbase.GenHeight, b.Main.MajorVersion)
	}

	if b.CachedShareVersion == ShareVersion_None {
		b.CachedShareVersion = b.CalculateShareVersion(consensus)
	}

	if b.ShareVersion() >= ShareVersion_V3 {
		if b.Main.MajorVersion > math.MaxInt8 || b.Main.MinorVersion > math.MaxInt8 {
			return errors.New("version exceeds allowed values")
		}
	}

	if err = b.consensusMergeMiningTag(); err != nil {
		return err
	}

	// verify number and order of tags
	if extra := b.Main.Coinbase.Extra; len(extra) != 3 {
		return errors.New("wrong coinbase extra tag count")
	} else if extra[0].Tag != transaction.TxExtraTagPubKey {
		return errors.New("wrong coinbase extra tag at index 0")
	} else if extra[1].Tag != transaction.TxExtraTagNonce {
		return errors.New("wrong coinbase extra tag at index 1")
	} else if extra[2].Tag != transaction.TxExtraTagMergeMining {
		return errors.New("wrong coinbase extra tag at index 2")
	}

	if err = b.Side.FromReader(reader, b.ShareVersion()); err != nil {
		return err
	}

	b.FillPrivateKeys(derivationCache)

	return nil
}

// PreProcessBlock processes and fills the block data from either pruned or compact modes
func (b *PoolBlock) PreProcessBlock(consensus *Consensus, derivationCache DerivationCacheInterface, preAllocatedShares Shares, difficultyByHeight mainblock.GetDifficultyByHeightFunc, getTemplateById GetByTemplateIdFunc) (missingBlocks []types.Hash, err error) {
	return b.PreProcessBlockWithOutputs(consensus, getTemplateById, func() (outputs transaction.Outputs, bottomHeight uint64, err error) {
		return CalculateOutputs(b, consensus, difficultyByHeight, getTemplateById, derivationCache, preAllocatedShares, nil)
	})
}

// PreProcessBlockWithOutputs processes and fills the block data from either pruned or compact modes
func (b *PoolBlock) PreProcessBlockWithOutputs(consensus *Consensus, getTemplateById GetByTemplateIdFunc, calculateOutputs func() (outputs transaction.Outputs, bottomHeight uint64, err error)) (missingBlocks []types.Hash, err error) {

	getTemplateByIdFillingTx := func(h types.Hash) *PoolBlock {
		chain := make(UniquePoolBlockSlice, 0, 1)

		cur := getTemplateById(h)
		for ; cur != nil; cur = getTemplateById(cur.Side.Parent) {
			chain = append(chain, cur)
			if !cur.NeedsCompactTransactionFilling() {
				break
			}
			if len(chain) > 1 {
				prevBlock := chain[len(chain)-2]
				lastBlock := chain[len(chain)-1]
				if prevBlock.FillTransactionsFromTransactionParentIndices(consensus, lastBlock) == nil {
					if !prevBlock.NeedsCompactTransactionFilling() {
						//early abort if it can all be filled
						chain = chain[:len(chain)-1]
						break
					}
				}
			}
		}
		if len(chain) == 0 {
			return nil
		}
		//skips last entry
		for i := len(chain) - 2; i >= 0; i-- {
			if err := chain[i].FillTransactionsFromTransactionParentIndices(consensus, chain[i+1]); err != nil {
				return nil
			}
		}
		return chain[0]
	}

	var parent *PoolBlock
	if b.NeedsCompactTransactionFilling() {
		parent = getTemplateByIdFillingTx(b.Side.Parent)
		if parent == nil {
			missingBlocks = append(missingBlocks, b.Side.Parent)
			return missingBlocks, errors.New("parent does not exist in compact block")
		}
		if err := b.FillTransactionsFromTransactionParentIndices(consensus, parent); err != nil {
			return nil, fmt.Errorf("error filling transactions for block: %w", err)
		}
	}

	if len(b.Main.Transactions) != len(b.Main.TransactionParentIndices) {
		if parent == nil {
			parent = getTemplateByIdFillingTx(b.Side.Parent)
		}
		b.FillTransactionParentIndices(consensus, parent)
	}

	if len(b.Main.Coinbase.Outputs) == 0 {
		if outputs, _, err := calculateOutputs(); err != nil {
			return nil, fmt.Errorf("error filling outputs for block: %w", err)
		} else {
			b.Main.Coinbase.Outputs = outputs
		}

		if outputBlob, err := b.Main.Coinbase.Outputs.AppendBinary(make([]byte, 0, b.Main.Coinbase.Outputs.BufferLength())); err != nil {
			return nil, fmt.Errorf("error filling outputs for block: %s", err)
		} else if uint64(len(outputBlob)) != b.Main.Coinbase.AuxiliaryData.OutputsBlobSize {
			return nil, fmt.Errorf("error filling outputs for block: invalid output blob size, got %d, expected %d", b.Main.Coinbase.AuxiliaryData.OutputsBlobSize, len(outputBlob))
		}
	}

	if b.ShareVersion() >= ShareVersion_V3 && b.Main.Coinbase.AuxiliaryData.TemplateId == types.ZeroHash {
		// Fill template id for pruned broadcasts
		templateId := b.SideTemplateId(consensus)
		b.Main.Coinbase.AuxiliaryData.TemplateId = templateId
	}

	return nil, nil
}

func (b *PoolBlock) NeedsPreProcess() bool {
	return b.NeedsCompactTransactionFilling() || len(b.Main.Coinbase.Outputs) == 0
}

func (b *PoolBlock) NeedsPostProcess() bool {
	// whether it needs to create indices and others
	return b.NeedsPreProcess() || len(b.Main.Transactions) > len(b.Main.TransactionParentIndices)
}

func (b *PoolBlock) FillPrivateKeys(derivationCache DerivationCacheInterface) {
	if b.ShareVersion() >= ShareVersion_V2 {
		if b.Side.CoinbasePrivateKey == crypto.ZeroPrivateKeyBytes {
			//Fill Private Key
			kP := derivationCache.GetDeterministicTransactionKey(b.GetPrivateKeySeed(), b.Main.PreviousId)
			b.Side.CoinbasePrivateKey = kP.PrivateKey.AsBytes()
		}
	} else {
		b.Side.CoinbasePrivateKeySeed = b.GetPrivateKeySeed()
	}
}

func (b *PoolBlock) IsProofHigherThanMainDifficulty(hasher randomx.Hasher, difficultyFunc mainblock.GetDifficultyByHeightFunc, seedFunc mainblock.GetSeedByHeightFunc) bool {
	r, _ := b.IsProofHigherThanMainDifficultyWithError(hasher, difficultyFunc, seedFunc)
	return r
}

var ErrNoMainDifficulty = errors.New("could not get main difficulty")

func (b *PoolBlock) IsProofHigherThanMainDifficultyWithError(hasher randomx.Hasher, difficultyFunc mainblock.GetDifficultyByHeightFunc, seedFunc mainblock.GetSeedByHeightFunc) (bool, error) {
	if mainDifficulty := b.MainDifficulty(difficultyFunc); mainDifficulty == types.ZeroDifficulty {
		return false, ErrNoMainDifficulty
	} else if powHash, err := b.PowHashWithError(hasher, seedFunc); err != nil {
		return false, err
	} else {
		return mainDifficulty.CheckPoW(powHash), nil
	}
}

func (b *PoolBlock) IsProofHigherThanDifficulty(hasher randomx.Hasher, f mainblock.GetSeedByHeightFunc) bool {
	r, _ := b.IsProofHigherThanDifficultyWithError(hasher, f)
	return r
}

func (b *PoolBlock) IsProofHigherThanDifficultyWithError(hasher randomx.Hasher, f mainblock.GetSeedByHeightFunc) (bool, error) {
	if powHash, err := b.PowHashWithError(hasher, f); err != nil {
		return false, err
	} else {
		return b.Side.Difficulty.CheckPoW(powHash), nil
	}
}

func (b *PoolBlock) GetPrivateKeySeed() types.Hash {
	if b.ShareVersion() >= ShareVersion_V2 {
		return b.Side.CoinbasePrivateKeySeed
	}

	oldSeed := types.Hash(b.Side.PublicKey[address.PackedAddressSpend])
	if b.Main.MajorVersion < monero.HardForkViewTagsVersion && p2poolcrypto.GetDeterministicTransactionPrivateKey(oldSeed, b.Main.PreviousId).AsBytes() != b.Side.CoinbasePrivateKey {
		return types.ZeroHash
	}

	return oldSeed
}

func (b *PoolBlock) CalculateTransactionPrivateKeySeed() types.Hash {
	if b.ShareVersion() >= ShareVersion_V2 {
		preAllocatedMainData := make([]byte, 0, b.Main.BufferLength())
		preAllocatedSideData := make([]byte, 0, b.Side.BufferLength(b.ShareVersion()))
		mainData, _ := b.Main.SideChainHashingBlob(preAllocatedMainData, false)
		sideData, _ := b.Side.AppendBinary(preAllocatedSideData, b.ShareVersion())
		return p2poolcrypto.CalculateTransactionPrivateKeySeed(
			mainData,
			sideData,
		)
	}

	return types.Hash(b.Side.PublicKey[address.PackedAddressSpend])
}

func (b *PoolBlock) GetAddress() address.PackedAddress {
	return b.Side.PublicKey
}

// GetPayoutAddress Special function that checks if a subaddress has been specified, on the right network
func (b *PoolBlock) GetPayoutAddress(networkType NetworkType) *address.Address {
	if d, ok := b.Side.MergeMiningExtra.Get(ExtraChainKeySubaddressViewPub); ok && len(d) >= crypto.PublicKeySize {
		// subaddress
		viewPub := crypto.PublicKeyBytes(d[:crypto.PublicKeySize])
		if n, err := networkType.SubaddressNetwork(); err == nil && viewPub.AsPoint() != nil {
			return address.FromRawAddress(n, b.Side.PublicKey.SpendPublicKey(), &viewPub)
		}
	}
	if n, err := networkType.AddressNetwork(); err == nil {
		return b.Side.PublicKey.ToAddress(n)
	}

	return nil
}

func (b *PoolBlock) GetTransactionOutputType() uint8 {
	// Both tx types are allowed by Monero consensus during v15 because it needs to process pre-fork mempool transactions,
	// but P2Pool can switch to using only TXOUT_TO_TAGGED_KEY for miner payouts starting from v15
	expectedTxType := uint8(transaction.TxOutToKey)
	if b.Main.MajorVersion >= monero.HardForkViewTagsVersion {
		expectedTxType = transaction.TxOutToTaggedKey
	}

	return expectedTxType
}

type poolBlockCache struct {
	templateId atomic.Pointer[types.Hash]
	coinbaseId atomic.Pointer[types.Hash]
	powHash    atomic.Pointer[types.Hash]
}
