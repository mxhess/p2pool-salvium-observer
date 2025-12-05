package utils

import (
	"io"
	"math"
	"slices"
)

// ReadFullProgressive Reads into buf up to size bytes by doubling size each time
func ReadFullProgressive[T ~[]byte](r io.Reader, dst *T, size int) (n int, err error) {
	if size < 0 {
		return 0, io.EOF
	}

	buf := *dst

	var offset int

	// reserve some, start with 64 KiB
	buf = slices.Grow(buf[:0], min(math.MaxUint16+1, size))
	buf = buf[:min(math.MaxUint16+1, size)]

	// special reader to grow extra over time
	for {
		// only read last part past read offset
		if n, err = io.ReadFull(r, buf[offset:]); err != nil {
			return offset + n, err
		}
		offset += n

		if offset >= size || n == 0 {
			break
		}

		// double size or just remainder
		buf = slices.Grow(buf[:0], min(offset*2, size))
		buf = buf[:min(offset*2, size)]
	}
	*dst = buf
	return offset, nil
}
