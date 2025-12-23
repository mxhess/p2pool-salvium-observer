// Package p2pool provides types for parsing p2pool-salvium block data.
// Based directly on ~/p2pool-salvium/src/ C++ implementation.
package p2pool

import (
	"encoding/binary"
	"fmt"
)

// ParseCacheEntry parses a block from Redis cache format.
// Format (from modified block_cache.cpp):
//   - 32 bytes: sidechain_id (prepended by our modified C++)
//   - 4 bytes: total_length (LE uint32)
//   - mainchain_data
//   - sidechain_data
//
// Reference: ~/p2pool-salvium/src/block_cache.cpp store() and load()
func ParseCacheEntry(data []byte) (*Share, error) {
	if len(data) < 36 { // 32 bytes sidechain_id + 4 bytes length
		return nil, fmt.Errorf("cache entry too short: %d bytes", len(data))
	}

	share := &Share{}

	// Read prepended sidechain_id (32 bytes)
	copy(share.SidechainId[:], data[0:32])

	// Read total_length (4 bytes LE)
	totalLen := binary.LittleEndian.Uint32(data[32:36])
	expectedEnd := 36 + int(totalLen)

	if len(data) < expectedEnd {
		return nil, fmt.Errorf("cache entry truncated: have %d, need %d", len(data), expectedEnd)
	}

	// Parse mainchain + sidechain data starting at offset 36
	blockData := data[36:expectedEnd]

	// Validate first byte is a reasonable majorVersion (10 for Salvium Carrot)
	if len(blockData) < 2 || blockData[0] < 1 || blockData[0] > 20 {
		return nil, fmt.Errorf("invalid majorVersion: 0x%02x (expected 1-20)", blockData[0])
	}

	// Parse mainchain header to get MainchainHeight and basic fields
	offset, err := parseMainchainData(blockData, share)
	if err != nil {
		return nil, fmt.Errorf("parsing mainchain data: %w", err)
	}

	// Validate offset is reasonable
	if offset < 0 || offset >= len(blockData) {
		return nil, fmt.Errorf("mainchain parsing returned invalid offset %d (data len %d)", offset, len(blockData))
	}

	// Parse sidechain data
	err = parseSidechainData(blockData[offset:], share)
	if err != nil {
		return nil, fmt.Errorf("parsing sidechain data: %w", err)
	}

	return share, nil
}

// parseMainchainData parses the mainchain portion of the block.
// Returns the offset where sidechain data begins.
//
// Reference: ~/p2pool-salvium/src/pool_block_parser.inl deserialize()
// Format:
//   - majorVersion (1 byte)
//   - minorVersion (1 byte)
//   - timestamp (varint)
//   - prevId (32 bytes)
//   - nonce (4 bytes)
//   - TX_VERSION (1 byte, expect 4)
//   - unlock_height (varint)
//   - 1 (1 byte)
//   - TXIN_GEN (1 byte, 0xff)
//   - txinGenHeight (varint) <- this is MainchainHeight
//   - ... outputs, tx_extra, transactions ...
//   - sidechain data starts after mainchain
func parseMainchainData(data []byte, share *Share) (int, error) {
	if len(data) < 40 { // minimum: 1+1+1+32+4+1+1+1+1+1 = 44
		return 0, fmt.Errorf("mainchain data too short")
	}

	offset := 0

	// majorVersion (1 byte)
	share.MajorVersion = data[offset]
	offset++

	// minorVersion (1 byte)
	share.MinorVersion = data[offset]
	offset++

	// timestamp (varint)
	var err error
	share.Timestamp, offset, err = ReadVarint(data, offset)
	if err != nil {
		return 0, fmt.Errorf("reading timestamp: %w", err)
	}

	// prevId (32 bytes)
	share.PrevId, offset, err = ReadHash(data, offset)
	if err != nil {
		return 0, fmt.Errorf("reading prevId: %w", err)
	}

	// nonce (4 bytes LE)
	share.Nonce, offset, err = ReadUint32LE(data, offset)
	if err != nil {
		return 0, fmt.Errorf("reading nonce: %w", err)
	}

	// TX_VERSION (1 byte, expect 4)
	txVersion, offset, err := ReadByte(data, offset)
	if err != nil {
		return 0, fmt.Errorf("reading tx version: %w", err)
	}
	if txVersion != 4 {
		return 0, fmt.Errorf("unexpected tx version: %d (expected 4)", txVersion)
	}

	// unlock_height (varint) - skip
	_, offset, err = ReadVarint(data, offset)
	if err != nil {
		return 0, fmt.Errorf("reading unlock_height: %w", err)
	}

	// Expect 1 (input count)
	inputCount, offset, err := ReadByte(data, offset)
	if err != nil {
		return 0, fmt.Errorf("reading input count: %w", err)
	}
	if inputCount != 1 {
		return 0, fmt.Errorf("unexpected input count: %d (expected 1)", inputCount)
	}

	// Expect TXIN_GEN (0xff)
	txinType, offset, err := ReadByte(data, offset)
	if err != nil {
		return 0, fmt.Errorf("reading txin type: %w", err)
	}
	if txinType != 0xff {
		return 0, fmt.Errorf("unexpected txin type: 0x%02x (expected 0xff)", txinType)
	}

	// txinGenHeight - this is the mainchain height!
	share.MainchainHeight, offset, err = ReadVarint(data, offset)
	if err != nil {
		return 0, fmt.Errorf("reading txinGenHeight: %w", err)
	}

	// Now we need to skip the rest of mainchain data to find where sidechain data starts.
	// This is complex because it includes variable-length outputs, tx_extra, transactions.
	// However, in the cache format, we can identify sidechain data by looking for
	// the wallet public keys which start the sidechain portion.

	// For now, we need to skip:
	// - num_outputs (varint)
	// - for each output: reward (varint) + output_type (1) + K_o (32) + asset (5) + view_tag (3) + anchor (16)
	// - or if compact: total_reward (varint) + outputs_blob_size (varint) + sidechainId (32)
	// - tx_extra_size (varint) + tx_extra data
	// - tx_type (varint) + amount_burnt (varint) + rct_type (1)
	// - optional protocol_tx
	// - num_transactions (varint) + transaction hashes

	// Skip outputs
	numOutputs, offset, err := ReadVarint(data, offset)
	if err != nil {
		return 0, fmt.Errorf("reading num_outputs: %w", err)
	}

	if numOutputs > 0 {
		// Full format: each Carrot v1 output is:
		// reward (varint) + TXOUT_TO_CARROT_V1 (1) + K_o (32) + asset_len (1) + "SAL1" (4) + view_tag (3) + anchor (16)
		// = minimum ~58 bytes per output
		for i := uint64(0); i < numOutputs; i++ {
			// reward (varint)
			_, offset, err = ReadVarint(data, offset)
			if err != nil {
				return 0, fmt.Errorf("reading output %d reward: %w", i, err)
			}
			// TXOUT_TO_CARROT_V1 (1 byte, 0x04)
			offset, err = SkipBytes(data, offset, 1)
			if err != nil {
				return 0, fmt.Errorf("skipping output %d type: %w", i, err)
			}
			// K_o (32 bytes)
			offset, err = SkipBytes(data, offset, 32)
			if err != nil {
				return 0, fmt.Errorf("skipping output %d K_o: %w", i, err)
			}
			// asset_len (1 byte, expect 4)
			var assetLen byte
			assetLen, offset, err = ReadByte(data, offset)
			if err != nil {
				return 0, fmt.Errorf("reading output %d asset_len: %w", i, err)
			}
			// asset string (e.g., "SAL1")
			offset, err = SkipBytes(data, offset, int(assetLen))
			if err != nil {
				return 0, fmt.Errorf("skipping output %d asset: %w", i, err)
			}
			// view_tag (3 bytes)
			offset, err = SkipBytes(data, offset, 3)
			if err != nil {
				return 0, fmt.Errorf("skipping output %d view_tag: %w", i, err)
			}
			// encrypted_anchor (16 bytes)
			offset, err = SkipBytes(data, offset, 16)
			if err != nil {
				return 0, fmt.Errorf("skipping output %d anchor: %w", i, err)
			}
		}
	} else {
		// Compact format: total_reward (varint) + outputs_blob_size (varint) + sidechainId (32)
		_, offset, err = ReadVarint(data, offset) // total_reward
		if err != nil {
			return 0, fmt.Errorf("reading compact total_reward: %w", err)
		}
		_, offset, err = ReadVarint(data, offset) // outputs_blob_size
		if err != nil {
			return 0, fmt.Errorf("reading compact outputs_blob_size: %w", err)
		}
		offset, err = SkipBytes(data, offset, 32) // sidechainId
		if err != nil {
			return 0, fmt.Errorf("skipping compact sidechainId: %w", err)
		}
	}

	// tx_extra_size (varint)
	txExtraSize, offset, err := ReadVarint(data, offset)
	if err != nil {
		return 0, fmt.Errorf("reading tx_extra_size: %w", err)
	}
	// Skip tx_extra
	offset, err = SkipBytes(data, offset, int(txExtraSize))
	if err != nil {
		return 0, fmt.Errorf("skipping tx_extra: %w", err)
	}

	// tx_type (varint) - Carrot miner type
	_, offset, err = ReadVarint(data, offset)
	if err != nil {
		return 0, fmt.Errorf("reading tx_type: %w", err)
	}

	// amount_burnt (varint)
	_, offset, err = ReadVarint(data, offset)
	if err != nil {
		return 0, fmt.Errorf("reading amount_burnt: %w", err)
	}

	// rct_type (1 byte, expect 0)
	offset, err = SkipBytes(data, offset, 1)
	if err != nil {
		return 0, fmt.Errorf("skipping rct_type: %w", err)
	}

	// Optional protocol_tx (Salvium Carrot v1+)
	// Check if this looks like a protocol_tx: version=4, unlock_time=60
	if share.MajorVersion >= 10 && offset+2 < len(data) {
		offset = skipProtocolTx(data, offset)
	}

	// num_transactions (varint)
	numTx, offset, err := ReadVarint(data, offset)
	if err != nil {
		return 0, fmt.Errorf("reading num_transactions: %w", err)
	}

	// Skip transaction hashes (32 bytes each)
	offset, err = SkipBytes(data, offset, int(numTx)*32)
	if err != nil {
		return 0, fmt.Errorf("skipping transactions: %w", err)
	}

	return offset, nil
}

// parseSidechainData parses the sidechain portion of the block.
//
// Reference: ~/p2pool-salvium/src/pool_block.cpp serialize_sidechain_data()
// Format:
//   - spend_public_key (32 bytes)
//   - view_public_key (32 bytes)
//   - txkeySecSeed (32 bytes)
//   - parent (32 bytes) - parent's sidechain ID
//   - uncles_count (varint)
//   - for each uncle: uncle_id (32 bytes)
//   - sidechainHeight (varint)
//   - difficulty.lo (varint)
//   - difficulty.hi (varint)
//   - cumulativeDifficulty.lo (varint)
//   - cumulativeDifficulty.hi (varint)
//   - merkleProof count (1 byte)
//   - for each proof: hash (32 bytes)
//   - mergeMiningExtra count (varint)
//   - for each: chain_id (32 bytes) + data_len (varint) + data
//   - sidechainExtraBuf (16 bytes - 4 uint32_t LE)
func parseSidechainData(data []byte, share *Share) error {
	if len(data) < 128 { // minimum: 32*4 + some varints + 16
		return fmt.Errorf("sidechain data too short: %d bytes", len(data))
	}

	offset := 0
	var err error

	// spend_public_key (32 bytes)
	share.MinerWallet.SpendPublicKey, offset, err = ReadHash(data, offset)
	if err != nil {
		return fmt.Errorf("reading spend_public_key: %w", err)
	}

	// view_public_key (32 bytes)
	share.MinerWallet.ViewPublicKey, offset, err = ReadHash(data, offset)
	if err != nil {
		return fmt.Errorf("reading view_public_key: %w", err)
	}

	// txkeySecSeed (32 bytes)
	share.TxkeySecSeed, offset, err = ReadHash(data, offset)
	if err != nil {
		return fmt.Errorf("reading txkeySecSeed: %w", err)
	}

	// parent (32 bytes) - this is the parent's sidechain ID!
	share.Parent, offset, err = ReadHash(data, offset)
	if err != nil {
		return fmt.Errorf("reading parent: %w", err)
	}

	// uncles_count (varint)
	uncleCount, offset, err := ReadVarint(data, offset)
	if err != nil {
		return fmt.Errorf("reading uncle_count: %w", err)
	}

	// Sanity check
	if uncleCount > 10 {
		return fmt.Errorf("too many uncles: %d", uncleCount)
	}

	// Read uncle hashes
	share.Uncles = make([]Hash, uncleCount)
	for i := uint64(0); i < uncleCount; i++ {
		share.Uncles[i], offset, err = ReadHash(data, offset)
		if err != nil {
			return fmt.Errorf("reading uncle %d: %w", i, err)
		}
	}

	// sidechainHeight (varint)
	share.SidechainHeight, offset, err = ReadVarint(data, offset)
	if err != nil {
		return fmt.Errorf("reading sidechainHeight: %w", err)
	}

	// difficulty.lo (varint)
	share.Difficulty.Lo, offset, err = ReadVarint(data, offset)
	if err != nil {
		return fmt.Errorf("reading difficulty.lo: %w", err)
	}

	// difficulty.hi (varint)
	share.Difficulty.Hi, offset, err = ReadVarint(data, offset)
	if err != nil {
		return fmt.Errorf("reading difficulty.hi: %w", err)
	}

	// cumulativeDifficulty.lo (varint)
	share.CumulativeDifficulty.Lo, offset, err = ReadVarint(data, offset)
	if err != nil {
		return fmt.Errorf("reading cumulativeDifficulty.lo: %w", err)
	}

	// cumulativeDifficulty.hi (varint)
	share.CumulativeDifficulty.Hi, offset, err = ReadVarint(data, offset)
	if err != nil {
		return fmt.Errorf("reading cumulativeDifficulty.hi: %w", err)
	}

	// merkleProof count (1 byte)
	merkleCount, offset, err := ReadByte(data, offset)
	if err != nil {
		return fmt.Errorf("reading merkleProof count: %w", err)
	}

	// Read merkle proof hashes
	share.MerkleProof = make([]Hash, merkleCount)
	for i := uint8(0); i < merkleCount; i++ {
		share.MerkleProof[i], offset, err = ReadHash(data, offset)
		if err != nil {
			return fmt.Errorf("reading merkleProof %d: %w", i, err)
		}
	}

	// mergeMiningExtra count (varint)
	mmExtraCount, offset, err := ReadVarint(data, offset)
	if err != nil {
		return fmt.Errorf("reading mergeMiningExtra count: %w", err)
	}

	// Read merge mining extra data
	if mmExtraCount > 0 {
		share.MergeMiningExtra = make(map[Hash][]byte)
		for i := uint64(0); i < mmExtraCount; i++ {
			var chainId Hash
			chainId, offset, err = ReadHash(data, offset)
			if err != nil {
				return fmt.Errorf("reading mmExtra %d chainId: %w", i, err)
			}

			var dataLen uint64
			dataLen, offset, err = ReadVarint(data, offset)
			if err != nil {
				return fmt.Errorf("reading mmExtra %d dataLen: %w", i, err)
			}

			if offset+int(dataLen) > len(data) {
				return fmt.Errorf("mmExtra %d data exceeds bounds", i)
			}

			mmData := make([]byte, dataLen)
			copy(mmData, data[offset:offset+int(dataLen)])
			offset += int(dataLen)

			share.MergeMiningExtra[chainId] = mmData
		}
	}

	// sidechainExtraBuf (16 bytes - 4 uint32_t LE)
	if offset+16 > len(data) {
		return fmt.Errorf("sidechainExtraBuf exceeds bounds: offset=%d, len=%d", offset, len(data))
	}
	share.SidechainExtraBuf[0] = binary.LittleEndian.Uint32(data[offset:])
	share.SidechainExtraBuf[1] = binary.LittleEndian.Uint32(data[offset+4:])
	share.SidechainExtraBuf[2] = binary.LittleEndian.Uint32(data[offset+8:])
	share.SidechainExtraBuf[3] = binary.LittleEndian.Uint32(data[offset+12:])

	return nil
}

// skipProtocolTx attempts to skip a Salvium protocol_tx if present.
// Returns the new offset (unchanged if no protocol_tx found).
// Protocol_tx format: version=4, unlock_time=60, then standard tx format.
func skipProtocolTx(data []byte, offset int) int {
	savedOffset := offset

	// Check if we have enough data
	if offset+2 >= len(data) {
		return offset
	}

	// Check for version=4
	firstVal, nextOffset, err := ReadVarint(data, offset)
	if err != nil || firstVal != 4 {
		return savedOffset
	}

	// Check for unlock_time=60
	secondVal, nextOffset, err := ReadVarint(data, nextOffset)
	if err != nil || secondVal != 60 {
		return savedOffset
	}

	// This looks like a protocol_tx, try to skip it
	offset = nextOffset

	// vin_size (varint)
	vinSize, offset, err := ReadVarint(data, offset)
	if err != nil || vinSize != 1 {
		return savedOffset
	}

	// txin_type (1 byte, expect TXIN_GEN = 0xff)
	txinType, offset, err := ReadByte(data, offset)
	if err != nil || txinType != 0xff {
		return savedOffset
	}

	// protocol_height (varint)
	_, offset, err = ReadVarint(data, offset)
	if err != nil {
		return savedOffset
	}

	// vout_size (varint)
	voutSize, offset, err := ReadVarint(data, offset)
	if err != nil {
		return savedOffset
	}

	// Skip outputs
	for i := uint64(0); i < voutSize; i++ {
		_, offset, err = ReadVarint(data, offset) // amount
		if err != nil {
			return savedOffset
		}
		offset, err = SkipBytes(data, offset, 1+32) // type + key
		if err != nil {
			return savedOffset
		}
	}

	// extra_size (varint)
	extraSize, offset, err := ReadVarint(data, offset)
	if err != nil {
		return savedOffset
	}
	offset, err = SkipBytes(data, offset, int(extraSize))
	if err != nil {
		return savedOffset
	}

	// protocol_type (varint)
	_, offset, err = ReadVarint(data, offset)
	if err != nil {
		return savedOffset
	}

	// rct (1 byte)
	offset, err = SkipBytes(data, offset, 1)
	if err != nil {
		return savedOffset
	}

	return offset
}
