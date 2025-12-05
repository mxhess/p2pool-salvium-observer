package mempool

import (
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
	"lukechampine.com/uint128"
	"math"
	"math/bits"
	"slices"
	"time"
)

type Entry struct {
	Id           types.Hash `json:"id"`
	BlobSize     uint64     `json:"blob_size"`
	Weight       uint64     `json:"weight"`
	Fee          uint64     `json:"fee"`
	TimeReceived time.Time  `json:"-"`
}

type Mempool []*Entry

func (m Mempool) Sort() {
	// Sort all transactions by fee per byte (highest to lowest)

	slices.SortFunc(m, func(a, b *Entry) int {
		return a.Compare(b)
	})
}

func (m Mempool) WeightAndFees() (weight, fees uint64) {
	for _, e := range m {
		weight += e.Weight
		fees += e.Fee
	}
	return
}

func (m Mempool) Fees() (r uint64) {
	for _, e := range m {
		r += e.Fee
	}
	return r
}

func (m Mempool) Weight() (r uint64) {
	for _, e := range m {
		r += e.Weight
	}
	return r
}

// Pick Selects transactions semi-optimally
//
// Picking all transactions will result in the base reward penalty
// Use a heuristic algorithm to pick transactions and get the maximum possible reward
// Testing has shown that this algorithm is very close to the optimal selection
// Usually no more than 0.5 micronero away from the optimal discrete knapsack solution
// Sometimes it even finds the optimal solution
func (m Mempool) Pick(baseReward, minerTxWeight, medianWeight uint64) Mempool {
	// Sort all transactions by fee per byte (highest to lowest)
	m.Sort()

	finalReward := baseReward
	finalFees := uint64(0)
	finalWeight := minerTxWeight

	mempoolTxsOrder2 := make(Mempool, 0, len(m))

	for i, tx := range m {
		k := -1

		reward := GetBlockReward(baseReward, medianWeight, finalFees+tx.Fee, finalWeight+tx.Weight)
		if reward > finalReward {
			// If simply adding this transaction increases the reward, remember it
			finalReward = reward
			k = i
		}

		// Try replacing other transactions when we are above the limit
		if finalWeight+tx.Weight > medianWeight {
			// Don't check more than 100 transactions deep because they have higher and higher fee/byte
			n := len(mempoolTxsOrder2)
			for j, j1 := n-1, max(0, n-100); j >= j1; j-- {
				prevTx := mempoolTxsOrder2[j]
				reward2 := GetBlockReward(baseReward, medianWeight, finalFees+tx.Fee-prevTx.Fee, finalWeight+tx.Weight-prevTx.Weight)
				if reward2 > finalReward {
					// If replacing some other transaction increases the reward even more, remember it
					// And keep trying to replace other transactions
					finalReward = reward2
					k = j
				}
			}
		}

		if k == i {
			// Simply adding this tx improves the reward
			mempoolTxsOrder2 = append(mempoolTxsOrder2, tx)
			finalFees += tx.Fee
			finalWeight += tx.Weight
		} else if k >= 0 {
			// Replacing another tx with this tx improves the reward
			prevTx := mempoolTxsOrder2[k]
			mempoolTxsOrder2[k] = tx
			finalFees += tx.Fee - prevTx.Fee
			finalWeight += tx.Weight - prevTx.Weight
		}
	}

	return mempoolTxsOrder2
}

func (m Mempool) perfectSumRecursion(c chan Mempool, targetFee uint64, i int, currentSum uint64, top *int, m2 Mempool) {
	if currentSum == targetFee {
		c <- slices.Clone(m2)
		return
	}

	if currentSum < targetFee && i < len(m) {
		if top != nil && *top < i {
			*top = i
			utils.Logf("Mempool", "index %d/%d", i, len(m))
		}
		m3 := append(m2, m[i])
		m.perfectSumRecursion(c, targetFee, i+1, currentSum+m[i].Fee, nil, m3)
		m.perfectSumRecursion(c, targetFee, i+1, currentSum, top, m2)
	}
}

func (m Mempool) PerfectSum(targetFee uint64) chan Mempool {
	mempoolTxsOrder2 := make(Mempool, 0, len(m))
	c := make(chan Mempool)
	go func() {
		defer close(c)
		var i int
		m.perfectSumRecursion(c, targetFee, 0, 0, &i, mempoolTxsOrder2)
	}()
	return c
}

// Compare returns -1 if self is preferred over o, 0 if equal, 1 if o is preferred over self
func (t *Entry) Compare(o *Entry) int {
	a := t.Fee * o.Weight
	b := o.Fee * t.Weight

	// Prefer transactions with higher fee/byte
	if a > b {
		return -1
	}
	if a < b {
		return 1
	}

	// If fee/byte is the same, prefer smaller transactions (they give smaller penalty when going above the median block size limit)
	if t.Weight < o.Weight {
		return -1
	}
	if t.Weight > o.Weight {
		return 1
	}

	// If two transactions have exactly the same fee and weight, just order them by id
	return t.Id.Compare(o.Id)
}

// GetBlockReward Faster and limited version of block.GetBlockReward
func GetBlockReward(baseReward, medianWeight, fees, weight uint64) uint64 {
	if weight <= medianWeight {
		return baseReward + fees
	}
	if weight > medianWeight*2 {
		return 0
	}

	hi, lo := bits.Mul64(baseReward, (medianWeight*2-weight)*weight)

	if medianWeight >= math.MaxUint32 {
		// slow path for medianWeight overflow
		//panic("overflow")
		return uint128.New(lo, hi).Div64(medianWeight).Div64(medianWeight).Lo
	}

	// This will overflow if medianWeight >= 2^32
	// Performance of this code is more important
	reward, _ := bits.Div64(hi, lo, medianWeight*medianWeight)

	return reward + fees
}
