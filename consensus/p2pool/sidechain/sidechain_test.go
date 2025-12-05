package sidechain

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/client"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/randomx"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
	"git.gammaspectra.live/P2Pool/go-json"
	"github.com/ulikunitz/xz"
	"io"
	unsafeRandom "math/rand/v2"
	"os"
	"path"
	"runtime"
	"runtime/debug"
	"slices"
	"testing"
)

func init() {
	utils.GlobalLogLevel = 0

	_, filename, _, _ := runtime.Caller(0)
	// The ".." may change depending on you folder structure
	dir := path.Join(path.Dir(filename), "../..")
	err := os.Chdir(dir)
	if err != nil {
		panic(err)
	}

	_ = ConsensusDefault.InitHasher(2)
	_ = ConsensusMini.InitHasher(2)
	_ = ConsensusNano.InitHasher(2)
	client.SetDefaultClientSettings(os.Getenv("MONEROD_RPC_URL"))
}

type TestSideChainData struct {
	Name       string
	Path       string
	Decompress func(io.ReadCloser) (io.Reader, error)

	PatchedBlocks []string

	Consensus *Consensus

	TipSideHeight uint64
	TipMainHeight uint64
}

func (d TestSideChainData) Load() (server *FakeServer, blocks UniquePoolBlockSlice, err error) {
	server = GetFakeTestServer(d.Consensus)

	s := server.SideChain()

	var reader io.Reader

	f, err := os.Open(d.Path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	if d.Decompress != nil {
		reader, err = d.Decompress(f)
		if err != nil {
			return nil, nil, err
		}
		if closer, ok := reader.(io.ReadCloser); ok {
			defer closer.Close()
		}
	} else {
		reader = f
	}

	var patchedBlocks [][]byte

	for _, p := range d.PatchedBlocks {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, err
		}
		patchedBlocks = append(patchedBlocks, data)
	}

	if blocks, err = LoadSideChainTestData(s.Consensus(), s.DerivationCache(), reader, patchedBlocks...); err != nil {
		return nil, nil, err
	} else {
		return server, blocks, nil
	}
}

func gzipDecompress(r io.ReadCloser) (io.Reader, error) {
	return gzip.NewReader(r)
}

func xzDecompress(r io.ReadCloser) (io.Reader, error) {
	return xz.NewReader(r)
}

var DefaultTestSideChainData = getTestSideChain("Default_V4_2")
var MiniTestSideChainData = getTestSideChain("Mini_V4_2")

func getTestSideChain(name string) TestSideChainData {
	if i := slices.IndexFunc(testSideChains, func(data TestSideChainData) bool {
		return data.Name == name
	}); i != -1 {
		return testSideChains[i]
	}
	panic("no test side chain found")
}

var testSideChains = []TestSideChainData{
	{
		Name:          "Default_V4_2",
		Path:          "testdata/v4_2_sidechain_dump.dat.xz",
		Decompress:    xzDecompress,
		Consensus:     ConsensusDefault,
		TipSideHeight: 11704382,
		TipMainHeight: 3456189,
	},
	{
		Name:       "Default_V4",
		Path:       "testdata/v4_sidechain_dump.dat.gz",
		Decompress: gzipDecompress,
		PatchedBlocks: []string{
			//patch in missing blocks that are needed to reach sync range on newer limits
			"testdata/v4_sidechain_dump_9439439.dat",
			"testdata/v4_sidechain_dump_9439438.dat",
			"testdata/v4_sidechain_dump_9439438_1.dat",
			"testdata/v4_sidechain_dump_9439437.dat",
		},
		Consensus:     ConsensusDefault,
		TipSideHeight: 9443762,
		TipMainHeight: 3258121,
	},
	{
		Name:          "Default_V2",
		Path:          "testdata/v2_sidechain_dump.dat.gz",
		Decompress:    gzipDecompress,
		Consensus:     ConsensusDefault,
		TipSideHeight: 4957203,
		TipMainHeight: 2870010,
	},
	{
		Name:          "Default_V1",
		Path:          "testdata/v1_sidechain_dump.dat",
		Consensus:     ConsensusDefault,
		TipSideHeight: 522805,
		TipMainHeight: 2483901,
	},

	{
		Name:          "Mini_V4_2",
		Path:          "testdata/v4_2_sidechain_dump_mini.dat.xz",
		Decompress:    xzDecompress,
		Consensus:     ConsensusMini,
		TipSideHeight: 11207082,
		TipMainHeight: 3456189,
	},
	{
		Name:       "Mini_V4",
		Path:       "testdata/v4_sidechain_dump_mini.dat.gz",
		Decompress: gzipDecompress,
		PatchedBlocks: []string{
			//patch in missing blocks that are needed to reach sync range on newer limits
			"testdata/v4_sidechain_dump_mini_8907744.dat",
			"testdata/v4_sidechain_dump_mini_8907743.dat",
			"testdata/v4_sidechain_dump_mini_8907742.dat",
		},
		Consensus:     ConsensusMini,
		TipSideHeight: 8912067,
		TipMainHeight: 3258121,
	},
	{
		Name:          "Mini_V2",
		Path:          "testdata/v2_sidechain_dump_mini.dat.gz",
		Decompress:    gzipDecompress,
		Consensus:     ConsensusMini,
		TipSideHeight: 4414446,
		TipMainHeight: 2870010,
	},
	{
		Name: "Mini_V1",
		Path: "testdata/v1_sidechain_dump_mini.dat",
		PatchedBlocks: []string{
			//patch in missing blocks that are needed to reach sync range on newer limits
			"testdata/v1_sidechain_dump_mini_2420028.dat",
			"testdata/v1_sidechain_dump_mini_2420027.dat",
			"testdata/v1_sidechain_dump_mini_2420026.dat",
			"testdata/v1_sidechain_dump_mini_2420025.dat",
			"testdata/v1_sidechain_dump_mini_2420024.dat",
		},
		Consensus:     ConsensusMini,
		TipSideHeight: 2424349,
		TipMainHeight: 2696040,
	},

	{
		Name:          "Nano_V4_2",
		Path:          "testdata/v4_2_sidechain_dump_nano.dat.xz",
		Decompress:    xzDecompress,
		Consensus:     ConsensusNano,
		TipSideHeight: 188542,
		TipMainHeight: 3456189,
	},
	{
		Name:       "Nano_V4",
		Path:       "testdata/v4_sidechain_dump_nano.dat.gz",
		Decompress: gzipDecompress,
		PatchedBlocks: []string{
			//patch in missing blocks that are needed to reach sync range on newer limits
			"testdata/v4_sidechain_dump_nano_112327.dat",
			"testdata/v4_sidechain_dump_nano_112326.dat",
		},
		Consensus:     ConsensusNano,
		TipSideHeight: 116651,
		TipMainHeight: 3438036,
	},
}

func TestSideChainFullSync(t *testing.T) {

	if testing.Short() {
		t.Skip("Skipping full mode with -short")
	}

	oldLogLevel := utils.GlobalLogLevel
	defer func() {
		utils.GlobalLogLevel = oldLogLevel
	}()
	utils.GlobalLogLevel = utils.LogLevelInfo | utils.LogLevelNotice | utils.LogLevelError

	sideChain := DefaultTestSideChainData

	if os.Getenv("CI") == "" {
		// set full hasher
		consensusBuf, err := json.Marshal(sideChain.Consensus)
		if err != nil {
			t.Fatal(err)
		}
		consensus, err := NewConsensusFromJSON(consensusBuf)
		if err != nil {
			t.Fatal(err)
		}

		// do this to create a new full hasher
		sideChain.Consensus = consensus
		err = sideChain.Consensus.InitHasher(2, randomx.FlagSecure, randomx.FlagFullMemory)
		if err != nil {
			t.Fatal(err)
		}
	}

	server, blocks, err := sideChain.Load()
	if err != nil {
		t.Fatal(err)
	}

	s := server.SideChain()

	var tip *PoolBlock

	for _, b := range blocks {
		if tip == nil {
			tip = b
			continue
		}

		if tip.Side.Height < b.Side.Height {
			tip = b
		} else if tip.Side.Height == b.Side.Height {
			//determinism
			if tip.SideTemplateId(s.Consensus()).Compare(b.SideTemplateId(s.Consensus())) < 0 {
				tip = b
			}
		}
	}

	if tip == nil {
		t.Fatal("no tip")
	}

	err = server.DownloadMinimalBlockHeaders(tip.Main.Coinbase.GenHeight)
	if err != nil {
		t.Fatal(err)
	}

	var blocksToAdd UniquePoolBlockSlice
	blocksToAdd = append(blocksToAdd, tip)

	for len(blocksToAdd) > 0 {
		block := blocksToAdd[0]
		blocksToAdd = blocksToAdd[1:]
		missingBlocks, err, _ := s.AddPoolBlockExternal(block)
		if err != nil {
			t.Fatal(err)
		}

		block = nil
		for _, id := range missingBlocks {
			if b := blocks.Get(s.Consensus(), id); b != nil {
				if blocksToAdd.Get(s.Consensus(), id) == nil {
					blocksToAdd = append(blocksToAdd, b)
				}
			}
		}
	}

	tip = s.GetChainTip()

	if tip == nil {
		t.Fatalf("GetChainTip() returned nil")
	}

	if !tip.Verified.Load() {
		t.Fatalf("tip is not verified")
	}

	if tip.Invalid.Load() {
		t.Fatalf("tip is invalid")
	}

	if tip.Main.Coinbase.GenHeight != sideChain.TipMainHeight {
		t.Fatalf("tip main height mismatch %d != %d", tip.Main.Coinbase.GenHeight, sideChain.TipMainHeight)
	}
	if tip.Side.Height != sideChain.TipSideHeight {
		t.Fatalf("tip side height mismatch %d != %d", tip.Side.Height, sideChain.TipSideHeight)
	}
}

func FuzzSideChain_Mini_Shuffle_V4(f *testing.F) {
	// Fuzz shuffle insertion, does need to sync

	_, blocks, err := MiniTestSideChainData.Load()
	if err != nil {
		f.Fatal(err)
	}

	indices := make([]byte, len(blocks)*2)
	for i := range blocks {
		binary.LittleEndian.PutUint16(indices[i*2:], uint16(i))
	}

	f.Add(indices)

	f.Fuzz(func(t *testing.T, ix []byte) {
		if len(ix)%2 != 0 || len(ix) > len(indices) {
			t.SkipNow()
		}

		curBlocks := slices.Clone(blocks)

		for i := 0; i < len(ix); i += 2 {
			n := binary.LittleEndian.Uint16(ix[i:])
			ii := i / 2
			// wrap around index to ease edge finding
			curBlocks[ii], curBlocks[int(n)%len(blocks)] = curBlocks[int(n)%len(blocks)], curBlocks[ii]
		}

		server := GetFakeTestServer(MiniTestSideChainData.Consensus)
		s := server.SideChain()

		for _, b := range curBlocks {
			if b == nil {
				continue
			}
			// verify externally first without PoW, then add directly
			if _, err, _ = s.PoolBlockExternalVerify(b); err != nil {
				t.Fatalf("pool block external verify failed: %s", err)
			}
			if err = s.AddPoolBlock(b); err != nil {
				t.Fatalf("add pool block failed: %s", err)
			}
		}

		tip := s.GetChainTip()

		if tip == nil {
			t.Fatalf("GetChainTip() returned nil")
		}

		if !tip.Verified.Load() {
			t.Fatalf("tip is not verified")
		}

		if tip.Invalid.Load() {
			t.Fatalf("tip is invalid")
		}
	})
}

func FuzzSideChain_AddPoolBlockExternal(f *testing.F) {
	debug.SetMemoryLimit(1024 * 1024 * 1024 * 8)
	debug.SetMaxStack(1024 * 1024 * 256)

	// Fuzz shuffle insertion, does need to sync

	server, blocks, err := MiniTestSideChainData.Load()
	if err != nil {
		f.Fatal(err)
	}

	s := server.SideChain()

	var knownPoolBlocks []types.Hash
	for _, b := range blocks {
		// verify externally first without PoW, then add directly
		if _, err, _ = s.PoolBlockExternalVerify(b); err != nil {
			f.Fatalf("pool block external verify failed: %s", err)
		}
		if err = s.AddPoolBlock(b); err != nil {
			f.Fatalf("add pool block failed: %s", err)
		}
		data, err := b.MarshalBinary()
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
		knownPoolBlocks = append(knownPoolBlocks, b.SideTemplateId(s.Consensus()))
	}

	f.Fuzz(func(t *testing.T, buf []byte) {
		b := &PoolBlock{}
		reader := bytes.NewReader(buf)
		if err := b.FromReader(s.Consensus(), &NilDerivationCache{}, reader); err != nil {
			t.Skipf("leftover error: %s", err)
		}
		if reader.Len() > 0 {
			//clamp comparison
			buf = buf[:len(buf)-reader.Len()]
		}

		templateId := b.SideTemplateId(s.Consensus())
		if templateId == types.ZeroHash {
			t.Fatalf("template id should not be zero")
		}
		defer func() {
			// cleanup
			delete(s.blocksByTemplateId, templateId)
			delete(s.blocksByMerkleRoot, b.MergeMiningTag().RootHash)
			delete(s.seenBlocks, b.FullId(s.Consensus()))
		}()

		// verify externally first without PoW, then add directly
		if _, err, _ = s.PoolBlockExternalVerify(b); err != nil {
			if errors.Is(err, ErrPanic) {
				t.Fatal(err)
			}
			t.Skip(err)
		}
		if err = s.AddPoolBlock(b); err != nil {
			if errors.Is(err, ErrPanic) {
				t.Fatal(err)
			}
			t.Skip(err)
		}

		if b.Verified.Load() {
			if !slices.Contains(knownPoolBlocks, b.SideTemplateId(s.Consensus())) {
				t.Fatal("block is verified")
			}
		}
	})
}

func FuzzSideChain_AddPoolBlockExternal_PrunedCompact(f *testing.F) {
	debug.SetMemoryLimit(1024 * 1024 * 1024 * 8)
	debug.SetMaxStack(1024 * 1024 * 256)
	// Fuzz shuffle insertion, does need to sync

	server, blocks, err := MiniTestSideChainData.Load()
	if err != nil {
		f.Fatal(err)
	}

	s := server.SideChain()

	var knownPoolBlocks []types.Hash
	for _, b := range blocks {
		// verify externally first without PoW, then add directly
		if _, err, _ = s.PoolBlockExternalVerify(b); err != nil {
			f.Fatalf("pool block external verify failed: %s", err)
		}
		if err = s.AddPoolBlock(b); err != nil {
			f.Fatalf("add pool block failed: %s", err)
		}
	}

	tip := s.GetChainTip()
	if tip == nil {
		f.Fatalf("GetChainTip() returned nil")
	}

	for _, b := range blocks {
		buf, err := b.MarshalBinary()
		if err != nil {
			f.Fatal(err)
		}
		b2 := &PoolBlock{}
		if err := b2.UnmarshalBinary(s.Consensus(), &NilDerivationCache{}, buf); err != nil {
			f.Fatal(err)
		}
		// force certain properties by default to assist
		tip2 := s.GetPoolBlocksByHeight(tip.Side.Height - unsafeRandom.Uint64N(100))[0]
		b2.Side.Parent = tip2.SideTemplateId(s.Consensus())
		b2.Side.Height = tip2.Side.Height + 1
		b2.Side.MerkleProof = []types.Hash{b2.SideTemplateId(s.Consensus())}
		// add pruned and unpruned
		data, err := b2.MarshalBinaryFlags(false, true)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
		data, err = b2.MarshalBinaryFlags(true, true)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
		knownPoolBlocks = append(knownPoolBlocks, b2.SideTemplateId(s.Consensus()))
	}

	f.Fuzz(func(t *testing.T, buf []byte) {
		b := &PoolBlock{}
		reader := bytes.NewReader(buf)
		if err := b.FromCompactReader(s.Consensus(), &NilDerivationCache{}, reader); err != nil {
			t.Skipf("leftover error: %s", err)
		}

		if reader.Len() > 0 {
			t.Skipf("leftover data should be empty")
		}

		templateId := b.SideTemplateId(s.Consensus())
		if templateId == types.ZeroHash {
			t.Skipf("template id should not be zero")
		}
		defer func() {
			// cleanup
			delete(s.blocksByTemplateId, templateId)
			delete(s.blocksByMerkleRoot, b.MergeMiningTag().RootHash)
			delete(s.seenBlocks, b.FullId(s.Consensus()))
		}()

		// verify externally first without PoW, then add directly
		if _, err, _ = s.PoolBlockExternalVerify(b); err != nil {
			if errors.Is(err, ErrPanic) {
				t.Fatal(err)
			}
			t.Skip(err)
		}
		if err = s.AddPoolBlock(b); err != nil {
			if errors.Is(err, ErrPanic) {
				t.Fatal(err)
			}
			t.Skip(err)
		}

		if b.Verified.Load() {
			if !slices.Contains(knownPoolBlocks, b.SideTemplateId(s.Consensus())) {
				t.Fatal("block is verified")
			}
		}
	})
}

func FuzzSideChain_Default_Shuffle_V4(f *testing.F) {
	// Fuzz shuffle insertion, does need to sync

	_, blocks, err := DefaultTestSideChainData.Load()
	if err != nil {
		f.Fatal(err)
	}

	indices := make([]byte, len(blocks)*2)
	for i := range blocks {
		binary.LittleEndian.PutUint16(indices[i*2:], uint16(i))
	}

	f.Add(indices)

	f.Fuzz(func(t *testing.T, ix []byte) {
		if len(ix)%2 != 0 || len(ix) > len(indices) {
			t.SkipNow()
		}

		curBlocks := slices.Clone(blocks)

		for i := 0; i < len(ix); i += 2 {
			n := binary.LittleEndian.Uint16(ix[i:])
			ii := i / 2
			// wrap around index to ease edge finding
			curBlocks[ii], curBlocks[int(n)%len(blocks)] = curBlocks[int(n)%len(blocks)], curBlocks[ii]
		}

		server := GetFakeTestServer(DefaultTestSideChainData.Consensus)
		s := server.SideChain()

		for _, b := range curBlocks {
			if b == nil {
				continue
			}
			// verify externally first without PoW, then add directly
			if _, err, _ = s.PoolBlockExternalVerify(b); err != nil {
				t.Fatalf("pool block external verify failed: %s", err)
			}
			if err = s.AddPoolBlock(b); err != nil {
				t.Fatalf("add pool block failed: %s", err)
			}
		}

		tip := s.GetChainTip()

		if tip == nil {
			t.Fatalf("GetChainTip() returned nil")
		}

		if !tip.Verified.Load() {
			t.Fatalf("tip is not verified")
		}

		if tip.Invalid.Load() {
			t.Fatalf("tip is invalid")
		}
	})
}

func FuzzSideChain_Default_Random_V4(f *testing.F) {
	// Fuzz random insertion, does not need to sync

	_, blocks, err := DefaultTestSideChainData.Load()
	if err != nil {
		f.Fatal(err)
	}

	indices := make([]byte, len(blocks)*2)
	for i := range blocks {
		binary.LittleEndian.PutUint16(indices[i*2:], uint16(i))
	}

	f.Add(indices)

	f.Fuzz(func(t *testing.T, ix []byte) {
		if len(ix)%2 != 0 {
			t.SkipNow()
		}

		curBlocks := make([]*PoolBlock, 0, len(blocks))

		for i := 0; i < len(ix); i += 2 {
			n := binary.LittleEndian.Uint16(ix[i:])

			// wrap around index to ease edge finding
			curBlocks = append(curBlocks, blocks[int(n)%len(blocks)])
		}

		server := GetFakeTestServer(DefaultTestSideChainData.Consensus)
		s := server.SideChain()

		for _, b := range curBlocks {
			if b == nil {
				continue
			}
			// verify externally first without PoW, then add directly
			if _, err, _ = s.PoolBlockExternalVerify(b); err != nil {
				t.Fatalf("pool block external verify failed: %s", err)
			}
			if err = s.AddPoolBlock(b); err != nil {
				t.Fatalf("add pool block failed: %s", err)
			}
		}
	})
}

func TestSideChain(t *testing.T) {
	for _, sideChain := range testSideChains {
		t.Run(sideChain.Name, func(t *testing.T) {
			server, blocks, err := sideChain.Load()
			if err != nil {
				t.Fatal(err)
			}

			var shuffleSeed types.Hash
			_, _ = rand.Read(shuffleSeed[:])

			t.Logf("shuffle seed = %x", shuffleSeed.Slice())

			// Shuffle blocks. This allows testing proper reorg
			ShuffleSequence(ShareVersion_V2, shuffleSeed, len(blocks), func(i, j int) {
				blocks[i], blocks[j] = blocks[j], blocks[i]
			})

			s := server.SideChain()

			for _, b := range blocks {
				// verify externally first without PoW, then add directly
				if _, err, _ = s.PoolBlockExternalVerify(b); err != nil {
					t.Fatalf("pool block external verify failed: %s", err)
				}
				if err = s.AddPoolBlock(b); err != nil {
					t.Fatalf("add pool block failed: %s", err)
				}
			}

			tip := s.GetChainTip()

			if tip == nil {
				missing := s.GetMissingBlocks()
				for _, b := range missing {
					t.Errorf("missing block %x", b.Slice())
				}
				t.Fatalf("GetChainTip() returned nil")
			}

			if !tip.Verified.Load() {
				t.Fatalf("tip is not verified")
			}

			if tip.Invalid.Load() {
				t.Fatalf("tip is invalid")
			}

			if tip.Main.Coinbase.GenHeight != sideChain.TipMainHeight {
				t.Fatalf("tip main height mismatch %d != %d", tip.Main.Coinbase.GenHeight, sideChain.TipMainHeight)
			}
			if tip.Side.Height != sideChain.TipSideHeight {
				t.Fatalf("tip side height mismatch %d != %d", tip.Side.Height, sideChain.TipSideHeight)
			}

			hits, misses := s.DerivationCache().ephemeralPublicKeyCache.Stats()
			total := max(1, hits+misses)
			t.Logf("Ephemeral Public Key Cache hits = %d (%.02f%%), misses = %d (%.02f%%), total = %d", hits, (float64(hits)/float64(total))*100, misses, (float64(misses)/float64(total))*100, total)

			hits, misses = s.DerivationCache().deterministicKeyCache.Stats()
			total = max(1, hits+misses)
			t.Logf("Deterministic Key Cache hits = %d (%.02f%%), misses = %d (%.02f%%), total = %d", hits, (float64(hits)/float64(total))*100, misses, (float64(misses)/float64(total))*100, total)

			hits, misses = s.DerivationCache().derivationCache.Stats()
			total = max(1, hits+misses)
			t.Logf("Derivation Cache hits = %d (%.02f%%), misses = %d (%.02f%%), total = %d", hits, (float64(hits)/float64(total))*100, misses, (float64(misses)/float64(total))*100, total)

			hits, misses = s.DerivationCache().pubKeyToTableCache.Stats()
			total = max(1, hits+misses)
			t.Logf("PubKeyToTable Key Cache hits = %d (%.02f%%), misses = %d (%.02f%%), total = %d", hits, (float64(hits)/float64(total))*100, misses, (float64(misses)/float64(total))*100, total)

			hits, misses = s.DerivationCache().pubKeyToPointCache.Stats()
			total = max(1, hits+misses)
			t.Logf("PubKeyToPoint Key Cache hits = %d (%.02f%%), misses = %d (%.02f%%), total = %d", hits, (float64(hits)/float64(total))*100, misses, (float64(misses)/float64(total))*100, total)

		})
	}
}

func benchmarkResetState(tip, parent *PoolBlock, templateId, merkleRoot types.Hash, fullId FullId, difficulty types.Difficulty, blocksByHeightKeys []uint64, s *SideChain) {
	//Remove states in maps
	delete(s.blocksByHeight, tip.Side.Height)
	s.blocksByHeightKeys = blocksByHeightKeys
	s.blocksByHeightKeysSorted = true
	delete(s.blocksByTemplateId, templateId)
	delete(s.blocksByMerkleRoot, merkleRoot)
	delete(s.seenBlocks, fullId)

	// Update tip and depths
	tip.Depth.Store(0)
	parent.Depth.Store(0)
	s.chainTip.Store(parent)
	s.syncTip.Store(parent)
	s.currentDifficulty.Store(&difficulty)
	s.updateDepths(parent)

	// Update verification state
	tip.Verified.Store(false)
	tip.Invalid.Store(false)
	tip.iterationCache = nil

	// Update cache
	tip.cache.templateId.Store(nil)
}

func benchSideChain(b *testing.B, s *SideChain, tipHash types.Hash) {
	b.StopTimer()

	tip := s.GetChainTip()

	for tip.SideTemplateId(s.Consensus()) != tipHash {
		delete(s.blocksByHeight, tip.Side.Height)
		s.blocksByHeightKeys = slices.DeleteFunc(s.blocksByHeightKeys, func(u uint64) bool {
			return u == tip.Side.Height
		})
		delete(s.blocksByTemplateId, tip.SideTemplateId(s.Consensus()))
		delete(s.blocksByMerkleRoot, tip.MergeMiningTag().RootHash)
		delete(s.seenBlocks, tip.FullId(s.Consensus()))

		tip = s.GetParent(tip)
		if tip == nil {
			b.Error("nil tip")
			return
		}
	}
	templateId := tip.SideTemplateId(s.Consensus())
	merkleRoot := tip.MergeMiningTag().RootHash
	fullId := tip.FullId(s.Consensus())

	parent := s.GetParent(tip)

	difficulty, _, _ := s.GetDifficulty(parent)

	slices.Sort(s.blocksByHeightKeys)
	blocksByHeightKeys := slices.DeleteFunc(s.blocksByHeightKeys, func(u uint64) bool {
		return u == tip.Side.Height
	})

	benchmarkResetState(tip, parent, templateId, merkleRoot, fullId, difficulty, slices.Clone(blocksByHeightKeys), s)

	var err error

	b.StartTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		benchmarkResetState(tip, parent, templateId, merkleRoot, fullId, difficulty, slices.Clone(blocksByHeightKeys), s)
		b.StartTimer()
		_, err, _ = s.AddPoolBlockExternal(tip)
		if err != nil {
			b.Error(err)
			return
		}
	}
}

var benchLoadedSideChain *SideChain

func TestMain(m *testing.M) {

	var isBenchmark bool
	for _, arg := range os.Args {
		if arg == "-test.bench" {
			isBenchmark = true
		}
	}

	if isBenchmark {

		sideChainData := getTestSideChain("Default_V2")

		server, blocks, err := sideChainData.Load()
		if err != nil {
			panic(err)
		}

		benchLoadedSideChain = server.SideChain()

		for _, b := range blocks {
			// verify externally first without PoW, then add directly
			if _, err, _ = benchLoadedSideChain.PoolBlockExternalVerify(b); err != nil {
				panic(err)
			}
			if err = benchLoadedSideChain.AddPoolBlock(b); err != nil {
				panic(err)
			}
		}

		tip := benchLoadedSideChain.GetChainTip()

		// Pre-calculate PoW
		for i := 0; i < 5; i++ {
			_, err = tip.PowHashWithError(benchLoadedSideChain.Consensus().GetHasher(), benchLoadedSideChain.getSeedByHeightFunc())
			if err != nil {
				panic(err)
			}
			tip = benchLoadedSideChain.GetParent(tip)
		}
	}

	os.Exit(m.Run())
}

func BenchmarkSideChainDefault_AddPoolBlockExternal(b *testing.B) {
	b.ReportAllocs()
	benchSideChain(b, benchLoadedSideChain, types.MustHashFromString("61ecfc1c7738eacd8b815d2e28f124b31962996ae3af4121621b5c5501f19c5d"))
}

func BenchmarkSideChainDefault_GetDifficulty(b *testing.B) {
	b.ReportAllocs()
	tip := benchLoadedSideChain.GetChainTip()

	b.ResetTimer()
	var verifyError, invalidError error
	for i := 0; i < b.N; i++ {
		_, verifyError, invalidError = benchLoadedSideChain.getDifficulty(tip)
		if verifyError != nil {
			b.Error(verifyError)
			return
		}
		if invalidError != nil {
			b.Error(invalidError)
			return
		}
	}
}

func BenchmarkSideChainDefault_CalculateOutputs(b *testing.B) {
	b.ReportAllocs()
	tip := benchLoadedSideChain.GetChainTip()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		outputs, _, _ := CalculateOutputs(tip, benchLoadedSideChain.Consensus(), benchLoadedSideChain.server.GetDifficultyByHeight, benchLoadedSideChain.getPoolBlockByTemplateId, benchLoadedSideChain.derivationCache, benchLoadedSideChain.preAllocatedShares, benchLoadedSideChain.preAllocatedRewards)
		if outputs == nil {
			b.Error("nil outputs")
			return
		}
	}
}

func BenchmarkSideChainDefault_GetShares(b *testing.B) {
	b.ReportAllocs()
	tip := benchLoadedSideChain.GetChainTip()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		shares, _, _ := benchLoadedSideChain.getShares(tip, benchLoadedSideChain.preAllocatedShares)
		if shares == nil {
			b.Error("nil shares")
			return
		}
	}
}

func BenchmarkSideChainDefault_SplitReward(b *testing.B) {
	b.ReportAllocs()
	tip := benchLoadedSideChain.GetChainTip()

	shares, _, _ := benchLoadedSideChain.getShares(tip, benchLoadedSideChain.preAllocatedShares)
	preAllocatedRewards := make([]uint64, 0, len(shares))

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		rewards := SplitReward(preAllocatedRewards, tip.Main.Coinbase.AuxiliaryData.TotalReward, shares)
		if rewards == nil {
			b.Error("nil rewards")
			return
		}
	}
}

func BenchmarkSideChainDefault_BlocksInPPLNSWindow(b *testing.B) {
	b.ReportAllocs()
	tip := benchLoadedSideChain.GetChainTip()

	b.ResetTimer()

	var err error
	for i := 0; i < b.N; i++ {
		_, err = BlocksInPPLNSWindow(tip, benchLoadedSideChain.Consensus(), benchLoadedSideChain.server.GetDifficultyByHeight, benchLoadedSideChain.getPoolBlockByTemplateId, func(b *PoolBlock, weight types.Difficulty) {

		})
		if err != nil {
			b.Error(err)
			return
		}
	}
}
