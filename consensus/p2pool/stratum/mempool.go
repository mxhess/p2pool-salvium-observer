package stratum

import (
	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/mempool"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"time"
)

type MiningMempool map[types.Hash]*mempool.Entry

// Add Inserts a transaction into the mempool.
func (m MiningMempool) Add(tx *mempool.Entry) (added bool) {
	if _, ok := m[tx.Id]; !ok {
		if tx.TimeReceived.IsZero() {
			tx.TimeReceived = time.Now()
		}
		m[tx.Id] = tx
		added = true
	}

	return added
}

func (m MiningMempool) Swap(pool mempool.Mempool) {
	currentTime := time.Now()

	for _, tx := range pool {
		if v, ok := m[tx.Id]; ok {
			//tx is already here, use previous seen time
			tx.TimeReceived = v.TimeReceived
		} else {
			tx.TimeReceived = currentTime
		}
	}

	clear(m)

	for _, tx := range pool {
		m[tx.Id] = tx
	}
}

func (m MiningMempool) Select(highFee uint64, receivedSince time.Duration) (pool mempool.Mempool) {
	pool = make(mempool.Mempool, 0, len(m))

	currentTime := time.Now()

	for _, tx := range m {
		if currentTime.Sub(tx.TimeReceived) > receivedSince || tx.Fee >= highFee {
			pool = append(pool, tx)
		}
	}

	return pool
}
