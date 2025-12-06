package transaction

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"git.gammaspectra.live/P2Pool/consensus/v4/monero"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/crypto"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
)

type CoinbaseTransaction struct {
	Version uint8 `json:"version"`
	// UnlockTime would be here
	InputCount uint8 `json:"input_count"`
	InputType  uint8 `json:"input_type"`
	// UnlockTime re-arranged here to improve memory layout space
	UnlockTime uint64  `json:"unlock_time"`
	GenHeight  uint64  `json:"gen_height"`
	Outputs    Outputs `json:"outputs"`

	Extra ExtraTags `json:"extra"`

	ExtraBaseRCT uint8 `json:"extra_base_rct"`

	// AuxiliaryData Used by p2pool serialized pruned blocks
	AuxiliaryData CoinbaseTransactionAuxiliaryData `json:"auxiliary_data"`
}

type CoinbaseTransactionAuxiliaryData struct {
	// OutputsBlobSize length of serialized Outputs. Used by p2pool serialized pruned blocks, filled regardless
	OutputsBlobSize uint64 `json:"outputs_blob_size"`
	// TotalReward amount of reward existing Outputs. Used by p2pool serialized pruned blocks, filled regardless
	TotalReward uint64 `json:"total_reward"`
	// TemplateId Required by sidechain.GetOutputs to speed up repeated broadcasts from different peers
	// This must be filled when preprocessing
	TemplateId types.Hash `json:"template_id,omitempty"`
}

func (c *CoinbaseTransaction) UnmarshalBinary(data []byte, canBePruned, containsAuxiliaryTemplateId bool) error {
	reader := bytes.NewReader(data)
	err := c.FromReader(reader, canBePruned, containsAuxiliaryTemplateId)
	if err != nil {
		return err
	}
	if reader.Len() > 0 {
		return errors.New("leftover bytes in reader")
	}
	return nil
}

var ErrInvalidTransactionExtra = errors.New("invalid transaction extra")

func (c *CoinbaseTransaction) FromReader(reader utils.ReaderAndByteReader, canBePruned, containsAuxiliaryTemplateId bool) (err error) {
	var (
		txExtraSize uint64
	)

	c.AuxiliaryData.TotalReward = 0
	c.AuxiliaryData.OutputsBlobSize = 0

	if c.Version, err = reader.ReadByte(); err != nil {
		return err
	}

	if c.Version != 2 {
		return errors.New("version not supported")
	}

	if c.UnlockTime, err = utils.ReadCanonicalUvarint(reader); err != nil {
		return err
	}

	if c.InputCount, err = reader.ReadByte(); err != nil {
		return err
	}

	if c.InputCount != 1 {
		return errors.New("invalid input count")
	}

	if c.InputType, err = reader.ReadByte(); err != nil {
		return err
	}

	if c.InputType != TxInGen {
		return errors.New("invalid coinbase input type")
	}

	if c.GenHeight, err = utils.ReadCanonicalUvarint(reader); err != nil {
		return err
	}

	if c.UnlockTime != (c.GenHeight + monero.MinerRewardUnlockTime) {
		return errors.New("invalid unlock time")
	}

	if err = c.Outputs.FromReader(reader); err != nil {
		return err
	} else if len(c.Outputs) != 0 {
		for _, o := range c.Outputs {
			switch o.Type {
			case TxOutToTaggedKey:
				c.AuxiliaryData.OutputsBlobSize += 1 + types.HashSize + 1
			case TxOutToKey:
				c.AuxiliaryData.OutputsBlobSize += 1 + types.HashSize
			default:
				return fmt.Errorf("unknown %d TXOUT key", o.Type)
			}
			c.AuxiliaryData.TotalReward += o.Reward
		}
	} else {
		if !canBePruned {
			return errors.New("pruned outputs not supported")
		}

		// Outputs are not in the buffer and must be calculated from sidechain data
		// We only have total reward and outputs blob size here
		//special case, pruned block. outputs have to be generated from chain

		if c.AuxiliaryData.TotalReward, err = utils.ReadCanonicalUvarint(reader); err != nil {
			return err
		}

		if c.AuxiliaryData.OutputsBlobSize, err = utils.ReadCanonicalUvarint(reader); err != nil {
			return err
		}

		if containsAuxiliaryTemplateId {
			// Required by sidechain.get_outputs_blob() to speed up repeated broadcasts from different peers
			if _, err = io.ReadFull(reader, c.AuxiliaryData.TemplateId[:]); err != nil {
				return err
			}
		}
	}

	if txExtraSize, err = utils.ReadCanonicalUvarint(reader); err != nil {
		return err
	}

	limitReader := utils.LimitByteReader(reader, int64(txExtraSize))
	if err = c.Extra.FromReader(limitReader); err != nil {
		return errors.Join(ErrInvalidTransactionExtra, err)
	}
	if limitReader.Left() > 0 {
		return errors.New("bytes leftover in extra data")
	}
	if err = binary.Read(reader, binary.LittleEndian, &c.ExtraBaseRCT); err != nil {
		return err
	}

	if c.ExtraBaseRCT != 0 {
		return errors.New("invalid extra base RCT")
	}

	return nil
}

func (c *CoinbaseTransaction) MarshalBinary() ([]byte, error) {
	return c.MarshalBinaryFlags(false, false)
}

func (c *CoinbaseTransaction) BufferLength() int {
	return 1 +
		utils.UVarInt64Size(c.UnlockTime) +
		1 + 1 +
		utils.UVarInt64Size(c.GenHeight) +
		c.Outputs.BufferLength() +
		utils.UVarInt64Size(c.Extra.BufferLength()) + c.Extra.BufferLength() + 1
}

func (c *CoinbaseTransaction) MarshalBinaryFlags(pruned, containsAuxiliaryTemplateId bool) ([]byte, error) {
	return c.AppendBinaryFlags(make([]byte, 0, c.BufferLength()), pruned, containsAuxiliaryTemplateId)
}

func (c *CoinbaseTransaction) AppendBinaryFlags(preAllocatedBuf []byte, pruned, containsAuxiliaryTemplateId bool) ([]byte, error) {
	buf := preAllocatedBuf

	buf = append(buf, c.Version)
	buf = binary.AppendUvarint(buf, c.UnlockTime)
	buf = append(buf, c.InputCount)
	buf = append(buf, c.InputType)
	buf = binary.AppendUvarint(buf, c.GenHeight)

	if pruned {
		//pruned output
		buf = binary.AppendUvarint(buf, 0)
		buf = binary.AppendUvarint(buf, c.AuxiliaryData.TotalReward)
		outputs := make([]byte, 0, c.Outputs.BufferLength())
		outputs, _ = c.Outputs.AppendBinary(outputs)
		buf = binary.AppendUvarint(buf, uint64(len(outputs)))

		if containsAuxiliaryTemplateId {
			buf = append(buf, c.AuxiliaryData.TemplateId[:]...)
		}
	} else {
		buf, _ = c.Outputs.AppendBinary(buf)
	}

	buf = binary.AppendUvarint(buf, uint64(c.Extra.BufferLength()))
	buf, _ = c.Extra.AppendBinary(buf)
	buf = append(buf, c.ExtraBaseRCT)

	return buf, nil
}

func (c *CoinbaseTransaction) OutputsBlob() ([]byte, error) {
	return c.Outputs.MarshalBinary()
}

func (c *CoinbaseTransaction) SideChainHashingBlob(preAllocatedBuf []byte, zeroTemplateId bool) ([]byte, error) {
	buf := preAllocatedBuf

	buf = append(buf, c.Version)
	buf = binary.AppendUvarint(buf, c.UnlockTime)
	buf = append(buf, c.InputCount)
	buf = append(buf, c.InputType)
	buf = binary.AppendUvarint(buf, c.GenHeight)

	buf, _ = c.Outputs.AppendBinary(buf)

	buf = binary.AppendUvarint(buf, uint64(c.Extra.BufferLength()))
	buf, _ = c.Extra.SideChainHashingBlob(buf, zeroTemplateId)
	buf = append(buf, c.ExtraBaseRCT)

	return buf, nil
}

var baseRCTZeroHash = crypto.PooledKeccak256([]byte{0})

func (c *CoinbaseTransaction) CalculateId() (hash types.Hash) {

	txBytes, _ := c.AppendBinaryFlags(make([]byte, 0, c.BufferLength()), false, false)

	hasher := crypto.GetKeccak256Hasher()
	defer crypto.PutKeccak256Hasher(hasher)

	// coinbase id, base RCT hash, prunable RCT hash
	var txHashingBlob [3 * types.HashSize]byte

	// remove base RCT
	_, _ = hasher.Write(txBytes[:len(txBytes)-1])
	crypto.HashFastSum(hasher, txHashingBlob[:])

	if c.ExtraBaseRCT == 0 {
		// Base RCT, single 0 byte in miner tx
		copy(txHashingBlob[1*types.HashSize:], baseRCTZeroHash[:])
	} else {
		// fallback, but should never be hit
		hasher.Reset()
		_, _ = hasher.Write([]byte{c.ExtraBaseRCT})
		crypto.HashFastSum(hasher, txHashingBlob[1*types.HashSize:])
	}

	// Prunable RCT, empty in miner tx
	//copy(txHashingBlob[2*types.HashSize:], types.ZeroHash[:])

	hasher.Reset()
	_, _ = hasher.Write(txHashingBlob[:])
	crypto.HashFastSum(hasher, hash[:])

	return hash
}
