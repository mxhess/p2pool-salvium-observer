package block

import (
	"git.gammaspectra.live/P2Pool/consensus/v4/monero"
	"lukechampine.com/uint128"
	"math"
	"math/bits"
)

func GetBaseReward(alreadyGeneratedCounts uint64) uint64 {
	result := (^alreadyGeneratedCounts) >> 19
	if result < monero.TailEmissionReward {
		return monero.TailEmissionReward
	}
	return result
}

// GetBlockReward
// Taken from https://github.com/monero-project/monero/blob/b0bf49a65a38ceb1acfbc8e17f40e63383ac140d/src/cryptonote_basic/cryptonote_basic_impl.cpp#L83
func GetBlockReward(medianWeight, currentBlockWeight, alreadyGeneratedCoins uint64, version uint8) (reward uint64) {
	const DIFFICULTY_TARGET_V1 = 60  // seconds - before first fork
	const DIFFICULTY_TARGET_V2 = 120 // seconds
	const EMISSION_SPEED_FACTOR_PER_MINUTE = 20
	const FINAL_SUBSIDY_PER_MINUTE = 300000000000 // 3 * pow(10, 11)
	const MONEY_SUPPLY = math.MaxUint64
	const CRYPTONOTE_BLOCK_GRANTED_FULL_REWARD_ZONE_V1 = 20000  //size of block (bytes) after which reward for block calculated using block size - before first fork
	const CRYPTONOTE_BLOCK_GRANTED_FULL_REWARD_ZONE_V2 = 60000  //size of block (bytes) after which reward for block calculated using block size
	const CRYPTONOTE_BLOCK_GRANTED_FULL_REWARD_ZONE_V5 = 300000 //size of block (bytes) after which reward for block calculated using block size - second change, from v5

	target := uint64(DIFFICULTY_TARGET_V2)
	if version < 2 {
		target = DIFFICULTY_TARGET_V1
	}

	targetMinutes := target / 60

	emissionSpeedFactor := EMISSION_SPEED_FACTOR_PER_MINUTE - (targetMinutes - 1)

	baseReward := (MONEY_SUPPLY - alreadyGeneratedCoins) >> emissionSpeedFactor
	if baseReward < (FINAL_SUBSIDY_PER_MINUTE * targetMinutes) {
		baseReward = FINAL_SUBSIDY_PER_MINUTE * targetMinutes
	}

	fullRewardZone := func(version uint8) uint64 {
		// From get_min_block_weight()
		if version < 2 {
			return CRYPTONOTE_BLOCK_GRANTED_FULL_REWARD_ZONE_V1
		}
		if version < 5 {
			return CRYPTONOTE_BLOCK_GRANTED_FULL_REWARD_ZONE_V2
		}
		return CRYPTONOTE_BLOCK_GRANTED_FULL_REWARD_ZONE_V5
	}(version)

	//make it soft
	if medianWeight < fullRewardZone {
		medianWeight = fullRewardZone
	}

	if currentBlockWeight <= medianWeight {
		return baseReward
	}

	if currentBlockWeight > 2*medianWeight {
		//Block cumulative weight is too big
		return 0
	}

	hi, lo := bits.Mul64(baseReward, (2*medianWeight-currentBlockWeight)*currentBlockWeight)

	return uint128.New(lo, hi).Div64(medianWeight).Div64(medianWeight).Lo
}
