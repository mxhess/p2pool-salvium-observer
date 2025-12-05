package sidechain

import (
	"bytes"
	"context"
	"encoding/hex"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/client"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/crypto"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/randomx"
	types2 "git.gammaspectra.live/P2Pool/consensus/v4/p2pool/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
	unsafeRandom "math/rand/v2"
	"net/netip"
	"os"
	"testing"
	"time"
)

func testPoolBlock(b *PoolBlock, t *testing.T, expectedBufferLength int, majorVersion, minorVersion uint8, sideHeight uint64, nonce uint32, templateId, mainId, expectedPowHash, privateKeySeed, coinbaseId types.Hash, privateKey crypto.PrivateKeyBytes) {
	if buf, _ := b.Main.MarshalBinary(); len(buf) != expectedBufferLength {
		t.Fatal()
	}

	powHash := b.PowHash(ConsensusDefault.GetHasher(), func(height uint64) (hash types.Hash) {
		seedHeight := randomx.SeedHeight(height)
		if h, err := client.GetDefaultClient().GetBlockByHeight(seedHeight, context.Background()); err != nil {
			return types.ZeroHash
		} else {
			return h.BlockHeader.Hash
		}
	})

	if b.Main.MajorVersion != majorVersion || b.Main.MinorVersion != minorVersion {
		t.Fatal()
	}

	if b.Side.Height != sideHeight {
		t.Fatal()
	}

	if b.Main.Nonce != nonce {
		t.Fatal()
	}

	if b.SideTemplateId(ConsensusDefault) != templateId {
		t.Logf("%s != %s", b.SideTemplateId(ConsensusDefault), templateId)
		t.Fatal()
	}

	if b.FullId(ConsensusDefault).TemplateId() != templateId {
		t.Logf("%s != %s", b.FullId(ConsensusDefault).TemplateId(), templateId)
		t.Fatal()
	}

	if b.MainId() != mainId {
		t.Logf("%s != %s", b.MainId(), mainId)
		t.Fatal()
	}

	if powHash != expectedPowHash {
		t.Fatal()
	}

	if !b.IsProofHigherThanDifficulty(ConsensusDefault.GetHasher(), func(height uint64) (hash types.Hash) {
		seedHeight := randomx.SeedHeight(height)
		if h, err := client.GetDefaultClient().GetBlockByHeight(seedHeight, context.Background()); err != nil {
			return types.ZeroHash
		} else {
			return h.BlockHeader.Hash
		}
	}) {
		t.Fatal()
	}

	if b.Side.CoinbasePrivateKey != privateKey {
		t.Fatal()
	}

	if b.Side.CoinbasePrivateKeySeed != privateKeySeed {
		t.Fatal()
	}

	if b.CoinbaseId() != coinbaseId {
		t.Fatal()
	}
}

func TestPoolBlockMetadata(t *testing.T) {
	meta := PoolBlockReceptionMetadata{
		LocalTime:       time.Now().UTC(),
		AddressPort:     netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), 1234),
		PeerId:          unsafeRandom.Uint64(),
		SoftwareId:      uint32(types2.CurrentSoftwareId),
		SoftwareVersion: uint32(types2.CurrentSoftwareVersion),
	}

	blob, err := meta.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	var meta2 PoolBlockReceptionMetadata
	err = meta2.UnmarshalBinary(blob)
	if err != nil {
		t.Fatal(err)
	}

	if meta != meta2 {
		t.Errorf("different metadata: %v, %v", meta, meta2)
	}
}

func TestPoolBlockDecodeV4(t *testing.T) {

	if buf, err := os.ReadFile("testdata/v4_block.dat"); err != nil {
		t.Fatal(err)
	} else {
		b := &PoolBlock{}
		if err = b.UnmarshalBinary(ConsensusDefault, &NilDerivationCache{}, buf); err != nil {
			t.Fatal(err)
		}

		testPoolBlock(b, t,
			1829,
			16,
			16,
			9443384,
			352454720,
			types.MustHashFromString("53041164e1b220246b06037a25ee200f35d6c8d1f4086a8f04f558b16e23c5aa"),
			types.MustHashFromString("d2d9fe65331cc2194f486f77bec9a898564fbd1522e6f8bc0254d6bcc578b7ea"),
			types.MustHashFromString("0906c001cc0900098fe1b62593f8ba52bd1ae2a0806096aa361a9f1702000000"),
			types.MustHashFromString("3351277ffdfc4ed2321f148b061d27013ea696c4bff9742dd2c6268aa24ec79f"),
			types.MustHashFromString("60d8a8ecc49d9801dde848e74b4089c3f1fbb57c043fd8083aae357ac6868eae"),
			crypto.PrivateKeyBytes(types.MustHashFromString("aa2d1b60be7daafd95985c0a6c859f5dd1c7dbe8540dd56d015d2aec9d4f6f0c")),
		)
	}
}

func TestPoolBlockDecodeV2(t *testing.T) {

	if buf, err := os.ReadFile("testdata/v2_block.dat"); err != nil {
		t.Fatal(err)
	} else {
		b := &PoolBlock{}
		if err = b.UnmarshalBinary(ConsensusDefault, &NilDerivationCache{}, buf); err != nil {
			t.Fatal(err)
		}

		testPoolBlock(b, t,
			1757,
			16,
			16,
			4674483,
			1247,
			types.MustHashFromString("15ecd7e99e78e93bf8bffb1f597046cfa2d604decc32070e36e3caca01597c7d"),
			types.MustHashFromString("fc63db007b94b3eba51bbc6e2076b0ac37b49ea554cc310c5e0fa595889960f3"),
			types.MustHashFromString("aa7a3c4a2d67cb6a728e244288219bf038024f3b511b0da197a19ec601000000"),
			types.MustHashFromString("fd7b5f77c79e624e26b939028a15f14b250fdb217cd40b4ce524eab9b17082ca"),
			types.MustHashFromString("b52a9222eb2712c43742ed3a598df34c821bfb5d3b5a25d41bb4bdb014505ca9"),
			crypto.PrivateKeyBytes(types.MustHashFromString("ba757262420e8bfa7c1931c75da051955cd2d4dff35dff7bfff42a1569941405")),
		)
	}
}

func TestPoolBlockDecodeV1(t *testing.T) {

	if buf, err := os.ReadFile("testdata/v1_mainnet_test2_block.dat"); err != nil {
		t.Fatal(err)
	} else {
		b := &PoolBlock{}
		if err = b.UnmarshalBinary(ConsensusDefault, &NilDerivationCache{}, buf); err != nil {
			t.Fatal(err)
		}

		testPoolBlock(b, t,
			5607,
			14,
			14,
			53450,
			2432795907,
			types.MustHashFromString("bd39e2870edfd54838fe690f70178bfe4f433198ae0b3c8b0725bdbbddf7ed57"),
			types.MustHashFromString("5023d36d54141efae5895eae3d51478fe541c5898ad4d783cef33118b67051f2"),
			types.MustHashFromString("f76d731c61c9c9b6c3f46be2e60c9478930b49b4455feecd41ecb9420d000000"),
			types.ZeroHash,
			types.MustHashFromString("09f4ead399cd8357eff7403562e43fe472f79e47deb39148ff3d681fc8f113ea"),
			crypto.PrivateKeyBytes(types.MustHashFromString("fed259c99cb7d21ac94ade82f2909ad1ccabdeafd16eeb60db4ca5eedca86a0a")),
		)
	}
}

var fuzzPoolBlocks = []string{
	"testdata/v4_block.dat",
	"testdata/v2_block.dat",
	"testdata/v1_mainnet_test2_block.dat",
}

func FuzzPoolBlockDecode(f *testing.F) {
	for _, path := range fuzzPoolBlocks {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}

	f.Fuzz(func(t *testing.T, buf []byte) {
		b := &PoolBlock{}
		if err := b.UnmarshalBinary(ConsensusDefault, &NilDerivationCache{}, buf); err != nil {
			t.Skipf("leftover error: %s", err)
		}
	})
}

func FuzzPoolBlockRoundTrip(f *testing.F) {
	for _, path := range fuzzPoolBlocks {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}

	f.Fuzz(func(t *testing.T, buf []byte) {
		b := &PoolBlock{}
		reader := bytes.NewReader(buf)
		err := b.FromReader(ConsensusDefault, &NilDerivationCache{}, reader)
		if err != nil {
			t.Skipf("leftover error: %s", err)
		}
		if reader.Len() > 0 {
			//clamp comparison
			buf = buf[:len(buf)-reader.Len()]
		}

		if b.FastSideTemplateId(ConsensusDefault) != b.SideTemplateId(ConsensusDefault) {
			t.Logf("mismatched side template ids")
		}
		_ = b.FullId(ConsensusDefault)
		_ = b.CalculateTransactionPrivateKeySeed()
		_ = b.MainId()

		data, err := b.MarshalBinary()
		if err != nil {
			t.Fatalf("failed to marshal decoded block: %s", err)
		}
		if !bytes.Equal(data, buf) {
			t.Logf("EXPECTED (len %d):\n%s", len(buf), hex.Dump(buf))
			t.Logf("ACTUAL (len %d):\n%s", len(data), hex.Dump(data))
			t.Fatalf("mismatched roundtrip")
		}

		if b.BufferLength() != len(buf) {
			t.Fatal("buffer length mismatch")
		}

	})
}

func FuzzPoolBlockRoundTripJSON(f *testing.F) {
	for _, path := range fuzzPoolBlocks {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}

	f.Fuzz(func(t *testing.T, buf []byte) {
		b := &PoolBlock{}
		reader := bytes.NewReader(buf)
		err := b.FromReader(ConsensusDefault, &NilDerivationCache{}, reader)
		if err != nil {
			t.Skipf("leftover error: %s", err)
		}
		if reader.Len() > 0 {
			//clamp comparison
			buf = buf[:len(buf)-reader.Len()]
		}

		bId := b.FastSideTemplateId(ConsensusDefault)

		if bId != b.SideTemplateId(ConsensusDefault) {
			t.Skipf("mismatched side template ids")
		}
		_ = b.FullId(ConsensusDefault)
		_ = b.CalculateTransactionPrivateKeySeed()
		_ = b.MainId()

		blockJSON, err := utils.MarshalJSON(b)
		if err != nil {
			t.Fatalf("failed to marshal block: %s", err)
		}

		b2 := &PoolBlock{}
		err = utils.UnmarshalJSON(blockJSON, b2)
		if err != nil {
			t.Fatalf("failed to unmarshal block: %s", err)
		}

		if b2.FastSideTemplateId(ConsensusDefault) != b2.SideTemplateId(ConsensusDefault) {
			t.Logf("mismatched side template ids")
		}
		if b.FastSideTemplateId(ConsensusDefault) != b2.FastSideTemplateId(ConsensusDefault) {
			t.Fatalf("mismatched side template ids from blocks")
		}
		_ = b2.FullId(ConsensusDefault)
		_ = b2.CalculateTransactionPrivateKeySeed()
		_ = b2.MainId()

		data, err := b2.MarshalBinary()
		if err != nil {
			t.Fatalf("failed to marshal decoded block: %s", err)
		}
		if !bytes.Equal(data, buf) {
			t.Logf("EXPECTED (len %d):\n%s", len(buf), hex.Dump(buf))
			t.Logf("ACTUAL (len %d):\n%s", len(data), hex.Dump(data))
			t.Fatalf("mismatched roundtrip")
		}

		if b2.BufferLength() != len(data) {
			t.Fatal("buffer length mismatch")
		}

	})
}
