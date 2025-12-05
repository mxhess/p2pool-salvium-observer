package types

import (
	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/mempool"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"time"
)

type MinerData struct {
	MajorVersion          uint8            `json:"major_version"`
	Height                uint64           `json:"height"`
	PrevId                types.Hash       `json:"prev_id"`
	SeedHash              types.Hash       `json:"seed_hash"`
	Difficulty            types.Difficulty `json:"difficulty"`
	MedianWeight          uint64           `json:"median_weight"`
	AlreadyGeneratedCoins uint64           `json:"already_generated_coins"`
	MedianTimestamp       uint64           `json:"median_timestamp"`
	TxBacklog             mempool.Mempool  `json:"tx_backlog"`

	TimeReceived time.Time `json:"time_received"`

	AuxiliaryChains []AuxiliaryChainData `json:"aux_chains,omitempty"`
	AuxiliaryNonce  uint32               `json:"aux_nonce,omitempty"`
}

type AuxiliaryChainData struct {
	UniqueId   types.Hash
	Data       types.Hash
	Difficulty types.Difficulty
}
