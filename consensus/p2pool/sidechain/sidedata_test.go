package sidechain

import (
	"bytes"
	"encoding/hex"
	"os"
	"testing"
)

func FuzzSideDataRoundTrip_V1(f *testing.F) {
	data, err := os.ReadFile("testdata/v1_mainnet_test2_block.dat")
	if err != nil {
		f.Fatal(err)
	}
	b := &PoolBlock{}
	if err := b.UnmarshalBinary(ConsensusDefault, &NilDerivationCache{}, data); err != nil {
		f.Fatal(err)
		return
	}
	buf, err := b.Side.MarshalBinary(b.ShareVersion())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(buf)

	f.Fuzz(func(t *testing.T, buf []byte) {
		b := &SideData{}
		if err := b.UnmarshalBinary(buf, ShareVersion_V1); err != nil {
			t.Skipf("leftover error: %s", err)
			return
		}
		data, err := b.MarshalBinary(ShareVersion_V1)
		if err != nil {
			t.Fatalf("failed to marshal decoded side data: %s", err)
			return
		}
		if !bytes.Equal(data, buf) {
			t.Logf("EXPECTED (len %d):\n%s", len(buf), hex.Dump(buf))
			t.Logf("ACTUAL (len %d):\n%s", len(data), hex.Dump(data))
			t.Fatalf("mismatched roundtrip")
		}
	})
}

func FuzzSideDataRoundTrip_V2(f *testing.F) {
	data, err := os.ReadFile("testdata/v2_block.dat")
	if err != nil {
		f.Fatal(err)
	}
	b := &PoolBlock{}
	if err := b.UnmarshalBinary(ConsensusDefault, &NilDerivationCache{}, data); err != nil {
		f.Fatal(err)
		return
	}
	buf, err := b.Side.MarshalBinary(b.ShareVersion())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(buf)

	f.Fuzz(func(t *testing.T, buf []byte) {
		b := &SideData{}
		if err := b.UnmarshalBinary(buf, ShareVersion_V2); err != nil {
			t.Skipf("leftover error: %s", err)
			return
		}
		data, err := b.MarshalBinary(ShareVersion_V2)
		if err != nil {
			t.Fatalf("failed to marshal decoded side data: %s", err)
			return
		}
		if !bytes.Equal(data, buf) {
			t.Logf("EXPECTED (len %d):\n%s", len(buf), hex.Dump(buf))
			t.Logf("ACTUAL (len %d):\n%s", len(data), hex.Dump(data))
			t.Fatalf("mismatched roundtrip")
		}
	})
}

func FuzzSideDataRoundTrip_V3(f *testing.F) {
	data, err := os.ReadFile("testdata/v4_block.dat")
	if err != nil {
		f.Fatal(err)
	}
	b := &PoolBlock{}
	if err := b.UnmarshalBinary(ConsensusDefault, &NilDerivationCache{}, data); err != nil {
		f.Fatal(err)
		return
	}
	buf, err := b.Side.MarshalBinary(b.ShareVersion())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(buf)

	f.Fuzz(func(t *testing.T, buf []byte) {
		b := &SideData{}
		if err := b.UnmarshalBinary(buf, ShareVersion_V3); err != nil {
			t.Skipf("leftover error: %s", err)
			return
		}
		data, err := b.MarshalBinary(ShareVersion_V3)
		if err != nil {
			t.Fatalf("failed to marshal decoded side data: %s", err)
			return
		}
		if !bytes.Equal(data, buf) {
			t.Logf("EXPECTED (len %d):\n%s", len(buf), hex.Dump(buf))
			t.Logf("ACTUAL (len %d):\n%s", len(data), hex.Dump(data))
			t.Fatalf("mismatched roundtrip")
		}
	})
}
