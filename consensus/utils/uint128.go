package utils

import "math/bits"

func Div128(hi, lo, y uint64) (hiQuo, loQuo uint64) {
	if hi < y {
		loQuo, _ = bits.Div64(hi, lo, y)
	} else {
		hiQuo, loQuo = bits.Div64(0, hi, y)
		loQuo, _ = bits.Div64(loQuo, lo, y)
	}
	return
}
