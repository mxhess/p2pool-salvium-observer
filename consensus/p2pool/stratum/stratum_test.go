package stratum

import (
	"fmt"
	unsafeRandom "math/rand/v2"
	"os"
	"path"
	"runtime"
	"testing"
	"time"
	_ "unsafe"

	"git.gammaspectra.live/P2Pool/consensus/v4/monero"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/address"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/block"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/client"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/crypto"
	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/sidechain"
	p2pooltypes "git.gammaspectra.live/P2Pool/consensus/v4/p2pool/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
	"github.com/ulikunitz/xz"
)

var preLoadedMiniSideChain *sidechain.SideChain

var preLoadedPoolBlock *sidechain.PoolBlock

var submitBlockFunc = func(block *sidechain.PoolBlock) (err error) {
	if blob, err := block.MarshalBinary(); err == nil {
		_, err = client.GetDefaultClient().SubmitBlock(blob)
		return err
	}
	return err
}
var submitMainBlockFunc = func(b *block.Block) (err error) {
	if blob, err := b.MarshalBinary(); err == nil {
		_, err = client.GetDefaultClient().SubmitBlock(blob)
		return err
	}
	return err
}

func init() {
	utils.GlobalLogLevel = 0

	_, filename, _, _ := runtime.Caller(0)
	// The ".." may change depending on you folder structure
	dir := path.Join(path.Dir(filename), "../..")
	err := os.Chdir(dir)
	if err != nil {
		panic(err)
	}

	_ = sidechain.ConsensusDefault.InitHasher(2)
	_ = sidechain.ConsensusMini.InitHasher(2)
	_ = sidechain.ConsensusNano.InitHasher(2)
	client.SetDefaultClientSettings(os.Getenv("MONEROD_RPC_URL"))
}

func getMinerData() *p2pooltypes.MinerData {
	if d, err := client.GetDefaultClient().GetMinerData(); err != nil {
		return nil
	} else {
		return &p2pooltypes.MinerData{
			MajorVersion:          d.MajorVersion,
			Height:                d.Height,
			PrevId:                d.PrevId,
			SeedHash:              d.SeedHash,
			Difficulty:            d.Difficulty,
			MedianWeight:          d.MedianWeight,
			AlreadyGeneratedCoins: d.AlreadyGeneratedCoins,
			MedianTimestamp:       d.MedianTimestamp,
			TimeReceived:          time.Now(),
			TxBacklog:             nil,
		}
	}
}

func TestMain(m *testing.M) {
	client.SetDefaultClientSettings(os.Getenv("MONEROD_RPC_URL"))

	if buf, err := os.ReadFile("testdata/v4_block.dat"); err != nil {
		panic(err)
	} else {
		preLoadedPoolBlock = &sidechain.PoolBlock{}
		if err = preLoadedPoolBlock.UnmarshalBinary(sidechain.ConsensusDefault, &sidechain.NilDerivationCache{}, buf); err != nil {
			panic(err)
		}
	}

	_ = sidechain.ConsensusMini.InitHasher(2)

	preLoadedMiniSideChain = sidechain.NewSideChain(sidechain.GetFakeTestServer(sidechain.ConsensusMini))

	f, err := os.Open("testdata/v4_2_sidechain_dump_mini.dat.xz")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	r, err := xz.NewReader(f)
	if err != nil {
		panic(err)
	}

	if blocks, err := sidechain.LoadSideChainTestData(preLoadedMiniSideChain.Consensus(), preLoadedMiniSideChain.DerivationCache(), r); err != nil {
		panic(err)
	} else {
		for _, b := range blocks {
			// verify externally first without PoW, then add directly
			if _, err, _ = preLoadedMiniSideChain.PoolBlockExternalVerify(b); err != nil {
				panic(err)
			}
			if err = preLoadedMiniSideChain.AddPoolBlock(b); err != nil {
				panic(err)
			}
		}
	}

	code := m.Run()

	os.Exit(code)
}

func TestStratumServer(t *testing.T) {
	stratumServer := NewServer(preLoadedMiniSideChain, submitBlockFunc, submitMainBlockFunc)
	minerData := getMinerData()
	tip := preLoadedMiniSideChain.GetChainTip()
	stratumServer.HandleMinerData(minerData)
	stratumServer.HandleTip(tip)

	func() {
		//Process all incoming changes first
		for {
			select {
			case f := <-stratumServer.incomingChanges:
				if f() {
					stratumServer.Update()
				}
			default:
				return
			}
		}
	}()

	tpl, _, _, seedHash, err := stratumServer.BuildTemplate(address.FromBase58(types.DonationAddress).ToPackedAddress(), false)
	if err != nil {
		t.Fatal(err)
	}

	if seedHash != minerData.SeedHash {
		t.Fatal()
	}

	if tpl.MainHeight != minerData.Height {
		t.Fatal()
	}

	if tpl.MainParent != minerData.PrevId {
		t.Fatal()
	}

	if tpl.SideHeight != (tip.Side.Height + 1) {
		t.Fatal()
	}

	if tpl.SideParent != tip.SideTemplateId(preLoadedMiniSideChain.Consensus()) {
		t.Fatal()
	}
}

func TestStratumServer_GenesisV2(t *testing.T) {
	consensus := sidechain.NewConsensus(sidechain.NetworkMainnet, "test", "", "", 10, 100000, 100, 20)
	consensus.HardForks = []monero.HardFork{
		{uint8(sidechain.ShareVersion_V2), 0, 0, 0},
	}

	err := consensus.InitHasher(1)
	if err != nil {
		t.Fatal(err)
	}
	defer consensus.GetHasher().Close()

	sideChain := sidechain.NewSideChain(sidechain.GetFakeTestServer(consensus))

	stratumServer := NewServer(sideChain, submitBlockFunc, submitMainBlockFunc)
	minerData := getMinerData()
	stratumServer.HandleMinerData(minerData)

	func() {
		//Process all incoming changes first
		for {
			select {
			case f := <-stratumServer.incomingChanges:
				if f() {
					stratumServer.Update()
				}
			default:
				return
			}
		}
	}()

	tpl, _, _, seedHash, err := stratumServer.BuildTemplate(address.FromBase58(types.DonationAddress).ToPackedAddress(), false)
	if err != nil {
		t.Fatal(err)
	}

	if seedHash != minerData.SeedHash {
		t.Fatal()
	}

	if tpl.MainHeight != minerData.Height {
		t.Fatal()
	}

	if tpl.MainParent != minerData.PrevId {
		t.Fatal()
	}

	// verify genesis parameters
	if tpl.SideHeight != 0 {
		t.Fatal()
	}

	if tpl.SideParent != types.ZeroHash {
		t.Fatal()
	}

	hasher := crypto.GetKeccak256Hasher()
	defer crypto.PutKeccak256Hasher(hasher)

	var templateId types.Hash
	tpl.TemplateId(hasher, nil, consensus, 0, 0, nil, nil, p2pooltypes.CurrentSoftwareId, p2pooltypes.CurrentSoftwareVersion, &templateId)
	blockData := tpl.Blob(nil, consensus, 0, 0, 0, 0, templateId, nil, nil, p2pooltypes.CurrentSoftwareId, p2pooltypes.CurrentSoftwareVersion)
	var b sidechain.PoolBlock
	err = b.UnmarshalBinary(consensus, &sidechain.NilDerivationCache{}, blockData)
	if err != nil {
		t.Fatal(err)
	}
	if b.SideTemplateId(consensus) != templateId {
		t.Fatalf("mismatched template id, got %s expected %s", b.SideTemplateId(consensus), templateId)
	}

	if b.Side.CoinbasePrivateKeySeed != consensus.Id {
		t.Fatal()
	}

	if b.Side.CumulativeDifficulty.Cmp64(consensus.MinimumDifficulty) != 0 {
		t.Fatal()
	}

	if b.Side.Difficulty.Cmp64(consensus.MinimumDifficulty) != 0 {
		t.Fatal()
	}
}

func TestStratumServer_GenesisV3(t *testing.T) {
	consensus := sidechain.NewConsensus(sidechain.NetworkMainnet, "test", "", "", 10, 100000, 100, 20)
	consensus.HardForks = []monero.HardFork{
		{uint8(sidechain.ShareVersion_V3), 0, 0, 0},
	}

	err := consensus.InitHasher(1)
	if err != nil {
		t.Fatal(err)
	}
	defer consensus.GetHasher().Close()

	sideChain := sidechain.NewSideChain(sidechain.GetFakeTestServer(consensus))

	stratumServer := NewServer(sideChain, submitBlockFunc, submitMainBlockFunc)
	minerData := getMinerData()
	stratumServer.HandleMinerData(minerData)

	func() {
		//Process all incoming changes first
		for {
			select {
			case f := <-stratumServer.incomingChanges:
				if f() {
					stratumServer.Update()
				}
			default:
				return
			}
		}
	}()

	tpl, _, _, seedHash, err := stratumServer.BuildTemplate(address.FromBase58(types.DonationAddress).ToPackedAddress(), false)
	if err != nil {
		t.Fatal(err)
	}

	if seedHash != minerData.SeedHash {
		t.Fatal()
	}

	if tpl.MainHeight != minerData.Height {
		t.Fatal()
	}

	if tpl.MainParent != minerData.PrevId {
		t.Fatal()
	}

	// verify genesis parameters
	if tpl.SideHeight != 0 {
		t.Fatal()
	}

	if tpl.SideParent != types.ZeroHash {
		t.Fatal()
	}

	hasher := crypto.GetKeccak256Hasher()
	defer crypto.PutKeccak256Hasher(hasher)

	var templateId types.Hash
	tpl.TemplateId(hasher, nil, consensus, 0, 0, nil, nil, p2pooltypes.CurrentSoftwareId, p2pooltypes.CurrentSoftwareVersion, &templateId)
	blockData := tpl.Blob(nil, consensus, 0, 0, 0, 0, templateId, nil, nil, p2pooltypes.CurrentSoftwareId, p2pooltypes.CurrentSoftwareVersion)
	var b sidechain.PoolBlock
	err = b.UnmarshalBinary(consensus, &sidechain.NilDerivationCache{}, blockData)
	if err != nil {
		t.Fatal(err)
	}
	if b.SideTemplateId(consensus) != templateId {
		t.Fatalf("mismatched template id, got %s expected %s", b.SideTemplateId(consensus), templateId)
	}

	if b.Side.CoinbasePrivateKeySeed != consensus.Id {
		t.Fatal()
	}

	if b.Side.CumulativeDifficulty.Cmp64(consensus.MinimumDifficulty) != 0 {
		t.Fatal()
	}

	if b.Side.Difficulty.Cmp64(consensus.MinimumDifficulty) != 0 {
		t.Fatal()
	}

}

func BenchmarkServer_FillTemplate(b *testing.B) {
	stratumServer := NewServer(preLoadedMiniSideChain, submitBlockFunc, submitMainBlockFunc)
	minerData := getMinerData()
	tip := preLoadedMiniSideChain.GetChainTip()
	stratumServer.minerData = minerData
	stratumServer.tip = tip

	b.ResetTimer()

	b.Run("New", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if err := stratumServer.fillNewTemplateData(minerData.Difficulty); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportAllocs()
	})

	b.Run("Cached", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if err := stratumServer.fillNewTemplateData(types.ZeroDifficulty); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportAllocs()
	})

}

func BenchmarkServer_BuildTemplate(b *testing.B) {
	stratumServer := NewServer(preLoadedMiniSideChain, submitBlockFunc, submitMainBlockFunc)
	minerData := getMinerData()
	tip := preLoadedMiniSideChain.GetChainTip()
	stratumServer.minerData = minerData
	stratumServer.tip = tip

	if err := stratumServer.fillNewTemplateData(minerData.Difficulty); err != nil {
		b.Fatal(err)
	}

	const randomPoolSize = 512
	var randomKeys [randomPoolSize]address.PackedAddress

	//generate random keys deterministically
	for i := range randomKeys {
		spendPriv, viewPriv := crypto.DeterministicScalar([]byte(fmt.Sprintf("BenchmarkBuildTemplate_%d_spend", i))), crypto.DeterministicScalar([]byte(fmt.Sprintf("BenchmarkBuildTemplate_%d_spend", i)))
		randomKeys[i][0] = (*crypto.PrivateKeyScalar)(spendPriv).PublicKey().AsBytes()
		randomKeys[i][1] = (*crypto.PrivateKeyScalar)(viewPriv).PublicKey().AsBytes()
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.Run("Cached", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			counter := unsafeRandom.IntN(randomPoolSize)
			for pb.Next() {
				a := randomKeys[counter%randomPoolSize]
				if _, _, _, _, err := stratumServer.BuildTemplate(a, false); err != nil {
					b.Fatal(err)
				}
				counter++
			}
		})
		b.ReportAllocs()
	})

	b.Run("Forced", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			counter := unsafeRandom.IntN(randomPoolSize)
			for pb.Next() {
				a := randomKeys[counter%randomPoolSize]
				if _, _, _, _, err := stratumServer.BuildTemplate(a, true); err != nil {
					b.Fatal(err)
				}
				counter++
			}
		})
		b.ReportAllocs()
	})
}
