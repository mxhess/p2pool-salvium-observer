package transaction

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func FuzzCoinbaseTransactionRoundTrip(f *testing.F) {
	f.Fuzz(func(t *testing.T, buf []byte) {
		tx := &CoinbaseTransaction{}
		if err := tx.UnmarshalBinary(buf, false, false); err != nil {
			t.Skipf("leftover error: %s", err)
			return
		}
		data, err := tx.MarshalBinary()
		if err != nil {
			t.Fatalf("failed to marshal decoded transacion: %s", err)
			return
		}
		if !bytes.Equal(data, buf) {
			t.Logf("EXPECTED (len %d):\n%s", len(buf), hex.Dump(buf))
			t.Logf("ACTUAL (len %d):\n%s", len(data), hex.Dump(data))
			t.Fatalf("mismatched roundtrip")
		}
	})
}
