// Package p2pool provides types for parsing p2pool-salvium block data.
// Based directly on ~/p2pool-salvium/src/ C++ implementation.
package p2pool

// PPLNS constants from C++ side_chain.cpp
const (
	// PPLNSWindow is the number of shares in the PPLNS window
	// C++: m_chainWindowSize = 2160
	PPLNSWindow = 2160

	// UnclePenalty is the percentage penalty for uncle shares
	// C++: m_unclePenalty = 20 (meaning 20%)
	UnclePenalty = 20

	// AtomicUnits is the number of atomic units per SAL.
	// Salvium uses 1e8 (100 million) atomic units.
	AtomicUnits = 100_000_000
)

// PositionBuckets is the number of buckets for position visualization
const PositionBuckets = 30

// MinerWeight tracks a miner's accumulated weight in the PPLNS window
type MinerWeight struct {
	Address    Address
	Weight     Difficulty
	ShareCount int // Number of main shares in window
	UncleCount int // Number of uncle shares in window

	// Position tracking for visualization (30 buckets across window)
	ShareBuckets [PositionBuckets]int // Count of shares per bucket
	UncleBuckets [PositionBuckets]int // Count of uncles per bucket
}

// PPLNSResult contains the full PPLNS calculation result
type PPLNSResult struct {
	// Shares is the map of miner address -> accumulated weight
	Shares map[Address]*MinerWeight

	// TotalWeight is the sum of all miner weights
	TotalWeight Difficulty

	// BlocksIncluded is the number of shares processed
	BlocksIncluded int

	// UnclesIncluded is the number of uncle shares processed
	UnclesIncluded int

	// TipHeight is the sidechain height of the tip share
	TipHeight uint64

	// BottomHeight is the sidechain height of the oldest share included
	BottomHeight uint64

	// WindowDuration is the time span of the PPLNS window in seconds
	WindowDuration int64
}

// CalculatePPLNS calculates PPLNS shares starting from the tip.
// This matches C++ SideChain::get_shares() from side_chain.cpp:407-520
//
// Parameters:
//   - shares: map of sidechain ID -> Share (from LoadAllShares)
//   - tip: the tip share to start from
//   - mainchainDiff: current mainchain difficulty (for 2x weight cap)
//
// The algorithm:
//  1. Walk parent chain from tip through PPLNS window
//  2. For each share, add its difficulty to the miner's weight
//  3. For each uncle in the share:
//     - Calculate uncle_penalty = uncle.Difficulty * 20 / 100
//     - Add (uncle.Difficulty - uncle_penalty) to uncle's miner
//     - Add uncle_penalty to current share's miner (bonus for including)
//  4. Stop when total weight exceeds 2x mainchain difficulty
func CalculatePPLNS(shares map[Hash]*Share, tip *Share, mainchainDiff Difficulty) *PPLNSResult {
	result := &PPLNSResult{
		Shares:    make(map[Address]*MinerWeight),
		TipHeight: tip.SidechainHeight,
	}

	// Max PPLNS weight = mainchain_diff * 2
	// C++: const difficulty_type max_pplns_weight = mainchain_diff * 2;
	maxWeight := mainchainDiff.Mul64(2)

	var pplnsWeight Difficulty
	blockDepth := 0
	cur := tip

	// Track timestamps for window duration
	tipTimestamp := tip.Timestamp
	var bottomTimestamp uint64

	for cur != nil {
		curWeight := cur.Difficulty
		bottomTimestamp = cur.Timestamp // Update as we go

		// Calculate position bucket for current share (0 = newest, PositionBuckets-1 = oldest)
		// C++: window_index = block_depth * (N - 1) / (window_size - 1)
		// This maps [0, window_size-1] to [0, N-1]
		bucket := 0
		if PPLNSWindow > 1 {
			bucket = (blockDepth * (PositionBuckets - 1)) / (PPLNSWindow - 1)
		}
		if bucket >= PositionBuckets {
			bucket = PositionBuckets - 1
		}

		// Process uncles
		for _, uncleId := range cur.Uncles {
			uncle, ok := shares[uncleId]
			if !ok {
				// Uncle not found in our share map - skip
				continue
			}

			// Skip uncles which are already out of PPLNS window
			// C++: if (tip->m_sidechainHeight - uncle->m_sidechainHeight >= m_chainWindowSize)
			if tip.SidechainHeight-uncle.SidechainHeight >= PPLNSWindow {
				continue
			}

			// Calculate uncle penalty (20% of uncle's difficulty)
			// C++: const difficulty_type uncle_penalty = uncle->m_difficulty * m_unclePenalty / 100;
			unclePenalty := uncle.Difficulty.Mul64(UnclePenalty).Div64(100)

			// Uncle weight is difficulty minus penalty
			// C++: const difficulty_type uncle_weight = uncle->m_difficulty - uncle_penalty;
			uncleWeight := uncle.Difficulty.Sub(unclePenalty)

			// Check if adding uncle would exceed max weight
			// C++: const difficulty_type new_pplns_weight = pplns_weight + uncle_weight;
			newPplnsWeight := pplnsWeight.Add(uncleWeight)

			// Skip uncles that push PPLNS weight above the limit
			// C++: if (new_pplns_weight > max_pplns_weight) continue;
			if newPplnsWeight.Cmp(maxWeight) > 0 {
				continue
			}

			// Add uncle penalty to current share's miner (bonus for including uncle)
			// C++: cur_weight += uncle_penalty;
			curWeight = curWeight.Add(unclePenalty)

			// Uncle uses the same bucket as its parent block (C++ line 1162)
			// C++: ++our_uncles_in_window[window_index];
			addWeight(result.Shares, uncle.MinerWallet, uncleWeight, true, bucket)
			pplnsWeight = newPplnsWeight
			result.UnclesIncluded++
		}

		// Always add non-uncle shares even if PPLNS weight goes above the limit
		// C++: auto result = shares_set.emplace(cur_weight, &cur->m_minerWallet);
		addWeight(result.Shares, cur.MinerWallet, curWeight, false, bucket)
		pplnsWeight = pplnsWeight.Add(curWeight)
		result.BlocksIncluded++
		result.BottomHeight = cur.SidechainHeight

		// One non-uncle share can go above the limit, but check after adding
		// C++: if (pplns_weight > max_pplns_weight) break;
		if pplnsWeight.Cmp(maxWeight) > 0 {
			break
		}

		blockDepth++
		// C++: if (block_depth >= m_chainWindowSize) break;
		if blockDepth >= PPLNSWindow {
			break
		}

		// Reached the genesis block
		// C++: if (cur->m_sidechainHeight == 0) break;
		if cur.SidechainHeight == 0 {
			break
		}

		// Move to parent
		// C++: auto it = m_blocksById.find(cur->m_parent);
		parent, ok := shares[cur.Parent]
		if !ok {
			// Parent not found - stop walking
			break
		}
		cur = parent
	}

	result.TotalWeight = pplnsWeight
	result.WindowDuration = int64(tipTimestamp - bottomTimestamp)
	return result
}

// addWeight adds weight to a miner's accumulated weight
// bucket is the position bucket (0 = newest, PositionBuckets-1 = oldest)
func addWeight(shares map[Address]*MinerWeight, addr Address, weight Difficulty, isUncle bool, bucket int) {
	if mw, ok := shares[addr]; ok {
		mw.Weight = mw.Weight.Add(weight)
		if isUncle {
			mw.UncleCount++
			if bucket >= 0 && bucket < PositionBuckets {
				mw.UncleBuckets[bucket]++
			}
		} else {
			mw.ShareCount++
			if bucket >= 0 && bucket < PositionBuckets {
				mw.ShareBuckets[bucket]++
			}
		}
	} else {
		mw := &MinerWeight{
			Address: addr,
			Weight:  weight,
		}
		if isUncle {
			mw.UncleCount = 1
			if bucket >= 0 && bucket < PositionBuckets {
				mw.UncleBuckets[bucket] = 1
			}
		} else {
			mw.ShareCount = 1
			if bucket >= 0 && bucket < PositionBuckets {
				mw.ShareBuckets[bucket] = 1
			}
		}
		shares[addr] = mw
	}
}

// SharePositionString generates the share position visualization string
// e.g., "[+++++++++++++++++++++++++++++1]"
// Shows digit 1-9 for count, '+' for 10+, '.' for 0
func (mw *MinerWeight) SharePositionString() string {
	var buf [PositionBuckets + 2]byte
	buf[0] = '['
	for i := 0; i < PositionBuckets; i++ {
		count := mw.ShareBuckets[i]
		if count == 0 {
			buf[i+1] = '.'
		} else if count < 10 {
			buf[i+1] = byte('0' + count)
		} else {
			buf[i+1] = '+'
		}
	}
	buf[PositionBuckets+1] = ']'
	return string(buf[:])
}

// UnclePositionString generates the uncle position visualization string
// e.g., "[...........1...............1..]"
func (mw *MinerWeight) UnclePositionString() string {
	var buf [PositionBuckets + 2]byte
	buf[0] = '['
	for i := 0; i < PositionBuckets; i++ {
		count := mw.UncleBuckets[i]
		if count == 0 {
			buf[i+1] = '.'
		} else if count < 10 {
			buf[i+1] = byte('0' + count)
		} else {
			buf[i+1] = '+'
		}
	}
	buf[PositionBuckets+1] = ']'
	return string(buf[:])
}

// EstimatedPayout calculates the estimated payout for each miner if a block is found.
// Returns a map of miner address -> estimated payout in atomic units.
//
// This matches C++ SideChain::split_reward() logic.
func (r *PPLNSResult) EstimatedPayout(blockReward uint64) map[Address]uint64 {
	payouts := make(map[Address]uint64)

	if r.TotalWeight.IsZero() {
		return payouts
	}

	// For each miner: payout = (blockReward * minerWeight) / totalWeight
	// IMPORTANT: blockReward * weight can overflow uint64, so we use float64
	// which has enough precision for payout calculations (53 bits mantissa)
	for addr, mw := range r.Shares {
		// Calculate weight ratio as float64 to avoid overflow
		var weightRatio float64
		if r.TotalWeight.Hi == 0 && mw.Weight.Hi == 0 {
			// Both fit in 64 bits - use float64 for precision
			weightRatio = float64(mw.Weight.Lo) / float64(r.TotalWeight.Lo)
		} else {
			// 128-bit math: convert to float64 approximation
			// weight = Hi * 2^64 + Lo
			minerWeight := float64(mw.Weight.Hi)*float64(1<<64) + float64(mw.Weight.Lo)
			totalWeight := float64(r.TotalWeight.Hi)*float64(1<<64) + float64(r.TotalWeight.Lo)
			weightRatio = minerWeight / totalWeight
		}

		// Calculate payout: blockReward * (weight / totalWeight)
		payout := uint64(float64(blockReward) * weightRatio)
		payouts[addr] = payout
	}

	return payouts
}

// MinerPercentage returns the percentage of the PPLNS window owned by a miner.
// Returns a value between 0 and 100.
func (r *PPLNSResult) MinerPercentage(addr Address) float64 {
	mw, ok := r.Shares[addr]
	if !ok || r.TotalWeight.IsZero() {
		return 0
	}

	// Simple approximation for percentage
	if r.TotalWeight.Hi == 0 && mw.Weight.Hi == 0 {
		return float64(mw.Weight.Lo) / float64(r.TotalWeight.Lo) * 100
	}

	// For large difficulties, use approximate calculation
	return float64(mw.Weight.Lo) / float64(r.TotalWeight.Lo) * 100
}

// TopMiners returns the top N miners by weight, sorted descending.
func (r *PPLNSResult) TopMiners(n int) []MinerWeight {
	// Collect all miners
	miners := make([]MinerWeight, 0, len(r.Shares))
	for _, mw := range r.Shares {
		miners = append(miners, *mw)
	}

	// Sort by weight descending (simple bubble sort for now)
	for i := 0; i < len(miners)-1; i++ {
		for j := i + 1; j < len(miners); j++ {
			if miners[j].Weight.Cmp(miners[i].Weight) > 0 {
				miners[i], miners[j] = miners[j], miners[i]
			}
		}
	}

	if n > len(miners) {
		n = len(miners)
	}
	return miners[:n]
}

// GetOrderedMiners returns all miner addresses in the same order as coinbase outputs.
// P2Pool creates coinbase outputs in order of miner weight (highest first).
// This matches C++ split_reward() which iterates shares in weight order.
func (r *PPLNSResult) GetOrderedMiners() []Address {
	// Get all miners sorted by weight descending
	miners := r.TopMiners(len(r.Shares))

	// Extract just the addresses in order
	addresses := make([]Address, len(miners))
	for i, mw := range miners {
		addresses[i] = mw.Address
	}

	return addresses
}
