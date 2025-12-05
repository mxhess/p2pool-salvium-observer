package utils

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func FuzzCanonicalUvarint(f *testing.F) {
	for i := 0; i < 10; i++ {
		encoded := binary.AppendUvarint(nil, uint64(1)<<((i+0)*7))
		f.Add(encoded)
		encoded = binary.AppendUvarint(nil, uint64(1)<<((i+1)*7))
		f.Add(encoded)
		encoded = binary.AppendUvarint(nil, uint64(1)<<((i+2)*7))
		f.Add(encoded)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var buf [binary.MaxVarintLen64]byte
		value, n := CanonicalUvarint(data)
		if n <= 0 {
			t.SkipNow()
		}

		data = data[:n]

		if len(data) != UVarInt64Size(value) {
			t.Fatalf("[%d] expected %d bytes, got %d", value, n, UVarInt64Size(value))
		}

		encoded := binary.AppendUvarint(buf[:0], value)
		if !bytes.Equal(encoded, data) {
			t.Fatalf("[%d] canonical encoding mismatch: have %x, want %x", value, encoded, data)
		}
	})
}
