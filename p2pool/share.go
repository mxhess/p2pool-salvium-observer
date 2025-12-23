// Package p2pool provides types for parsing p2pool-salvium block data.
// Based directly on ~/p2pool-salvium/src/ C++ implementation.
package p2pool

// Share represents a P2Pool sidechain block (share).
// Field names and types match C++ PoolBlock from pool_block.h
type Share struct {
	// === Identity ===
	// SidechainId is the unique identifier for this share (includes extra_nonce).
	// C++: m_sidechainId - calculated from block data with nonce/extra_nonce zeroed
	SidechainId Hash

	// === Mainchain Data ===
	// MajorVersion is the hard fork version
	// C++: m_majorVersion
	MajorVersion uint8

	// MinorVersion is the soft fork version
	// C++: m_minorVersion
	MinorVersion uint8

	// Timestamp is the block timestamp (Unix seconds)
	// C++: m_timestamp
	Timestamp uint64

	// PrevId is the previous mainchain block hash
	// C++: m_prevId
	PrevId Hash

	// Nonce is the mining nonce
	// C++: m_nonce
	Nonce uint32

	// MainchainHeight is the height in the Salvium mainchain.
	// C++: m_txinGenHeight - from coinbase transaction input
	MainchainHeight uint64

	// === Miner Wallet ===
	// MinerWallet contains the miner's spend and view public keys
	// C++: m_minerWallet (Wallet class with spend_public_key() and view_public_key())
	MinerWallet Address

	// === Sidechain Chain Data ===
	// TxkeySecSeed is the transaction key secret seed
	// C++: m_txkeySecSeed
	TxkeySecSeed Hash

	// Parent is the sidechain ID of the parent share
	// C++: m_parent - NOTE: This is a sidechain ID, not template ID
	Parent Hash

	// Uncles is the list of uncle sidechain IDs
	// C++: m_uncles
	Uncles []Hash

	// SidechainHeight is the height in the P2Pool sidechain
	// C++: m_sidechainHeight
	SidechainHeight uint64

	// Difficulty is this share's target difficulty
	// C++: m_difficulty
	Difficulty Difficulty

	// CumulativeDifficulty is the cumulative difficulty up to this share
	// C++: m_cumulativeDifficulty
	CumulativeDifficulty Difficulty

	// === Merge Mining ===
	// MerkleProof is the merkle proof for merge mining
	// C++: m_merkleProof
	MerkleProof []Hash

	// MergeMiningExtra contains additional merge mining data
	// C++: m_mergeMiningExtra (map<hash, vector<uint8_t>>)
	MergeMiningExtra map[Hash][]byte

	// SidechainExtraBuf is arbitrary extra data (4 uint32s = 16 bytes)
	// C++: m_sidechainExtraBuf[4]
	SidechainExtraBuf [4]uint32

	// === Off-chain computed fields (not serialized) ===
	// Depth is the depth from chain tip
	Depth uint64

	// Verified indicates if this share has been verified
	Verified bool

	// Invalid indicates if verification failed
	Invalid bool
}

// TemplateId returns the template ID (sidechain ID with extra_nonce zeroed).
// For parent chain lookups, use SidechainId directly since parents reference by sidechain ID.
// C++: get_full_id() combines sidechainId + nonce + extraNonce
func (s *Share) TemplateId() Hash {
	// Template ID is what you get when extra_nonce is zeroed in the hash calculation
	// For now, this returns SidechainId - the parent references use SidechainId anyway
	return s.SidechainId
}

// IsUncle returns true if this share has no uncles itself (it is an uncle candidate)
// Actually, a share IS an uncle when it's referenced in another share's Uncles list
func (s *Share) IsUncle() bool {
	return false // This needs to be determined by the chain, not the share itself
}

// GetMinerAddress returns the base58-encoded miner address
func (s *Share) GetMinerAddress() string {
	return s.MinerWallet.ToBase58()
}
