package types

import (
	"encoding/binary"
	"lukechampine.com/uint128"
	"math"
	"math/bits"
)

func DifficultyFromPoW(powHash Hash) Difficulty {
	if powHash == ZeroHash {
		return ZeroDifficulty
	}

	return Difficulty(uint128.Max.Div(uint128.FromBytes(powHash[16:])))
}

func (d Difficulty) CheckPoW(pow Hash) bool {
	return DifficultyFromPoW(pow).Cmp(d) >= 0
}

func (d Difficulty) CheckPoW_Native(pow Hash) bool {
	// P2Pool similar code
	var result [6]uint64
	var product [6]uint64

	a := [4]uint64{
		binary.LittleEndian.Uint64(pow[:]),
		binary.LittleEndian.Uint64(pow[8:]),
		binary.LittleEndian.Uint64(pow[16:]),
		binary.LittleEndian.Uint64(pow[24:]),
	}

	if d.Hi == 0 {
		for i := 3; i >= 0; i-- {
			product[1], product[0] = bits.Mul64(a[i], d.Lo)

			var carry uint64
			for k, l := i, 0; k < 5; k, l = k+1, l+1 {
				result[k], carry = bits.Add64(result[k], product[l], carry)
			}

			if result[4] > 0 {
				return false
			}
		}
	} else {
		b := [2]uint64{d.Lo, d.Hi}

		for i := 3; i >= 0; i-- {
			for j := 1; j >= 0; j-- {
				product[1], product[0] = bits.Mul64(a[i], b[j])

				var carry uint64
				for k, l := i+j, 0; k < 6; k, l = k+1, l+1 {
					result[k], carry = bits.Add64(result[k], product[l], carry)
				}

				if result[4] > 0 || result[5] > 0 {
					return false
				}
			}
		}
	}

	return true
}

// Target Finds a 64-bit target for mining (target = 2^64 / difficulty) and rounds up the result of division
// Because of that, there's a very small chance that miners will find a hash that meets the target but is still wrong (hash * difficulty >= 2^256)
// A proper difficulty check is in CheckPoW / CheckPoW_Native
func (d Difficulty) Target() uint64 {
	if d.Hi > 0 {
		return 1
	}

	// Safeguard against division by zero (CPU will trigger it even if lo = 1 because result doesn't fit in 64 bits)
	if d.Lo <= 1 {
		return math.MaxUint64
	}

	q, rem := bits.Div64(1, 0, d.Lo)
	if rem > 0 {
		return q + 1
	} else {
		return q
	}
}
