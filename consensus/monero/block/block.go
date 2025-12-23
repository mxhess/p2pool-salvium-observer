package block

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"git.gammaspectra.live/P2Pool/consensus/v4/monero"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/crypto"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/randomx"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/transaction"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
)

const MaxTransactionCount = uint64(math.MaxUint64) / types.HashSize

// readProtocolTransactionHash reads the Salvium Carrot v1 protocol transaction and returns its hash
// Protocol TX structure: version(4) + unlock_time(60) + vin(1 txin_gen + height) +
// vout(0) + extra(2 bytes: 0x02 0x00) + type(2=PROTOCOL) + rct(0)
func readProtocolTransactionHash(reader utils.ReaderAndByteReader) (types.Hash, error) {
	var hash types.Hash
	txBuf := bytes.NewBuffer(make([]byte, 0, 64))

	// Read version
	version, err := utils.ReadCanonicalUvarint(reader)
	if err != nil {
		return hash, err
	}
	txBuf.Write(binary.AppendUvarint(nil, version))

	if version != 4 {
		return hash, fmt.Errorf("unexpected protocol TX version: %d", version)
	}

	// Read unlock_time
	unlockTime, err := utils.ReadCanonicalUvarint(reader)
	if err != nil {
		return hash, err
	}
	txBuf.Write(binary.AppendUvarint(nil, unlockTime))

	if unlockTime != 60 {
		return hash, fmt.Errorf("unexpected protocol TX unlock time: %d", unlockTime)
	}

	// Read vin_size
	vinSize, err := utils.ReadCanonicalUvarint(reader)
	if err != nil {
		return hash, err
	}
	txBuf.Write(binary.AppendUvarint(nil, vinSize))

	if vinSize != 1 {
		return hash, fmt.Errorf("unexpected protocol TX vin size: %d", vinSize)
	}

	// Read txin_type
	txinType, err := reader.ReadByte()
	if err != nil {
		return hash, err
	}
	txBuf.WriteByte(txinType)

	if txinType != transaction.TxInGen {
		return hash, fmt.Errorf("unexpected protocol TX input type: %d", txinType)
	}

	// Read height
	height, err := utils.ReadCanonicalUvarint(reader)
	if err != nil {
		return hash, err
	}
	txBuf.Write(binary.AppendUvarint(nil, height))

	// Read vout_size
	voutSize, err := utils.ReadCanonicalUvarint(reader)
	if err != nil {
		return hash, err
	}
	txBuf.Write(binary.AppendUvarint(nil, voutSize))

	if voutSize != 0 {
		return hash, fmt.Errorf("unexpected protocol TX vout size: %d", voutSize)
	}

	// Read extra_size
	extraSize, err := utils.ReadCanonicalUvarint(reader)
	if err != nil {
		return hash, err
	}
	txBuf.Write(binary.AppendUvarint(nil, extraSize))

	// Read extra bytes
	extraBuf := make([]byte, extraSize)
	if _, err = io.ReadFull(reader, extraBuf); err != nil {
		return hash, err
	}
	txBuf.Write(extraBuf)

	// Read type
	txType, err := utils.ReadCanonicalUvarint(reader)
	if err != nil {
		return hash, err
	}
	txBuf.Write(binary.AppendUvarint(nil, txType))

	// Read rct_type
	rctType, err := reader.ReadByte()
	if err != nil {
		return hash, err
	}
	txBuf.WriteByte(rctType)

	if rctType != 0 {
		return hash, fmt.Errorf("unexpected protocol TX rct type: %d", rctType)
	}

	// Calculate hash of the protocol transaction
	// The hash is calculated the same way as regular transactions
	txBytes := txBuf.Bytes()

	hasher := crypto.GetKeccak256Hasher()
	defer crypto.PutKeccak256Hasher(hasher)

	// Protocol tx hash: coinbase id, base RCT hash, prunable RCT hash
	var txHashingBlob [3 * types.HashSize]byte

	// Hash transaction bytes (without rct_type)
	_, _ = hasher.Write(txBytes[:len(txBytes)-1])
	crypto.HashFastSum(hasher, txHashingBlob[:])

	// Base RCT (single 0 byte)
	baseRCTZeroHash := crypto.PooledKeccak256([]byte{0})
	copy(txHashingBlob[1*types.HashSize:], baseRCTZeroHash[:])

	// Prunable RCT is empty (already zeroed)

	hasher.Reset()
	_, _ = hasher.Write(txHashingBlob[:])
	crypto.HashFastSum(hasher, hash[:])

	utils.Logf("P2Pool/ProtocolTx", "Calculated protocol TX hash: %x (height: %d)", hash[:], height)

	return hash, nil
}

type Block struct {
	MajorVersion uint8 `json:"major_version"`
	MinorVersion uint8 `json:"minor_version"`
	// Nonce re-arranged here to improve memory layout space
	Nonce uint32 `json:"nonce"`

	Timestamp     uint64        `json:"timestamp"`
	PreviousId    types.Hash    `json:"previous_id"`
	PricingRecord PricingRecord `json:"pricing_record,omitempty"` // Salvium: Oracle pricing data
	//Nonce would be here

	Coinbase   transaction.CoinbaseTransaction  `json:"coinbase"`
	ProtocolTx *transaction.CoinbaseTransaction `json:"protocol_tx,omitempty"` // Salvium: Protocol transaction

	// ProtocolTxHash is the hash of the protocol transaction (Carrot v1 only)
	ProtocolTxHash types.Hash `json:"protocol_tx_hash,omitempty"`

	Transactions []types.Hash `json:"transactions,omitempty"`
	// TransactionParentIndices amount of reward existing Outputs. Used by p2pool serialized compact broadcasted blocks in protocol >= 1.1, filled only in compact blocks or by pre-processing.
	TransactionParentIndices []uint64 `json:"transaction_parent_indices,omitempty"`
}

type Header struct {
	MajorVersion uint8 `json:"major_version"`
	MinorVersion uint8 `json:"minor_version"`
	// Nonce re-arranged here to improve memory layout space
	Nonce uint32 `json:"nonce"`

	Timestamp     uint64        `json:"timestamp"`
	PreviousId    types.Hash    `json:"previous_id"`
	PricingRecord PricingRecord `json:"pricing_record,omitempty"` // Salvium: Oracle pricing data
	Height        uint64        `json:"height"`
	//Nonce would be here
	Reward     uint64           `json:"reward"`
	Difficulty types.Difficulty `json:"difficulty"`
	Id         types.Hash       `json:"id"`
}

func (b *Block) MarshalBinary() (buf []byte, err error) {
	return b.MarshalBinaryFlags(false, false, false)
}

func (b *Block) BufferLength() int {
	length := utils.UVarInt64Size(b.MajorVersion) +
		utils.UVarInt64Size(b.MinorVersion) +
		utils.UVarInt64Size(b.Timestamp) +
		types.HashSize +
		4 // nonce

	// Add pricing record if oracle enabled (Salvium)
	if b.MajorVersion >= HFVersionEnableOracle && !b.PricingRecord.Empty() {
		length += b.PricingRecord.BufferLength()
	}

	length += b.Coinbase.BufferLength()

	// Add protocol tx if present (Salvium)
	if b.ProtocolTx != nil {
		length += b.ProtocolTx.BufferLength()
	}

	length += utils.UVarInt64Size(len(b.Transactions)) + types.HashSize*len(b.Transactions)
	return length
}

func (b *Block) MarshalBinaryFlags(compact, pruned, containsAuxiliaryTemplateId bool) (buf []byte, err error) {
	return b.AppendBinaryFlags(make([]byte, 0, b.BufferLength()), pruned, compact, containsAuxiliaryTemplateId)
}

func (b *Block) AppendBinaryFlags(preAllocatedBuf []byte, compact, pruned, containsAuxiliaryTemplateId bool) (buf []byte, err error) {
	buf = preAllocatedBuf
	if b.MajorVersion > monero.HardForkSupportedVersion {
		return nil, fmt.Errorf("unsupported version %d", b.MajorVersion)
	}
	if b.MinorVersion < b.MajorVersion {
		return nil, fmt.Errorf("minor version %d smaller than major %d", b.MinorVersion, b.MajorVersion)
	}
	buf = binary.AppendUvarint(buf, uint64(b.MajorVersion))
	buf = binary.AppendUvarint(buf, uint64(b.MinorVersion))
	buf = binary.AppendUvarint(buf, b.Timestamp)
	buf = append(buf, b.PreviousId[:]...)
	buf = binary.LittleEndian.AppendUint32(buf, b.Nonce)
	
	// Salvium: Serialize pricing record if oracle enabled
	if b.MajorVersion >= HFVersionEnableOracle && !b.PricingRecord.Empty() {
		var pricingData []byte
		pricingData, err = b.PricingRecord.MarshalBinary()
		if err != nil {
			return nil, err
		}
		buf = append(buf, pricingData...)
	}
	
	if buf, err = b.Coinbase.AppendBinaryFlags(buf, pruned, containsAuxiliaryTemplateId); err != nil {
		return nil, err
	}
	
	// Salvium: Serialize protocol tx if present
	if b.ProtocolTx != nil {
		if buf, err = b.ProtocolTx.AppendBinaryFlags(buf, pruned, containsAuxiliaryTemplateId); err != nil {
			return nil, err
		}
	}
	
	buf = binary.AppendUvarint(buf, uint64(len(b.Transactions)))
	if compact {
		for i, txId := range b.Transactions {
			if i < len(b.TransactionParentIndices) && b.TransactionParentIndices[i] != 0 {
				buf = binary.AppendUvarint(buf, b.TransactionParentIndices[i])
			} else {
				buf = binary.AppendUvarint(buf, 0)
				buf = append(buf, txId[:]...)
			}
		}
	} else {
		for _, txId := range b.Transactions {
			buf = append(buf, txId[:]...)
		}
	}
	return buf, nil
}

type PrunedFlagsFunc func() (containsAuxiliaryTemplateId bool)

func (b *Block) FromReader(reader utils.ReaderAndByteReader, canBePruned bool, f PrunedFlagsFunc) (err error) {
	return b.FromReaderFlags(reader, false, canBePruned, f)
}

func (b *Block) FromCompactReader(reader utils.ReaderAndByteReader, canBePruned bool, f PrunedFlagsFunc) (err error) {
	return b.FromReaderFlags(reader, true, canBePruned, f)
}

func (b *Block) UnmarshalBinary(data []byte, canBePruned bool, f PrunedFlagsFunc) error {
	reader := bytes.NewReader(data)
	err := b.FromReader(reader, canBePruned, f)
	if err != nil {
		return err
	}
	if reader.Len() > 0 {
		return errors.New("leftover bytes in reader")
	}
	return nil
}

func (b *Block) FromReaderFlags(reader utils.ReaderAndByteReader, compact, canBePruned bool, f PrunedFlagsFunc) (err error) {
	var (
		txCount         uint64
		transactionHash types.Hash
	)

	if b.MajorVersion, err = reader.ReadByte(); err != nil {
		return err
	}

	if b.MajorVersion > monero.HardForkSupportedVersion {
		return fmt.Errorf("unsupported version %d", b.MajorVersion)
	}

	if b.MinorVersion, err = reader.ReadByte(); err != nil {
		return err
	}

	if b.MinorVersion < b.MajorVersion {
		return fmt.Errorf("minor version %d smaller than major version %d", b.MinorVersion, b.MajorVersion)
	}

	if b.MinorVersion > 127 {
		return fmt.Errorf("minor version %d larger than maximum byte varint size", b.MinorVersion)
	}

	if b.Timestamp, err = utils.ReadCanonicalUvarint(reader); err != nil {
		return err
	}

	if _, err = io.ReadFull(reader, b.PreviousId[:]); err != nil {
		return err
	}

	if err = binary.Read(reader, binary.LittleEndian, &b.Nonce); err != nil {
		return err
	}

	var containsAuxiliaryTemplateId bool

	if canBePruned && f != nil {
		containsAuxiliaryTemplateId = f()
	}

	// Coinbase Tx Decoding
	{
		if err = b.Coinbase.FromReader(reader, canBePruned, containsAuxiliaryTemplateId); err != nil {
			return err
		}
	}

	// Salvium Carrot v1+: Read Protocol TX hash if present (major_version >= 10)
	if b.MajorVersion >= 10 {
		if b.ProtocolTxHash, err = readProtocolTransactionHash(reader); err != nil {
			return err
		}
		utils.Logf("P2Pool/Deserialize", "Read protocol TX hash for MajorVersion %d", b.MajorVersion)
	}

	//TODO: verify hardfork major versions

	if txCount, err = utils.ReadCanonicalUvarint(reader); err != nil {
		return err
	} else if txCount > MaxTransactionCount {
		return fmt.Errorf("transaction count count too large: %d > %d", txCount, MaxTransactionCount)
	} else if txCount > 0 {
		if compact {
			// preallocate with soft cap
			b.Transactions = make([]types.Hash, 0, min(8192, txCount))
			b.TransactionParentIndices = make([]uint64, 0, min(8192, txCount))

			var parentIndex uint64
			for i := 0; i < int(txCount); i++ {
				if parentIndex, err = utils.ReadCanonicalUvarint(reader); err != nil {
					return err
				}

				if parentIndex == 0 {
					//not in lookup
					if _, err = io.ReadFull(reader, transactionHash[:]); err != nil {
						return err
					}

					b.Transactions = append(b.Transactions, transactionHash)
				} else {
					b.Transactions = append(b.Transactions, types.ZeroHash)
				}

				b.TransactionParentIndices = append(b.TransactionParentIndices, parentIndex)
			}
		} else {
			// preallocate with soft cap
			b.Transactions = make([]types.Hash, 0, min(8192, txCount))

			for i := 0; i < int(txCount); i++ {
				if _, err = io.ReadFull(reader, transactionHash[:]); err != nil {
					return err
				}
				b.Transactions = append(b.Transactions, transactionHash)
			}
		}
	}

	// Debug: log transaction count and first hash if present
	if b.MajorVersion >= 10 && len(b.Transactions) > 0 {
		utils.Logf("P2Pool/Deserialize", "Deserialized %d transaction hashes, first: %x", len(b.Transactions), b.Transactions[0][:])
	}

	return nil
}

func (b *Block) Header() *Header {
	return &Header{
		MajorVersion: b.MajorVersion,
		MinorVersion: b.MinorVersion,
		Timestamp:    b.Timestamp,
		PreviousId:   b.PreviousId,
		Height:       b.Coinbase.GenHeight,
		Nonce:        b.Nonce,
		Reward:       b.Coinbase.AuxiliaryData.TotalReward,
		Id:           b.Id(),
		Difficulty:   types.ZeroDifficulty,
	}
}

func (b *Block) HeaderBlobBufferLength() int {
	return 1 + 1 +
		utils.UVarInt64Size(b.Timestamp) +
		types.HashSize +
		4
}

func (b *Block) HeaderBlob(preAllocatedBuf []byte) []byte {
	buf := preAllocatedBuf
	buf = append(buf, b.MajorVersion)
	buf = append(buf, b.MinorVersion)
	buf = binary.AppendUvarint(buf, b.Timestamp)
	buf = append(buf, b.PreviousId[:]...)
	buf = binary.LittleEndian.AppendUint32(buf, b.Nonce)

	return buf
}

// SideChainHashingBlob Same as MarshalBinary but with nonce or template id set to 0
func (b *Block) SideChainHashingBlob(preAllocatedBuf []byte, zeroTemplateId bool) (buf []byte, err error) {
	buf = preAllocatedBuf
	buf = append(buf, b.MajorVersion)
	buf = append(buf, b.MinorVersion)
	buf = binary.AppendUvarint(buf, b.Timestamp)
	buf = append(buf, b.PreviousId[:]...)
	buf = binary.LittleEndian.AppendUint32(buf, 0) //replaced

	if buf, err = b.Coinbase.SideChainHashingBlob(buf, zeroTemplateId); err != nil {
		return nil, err
	}

	// Serialize protocol transaction for Carrot v1 (matches pool_block.cpp:295-321)
	if b.MajorVersion >= 10 {
		// version = 4 (TRANSACTION_VERSION_CARROT)
		buf = binary.AppendUvarint(buf, 4)
		// unlock_time = 60
		buf = binary.AppendUvarint(buf, 60)
		// vin.size() = 1
		buf = binary.AppendUvarint(buf, 1)
		// TXIN_GEN
		buf = append(buf, transaction.TxInGen)
		// height
		buf = binary.AppendUvarint(buf, b.Coinbase.GenHeight)
		// vout.size() = 0
		buf = binary.AppendUvarint(buf, 0)
		// extra.size() = 2
		buf = binary.AppendUvarint(buf, 2)
		// extra data: 0x02 0x00
		buf = append(buf, 0x02, 0x00)
		// type = PROTOCOL (2)
		buf = binary.AppendUvarint(buf, 2)
		// RCT type = 0
		buf = append(buf, 0)
	}

	// Write transaction count and hashes (excluding coinbase)
	buf = binary.AppendUvarint(buf, uint64(len(b.Transactions)))
	for _, txId := range b.Transactions {
		buf = append(buf, txId[:]...)
	}

	return buf, nil
}

func (b *Block) HashingBlobBufferLength() int {
	return b.HeaderBlobBufferLength() +
		types.HashSize + utils.UVarInt64Size(len(b.Transactions)+1)
}

func (b *Block) HashingBlob(preAllocatedBuf []byte) []byte {
	buf := b.HeaderBlob(preAllocatedBuf)

	merkleTree := make(crypto.BinaryTreeHash, len(b.Transactions)+1)
	//TODO: cache?
	merkleTree[0] = b.Coinbase.CalculateId()
	copy(merkleTree[1:], b.Transactions)
	txTreeHash := merkleTree.RootHash()
	buf = append(buf, txTreeHash[:]...)

	buf = binary.AppendUvarint(buf, uint64(len(b.Transactions)+1))

	return buf
}

func (b *Block) Difficulty(f GetDifficultyByHeightFunc) types.Difficulty {
	//cached by sidechain.Share
	return f(b.Coinbase.GenHeight)
}

var ErrNoSeed = errors.New("could not get seed")

func (b *Block) PowHashWithError(hasher randomx.Hasher, f GetSeedByHeightFunc) (types.Hash, error) {
	//not cached
	if seed := f(b.Coinbase.GenHeight); seed == types.ZeroHash {
		return types.ZeroHash, ErrNoSeed
	} else {
		return hasher.Hash(seed[:], b.HashingBlob(make([]byte, 0, b.HashingBlobBufferLength())))
	}
}

func (b *Block) Id() types.Hash {
	//cached by sidechain.Share
	var varIntBuf [binary.MaxVarintLen64]byte
	buf := b.HashingBlob(make([]byte, 0, b.HashingBlobBufferLength()))
	return crypto.PooledKeccak256(varIntBuf[:binary.PutUvarint(varIntBuf[:], uint64(len(buf)))], buf)
}

var (
	ErrInvalidVarint = errors.New("invalid varint")
	ErrInvalidLength = errors.New("invalid length")
)
