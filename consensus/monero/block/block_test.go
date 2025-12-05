package block

import (
	"bytes"
	"encoding/hex"
	"os"
	"path"
	"runtime"
	"testing"
)

func init() {
	_, filename, _, _ := runtime.Caller(0)
	// The ".." may change depending on you folder structure
	dir := path.Join(path.Dir(filename), "../..")
	err := os.Chdir(dir)
	if err != nil {
		panic(err)
	}
}

var fuzzPoolBlocks = []string{
	"testdata/v4_block.dat",
	"testdata/v2_block.dat",
	"testdata/v1_mainnet_test2_block.dat",
}

func FuzzMainBlockRoundTrip(f *testing.F) {

	for _, path := range fuzzPoolBlocks {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		reader := bytes.NewReader(data)
		b := &Block{}
		err = b.FromReader(reader, false, nil)
		if err != nil {
			f.Skipf("leftover error: %s", err)
		}
		buf, err := b.MarshalBinary()
		if err != nil {
			f.Fatal(err)
		}
		f.Add(buf)
	}

	f.Fuzz(func(t *testing.T, buf []byte) {
		b := &Block{}
		reader := bytes.NewReader(buf)
		err := b.FromReader(reader, false, nil)
		if err != nil {
			t.Skipf("leftover error: %s", err)
		}
		if reader.Len() > 0 {
			//clamp comparison
			buf = buf[:len(buf)-reader.Len()]
		}

		data, err := b.MarshalBinary()
		if err != nil {
			t.Fatalf("failed to marshal decoded block: %s", err)
			return
		}
		if !bytes.Equal(data, buf) {
			t.Logf("EXPECTED (len %d):\n%s", len(buf), hex.Dump(buf))
			t.Logf("ACTUAL (len %d):\n%s", len(data), hex.Dump(data))
			t.Fatalf("mismatched roundtrip")
		}
	})
}
