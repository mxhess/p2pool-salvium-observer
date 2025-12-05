package mempool

import (
	"encoding/binary"
	"testing"
)

func FuzzMempool_Pick(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < (8*3) || len(data)%(8*3) != 0 {
			t.SkipNow()
		}
		var mempool Mempool

		for i := 0; i < len(data); i += 8 * 3 {
			var entry Entry
			// fixed index
			binary.LittleEndian.PutUint64(entry.Id[:], uint64(i/(8*3)))
			entry.BlobSize = binary.LittleEndian.Uint64(data[i:])
			entry.Weight = binary.LittleEndian.Uint64(data[i+8:])
			entry.Fee = binary.LittleEndian.Uint64(data[i+8+8:])

			mempool = append(mempool, &entry)
		}

		// use first index as args
		ee := mempool[0]
		mempool = mempool[1:]
		newPool := mempool.Pick(ee.Fee, ee.Weight, ee.BlobSize)
		_ = newPool
	})
}
