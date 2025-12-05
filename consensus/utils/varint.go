package utils

import (
	"encoding/binary"
	"errors"
	"io"
	"math/bits"
)

var errOverflow = errors.New("binary: varint overflows a 64-bit integer")

var ErrNonCanonicalEncoding = errors.New("binary: varint has non canonical encoding")

// ReadCanonicalUvarint reads an encoded unsigned integer from r and returns it as a uint64.
// The error is ErrNonCanonicalEncoding if non-canonical bytes were read.
// The error is [io.EOF] only if no bytes were read.
// If an [io.EOF] happens after reading some but not all the bytes,
// ReadCanonicalUvarint returns [io.ErrUnexpectedEOF].
func ReadCanonicalUvarint(r io.ByteReader) (uint64, error) {
	var x uint64
	var s uint
	for i := 0; i < binary.MaxVarintLen64; i++ {
		b, err := r.ReadByte()
		if err != nil {
			if i > 0 && err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return x, err
		}
		if i > 0 && b == 0 {
			return x, ErrNonCanonicalEncoding
		}
		if b < 0x80 {
			if i == binary.MaxVarintLen64-1 && b > 1 {
				return x, errOverflow
			}
			return x | uint64(b)<<s, nil
		}
		x |= uint64(b&0x7f) << s
		s += 7
	}
	return x, errOverflow
}

// CanonicalUvarint decodes a uint64 from buf and returns that value and the
// number of bytes read (> 0). If an error occurred, the value is 0
// and the number of bytes n is <= 0 meaning:
//   - n == 0: buf too small;
//   - n < 0: value larger than 64 bits (overflow) and -n is the number of
//     bytes read.
//
// The function errors if non-canonical bytes were read.
func CanonicalUvarint(buf []byte) (uint64, int) {
	var x uint64
	var s uint
	for i, b := range buf {
		if i == binary.MaxVarintLen64 {
			// Catch byte reads past MaxVarintLen64.
			// See issue https://golang.org/issues/41185
			return 0, -(i + 1) // overflow
		}
		if i > 0 && b == 0 {
			return 0, -(i + 1) // overflow mask TODO: use different mask
		}
		if b < 0x80 {
			if i == binary.MaxVarintLen64-1 && b > 1 {
				return 0, -(i + 1) // overflow
			}
			return x | uint64(b)<<s, i + 1
		}
		x |= uint64(b&0x7f) << s
		s += 7
	}
	return 0, 0
}

func UVarInt64SliceSize[T uint64 | int](v []T) (n int) {
	for i := range v {
		n += UVarInt64Size(v[i])
	}
	return
}

func UVarInt64Size[T uint64 | int | uint8](v T) (n int) {
	return 1 + (bits.Len64(uint64(v))*9)/64
}
