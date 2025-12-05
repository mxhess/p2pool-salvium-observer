package stratum

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	unsafeRandom "math/rand/v2"
	"net"
	"net/netip"
	"slices"
	"sync"
	"syscall"
	"time"

	"git.gammaspectra.live/P2Pool/consensus/v4/merge_mining"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/address"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/block"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/crypto"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/transaction"
	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/mempool"
	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/sidechain"
	p2pooltypes "git.gammaspectra.live/P2Pool/consensus/v4/p2pool/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
	gojson "git.gammaspectra.live/P2Pool/go-json"
	fasthex "github.com/tmthrgd/go-hex"
)

// HighFeeValue 0.006 XMR
const HighFeeValue uint64 = 6000000000
const TimeInMempool = time.Second * 5

type ephemeralPubKeyCacheKey [crypto.PublicKeySize*2 + 8]byte

type ephemeralPubKeyCacheEntry struct {
	PublicKey crypto.PublicKeyBytes
	ViewTag   uint8
}

type NewTemplateData struct {
	PreviousTemplateId        types.Hash
	SideHeight                uint64
	Difficulty                types.Difficulty
	CumulativeDifficulty      types.Difficulty
	TransactionPrivateKeySeed types.Hash
	// TransactionPrivateKey Generated from TransactionPrivateKeySeed
	TransactionPrivateKey  crypto.PrivateKeyBytes
	TransactionPublicKey   crypto.PublicKeySlice
	Timestamp              uint64
	TotalReward            uint64
	Transactions           []types.Hash
	MaxRewardAmountsWeight uint64
	ShareVersion           sidechain.ShareVersion
	Uncles                 []types.Hash
	Ready                  bool
	Window                 struct {
		ReservedShareIndex   int
		Shares               sidechain.Shares
		ShuffleMapping       ShuffleMapping
		EphemeralPubKeyCache map[ephemeralPubKeyCacheKey]*ephemeralPubKeyCacheEntry
	}
}

type Server struct {
	SubmitFunc     func(block *sidechain.PoolBlock) error
	SubmitMainFunc func(b *block.Block) error

	refreshDuration time.Duration

	minerData       *p2pooltypes.MinerData
	tip             *sidechain.PoolBlock
	newTemplateData NewTemplateData
	lock            sync.RWMutex
	sidechain       *sidechain.SideChain

	mempool            MiningMempool
	lastMempoolRefresh time.Time

	preAllocatedDifficultyData        []sidechain.DifficultyData
	preAllocatedDifficultyDifferences []uint32
	preAllocatedSharesPool            *sidechain.PreAllocatedSharesPool

	preAllocatedBufferLock sync.Mutex
	preAllocatedBuffer     []byte

	minersLock sync.RWMutex
	miners     map[address.PackedAddress]*MinerTrackingEntry

	bansLock sync.RWMutex
	bans     map[[16]byte]BanEntry

	clientsLock sync.RWMutex
	clients     []*Client

	incomingChanges chan func() bool
}

func NewServer(s *sidechain.SideChain, submitFunc func(block *sidechain.PoolBlock) error, submitMain func(b *block.Block) error) *Server {
	server := &Server{
		SubmitFunc:                        submitFunc,
		SubmitMainFunc:                    submitMain,
		sidechain:                         s,
		preAllocatedDifficultyData:        make([]sidechain.DifficultyData, s.Consensus().ChainWindowSize*2),
		preAllocatedDifficultyDifferences: make([]uint32, s.Consensus().ChainWindowSize*2),
		preAllocatedSharesPool:            sidechain.NewPreAllocatedSharesPool(s.Consensus().ChainWindowSize * 2),
		preAllocatedBuffer:                make([]byte, 0, sidechain.PoolBlockMaxTemplateSize),
		miners:                            make(map[address.PackedAddress]*MinerTrackingEntry),
		mempool:                           (MiningMempool)(make(map[types.Hash]*mempool.Entry, 512)),
		// buffer 4 at a time for non-blocking source
		incomingChanges: make(chan func() bool, 4),

		//refresh every n seconds
		refreshDuration: time.Duration(s.Consensus().TargetBlockTime) * time.Second,
	}
	return server
}

func (s *Server) CleanupMiners() {
	s.minersLock.Lock()
	defer s.minersLock.Unlock()

	cleanupTime := time.Now()
	for k, e := range s.miners {
		if cleanupTime.Sub(e.LastJob) > time.Minute*5 {
			delete(s.miners, k)
		} else {
			if len(e.Templates) > 0 {
				var templateSideHeight uint64
				for _, tpl := range e.Templates {
					if tpl.SideHeight > templateSideHeight {
						templateSideHeight = tpl.SideHeight
					}
				}

				tipHeight := uint64(0)
				if templateSideHeight > sidechain.UncleBlockDepth {
					tipHeight = templateSideHeight - sidechain.UncleBlockDepth + 1
				}
				//Delete old templates further than uncle depth
				for key, tpl := range e.Templates {
					if tpl.SideHeight < tipHeight {
						delete(e.Templates, key)
					}
				}

				// TODO: Prevent long-term leaks by re-allocating templates if capacity grew too much
			}
		}
	}
}

func (s *Server) fillNewTemplateData(currentDifficulty types.Difficulty) error {

	s.newTemplateData.Ready = false

	if s.minerData == nil {
		return errors.New("no main data present")
	}

	if s.minerData.MajorVersion > monero.HardForkSupportedVersion {
		return fmt.Errorf("unsupported hardfork version %d", s.minerData.MajorVersion)
	}

	oldPubKeyCache := s.newTemplateData.Window.EphemeralPubKeyCache

	s.newTemplateData.Timestamp = uint64(time.Now().Unix())

	s.newTemplateData.ShareVersion = sidechain.P2PoolShareVersion(s.sidechain.Consensus(), s.newTemplateData.Timestamp)

	// Do not allow mining on old chains, as they are not optimal for CPU usage
	if s.newTemplateData.ShareVersion < sidechain.ShareVersion_V2 {
		return errors.New("unsupported sidechain version")
	}

	if s.newTemplateData.ShareVersion > sidechain.ShareVersion_V3 {
		return errors.New("unsupported sidechain version")
	}

	if s.tip != nil {
		s.newTemplateData.PreviousTemplateId = s.tip.SideTemplateId(s.sidechain.Consensus())
		s.newTemplateData.SideHeight = s.tip.Side.Height + 1

		oldSeed := s.newTemplateData.TransactionPrivateKeySeed

		s.newTemplateData.TransactionPrivateKeySeed = s.tip.Side.CoinbasePrivateKeySeed
		if s.tip.Main.PreviousId != s.minerData.PrevId {
			s.newTemplateData.TransactionPrivateKeySeed = s.tip.CalculateTransactionPrivateKeySeed()
		}

		//TODO: check this
		if s.newTemplateData.TransactionPrivateKeySeed != oldSeed || s.tip.Side.CoinbasePrivateKeySeed != oldSeed {
			oldPubKeyCache = nil
		}

		if currentDifficulty != types.ZeroDifficulty {
			//difficulty is set from caller
			s.newTemplateData.Difficulty = currentDifficulty
			oldPubKeyCache = nil
		}

		s.newTemplateData.CumulativeDifficulty = s.tip.Side.CumulativeDifficulty.Add(s.newTemplateData.Difficulty)

		s.newTemplateData.Uncles = s.sidechain.GetPossibleUncles(s.tip, s.newTemplateData.SideHeight)
	} else {
		s.newTemplateData.PreviousTemplateId = types.ZeroHash
		s.newTemplateData.TransactionPrivateKeySeed = s.sidechain.Consensus().Id
		s.newTemplateData.Difficulty = types.DifficultyFrom64(s.sidechain.Consensus().MinimumDifficulty)
		s.newTemplateData.CumulativeDifficulty = types.DifficultyFrom64(s.sidechain.Consensus().MinimumDifficulty)
	}

	kP := s.sidechain.DerivationCache().GetDeterministicTransactionKey(s.newTemplateData.TransactionPrivateKeySeed, s.minerData.PrevId)
	s.newTemplateData.TransactionPrivateKey = kP.PrivateKey.AsBytes()
	s.newTemplateData.TransactionPublicKey = kP.PublicKey.AsSlice()

	fakeTemplateTipBlock := &sidechain.PoolBlock{
		Main: block.Block{
			MajorVersion: s.minerData.MajorVersion,
			MinorVersion: monero.HardForkSupportedVersion,
			Timestamp:    s.newTemplateData.Timestamp,
			PreviousId:   s.minerData.PrevId,
			Nonce:        0,
			Coinbase: transaction.CoinbaseTransaction{
				GenHeight: s.minerData.Height,
			},
			//TODO:
			Transactions: nil,
		},
		Side: sidechain.SideData{
			//Zero Spend/View key
			PublicKey:              address.PackedAddress{},
			CoinbasePrivateKeySeed: s.newTemplateData.TransactionPrivateKeySeed,
			CoinbasePrivateKey:     s.newTemplateData.TransactionPrivateKey,
			Parent:                 s.newTemplateData.PreviousTemplateId,
			Uncles:                 s.newTemplateData.Uncles,
			Height:                 s.newTemplateData.SideHeight,
			Difficulty:             s.newTemplateData.Difficulty,
			CumulativeDifficulty:   s.newTemplateData.CumulativeDifficulty,
		},
		Metadata: sidechain.PoolBlockReceptionMetadata{
			LocalTime: time.Now().UTC(),
		},
		CachedShareVersion: s.newTemplateData.ShareVersion,
	}

	preAllocatedShares := s.preAllocatedSharesPool.Get()
	defer s.preAllocatedSharesPool.Put(preAllocatedShares)
	shares, _, err := sidechain.GetSharesOrdered(fakeTemplateTipBlock, s.sidechain.Consensus(), s.sidechain.Server().GetDifficultyByHeight, s.sidechain.GetPoolBlockByTemplateId, preAllocatedShares)
	if err != nil {
		return fmt.Errorf("could not get outputs: %w", err)
	}

	s.newTemplateData.Window.Shares = slices.Clone(shares)
	s.newTemplateData.Window.ReservedShareIndex = s.newTemplateData.Window.Shares.Index(fakeTemplateTipBlock.GetAddress())

	if s.newTemplateData.Window.ReservedShareIndex == -1 {
		return errors.New("could not find reserved share index")
	}

	// Only choose transactions that were received 5 or more seconds ago, or high fee (>= 0.006 XMR) transactions
	selectedMempool := s.mempool.Select(HighFeeValue, TimeInMempool)

	//TODO: limit max Monero block size

	baseReward := block.GetBaseReward(s.minerData.AlreadyGeneratedCoins)

	totalWeight, totalFees := selectedMempool.WeightAndFees()

	maxReward := baseReward + totalFees

	rewards := sidechain.SplitRewardAllocate(maxReward, s.newTemplateData.Window.Shares)

	s.newTemplateData.MaxRewardAmountsWeight = uint64(utils.UVarInt64SliceSize(rewards))

	//TODO efficient, move elsewhere
	nonce, ok := merge_mining.FindAuxiliaryNonce([]types.Hash{s.sidechain.Consensus().Id}, math.MaxUint32)
	if !ok {
		return errors.New("could not find nonce")
	}
	tag := merge_mining.Tag{
		NumberAuxiliaryChains: 1,
		Nonce:                 nonce,
	}

	tx, err := s.createCoinbaseTransaction(s.newTemplateData.ShareVersion, tag.MarshalTreeData(), fakeTemplateTipBlock.GetTransactionOutputType(), s.newTemplateData.Window.Shares, rewards, s.newTemplateData.MaxRewardAmountsWeight, false)
	if err != nil {
		return err
	}
	coinbaseTransactionWeight := uint64(tx.BufferLength())

	var pickedMempool mempool.Mempool

	if totalWeight+coinbaseTransactionWeight <= s.minerData.MedianWeight {
		// if a block doesn't get into the penalty zone, just pick all transactions
		pickedMempool = selectedMempool
	} else {
		pickedMempool = selectedMempool.Pick(baseReward, coinbaseTransactionWeight, s.minerData.MedianWeight)
	}

	//shuffle transactions
	unsafeRandom.Shuffle(len(pickedMempool), func(i, j int) {
		pickedMempool[i], pickedMempool[j] = pickedMempool[j], pickedMempool[i]
	})

	s.newTemplateData.Transactions = make([]types.Hash, len(pickedMempool))

	for i, entry := range pickedMempool {
		s.newTemplateData.Transactions[i] = entry.Id
	}

	finalReward := mempool.GetBlockReward(baseReward, s.minerData.MedianWeight, pickedMempool.Fees(), coinbaseTransactionWeight+pickedMempool.Weight())

	if finalReward < baseReward {
		return errors.New("final reward < base reward, should never happen")
	}
	s.newTemplateData.TotalReward = finalReward

	s.newTemplateData.Window.ShuffleMapping = BuildShuffleMapping(len(s.newTemplateData.Window.Shares), s.newTemplateData.ShareVersion, s.newTemplateData.TransactionPrivateKeySeed, s.newTemplateData.Window.ShuffleMapping)

	s.newTemplateData.Window.EphemeralPubKeyCache = make(map[ephemeralPubKeyCacheKey]*ephemeralPubKeyCacheEntry)

	txPrivateKeySlice := s.newTemplateData.TransactionPrivateKey.AsSlice()
	txPrivateKeyScalar := s.newTemplateData.TransactionPrivateKey.AsScalar()

	//TODO: parallelize this
	hasher := crypto.GetKeccak256Hasher()
	defer crypto.PutKeccak256Hasher(hasher)
	// generate ephemeral pubkeys based on indices

	var tempPubKey ephemeralPubKeyCacheKey
	generateEphPubKeyForIndex := func(index int, addr *address.PackedAddress) {
		if index == -1 {
			return
		}
		copy(tempPubKey[:], addr.Bytes())
		binary.LittleEndian.PutUint64(tempPubKey[crypto.PublicKeySize*2:], uint64(index))
		if e, ok := oldPubKeyCache[tempPubKey]; ok {
			s.newTemplateData.Window.EphemeralPubKeyCache[tempPubKey] = e
		} else {
			var e ephemeralPubKeyCacheEntry
			e.PublicKey, e.ViewTag = s.sidechain.DerivationCache().GetEphemeralPublicKey(addr, txPrivateKeySlice, txPrivateKeyScalar, uint64(index), hasher)
			s.newTemplateData.Window.EphemeralPubKeyCache[tempPubKey] = &e
		}
	}
	s.newTemplateData.Window.ShuffleMapping.RangePossibleIndices(func(i int, ix0, ix1, ix2 int) {
		if i == ShuffleMappingZeroKeyIndex {
			// Skip zero key
			return
		}

		share := s.newTemplateData.Window.Shares[i]
		generateEphPubKeyForIndex(ix0, &share.Address)
		generateEphPubKeyForIndex(ix1, &share.Address)
		generateEphPubKeyForIndex(ix2, &share.Address)
	})

	s.newTemplateData.Ready = true

	return nil

}

func (s *Server) BuildTemplate(addr address.PackedAddress, forceNewTemplate bool) (tpl *Template, jobCounter uint64, difficultyTarget types.Difficulty, seedHash types.Hash, err error) {

	var zeroAddress address.PackedAddress
	if addr == zeroAddress {
		return nil, 0, types.ZeroDifficulty, types.ZeroHash, errors.New("nil address")
	}

	e, ok := func() (*MinerTrackingEntry, bool) {
		s.minersLock.RLock()
		defer s.minersLock.RUnlock()
		e, ok := s.miners[addr]
		return e, ok
	}()

	tpl, jobCounter, targetDiff, seedHash, err := func() (tpl *Template, jobCounter uint64, difficultyTarget types.Difficulty, seedHash types.Hash, err error) {
		s.lock.RLock()
		defer s.lock.RUnlock()

		if s.minerData == nil {
			return nil, 0, types.ZeroDifficulty, types.ZeroHash, errors.New("nil miner data")
		}

		if !s.newTemplateData.Ready {
			return nil, 0, types.ZeroDifficulty, types.ZeroHash, errors.New("template data not ready")
		}

		if !forceNewTemplate && e != nil && ok {
			if tpl, jobCounter := func() (*Template, uint64) {
				e.Lock.RLock()
				defer e.Lock.RUnlock()

				jobCounter := e.LastTemplate.Load()

				if tpl, ok := e.Templates[jobCounter]; ok && tpl.SideParent == s.newTemplateData.PreviousTemplateId && tpl.MainParent == s.minerData.PrevId {
					return tpl, jobCounter
				}
				return nil, 0
			}(); tpl != nil {
				e.Lock.Lock()
				defer e.Lock.Unlock()
				e.LastJob = time.Now()

				targetDiff := tpl.SideDifficulty
				if s.minerData.Difficulty.Cmp(targetDiff) < 0 {
					targetDiff = s.minerData.Difficulty
				}

				return tpl, jobCounter, targetDiff, s.minerData.SeedHash, nil
			}
		}

		blockTemplate := &sidechain.PoolBlock{
			Main: block.Block{
				MajorVersion: s.minerData.MajorVersion,
				MinorVersion: monero.HardForkSupportedVersion,
				Timestamp:    s.newTemplateData.Timestamp,
				PreviousId:   s.minerData.PrevId,
				Nonce:        0,
				Transactions: s.newTemplateData.Transactions,
			},
			Side: sidechain.SideData{
				PublicKey:              addr,
				CoinbasePrivateKeySeed: s.newTemplateData.TransactionPrivateKeySeed,
				CoinbasePrivateKey:     s.newTemplateData.TransactionPrivateKey,
				Parent:                 s.newTemplateData.PreviousTemplateId,
				Uncles:                 s.newTemplateData.Uncles,
				Height:                 s.newTemplateData.SideHeight,
				Difficulty:             s.newTemplateData.Difficulty,
				CumulativeDifficulty:   s.newTemplateData.CumulativeDifficulty,
				MerkleProof:            nil,
				MergeMiningExtra:       nil,
				ExtraBuffer: sidechain.SideDataExtraBuffer{
					SoftwareId:          p2pooltypes.CurrentSoftwareId,
					SoftwareVersion:     p2pooltypes.CurrentSoftwareVersion,
					RandomNumber:        0,
					SideChainExtraNonce: 0,
				},
			},
			CachedShareVersion: s.newTemplateData.ShareVersion,
		}

		preAllocatedShares := s.preAllocatedSharesPool.Get()
		defer s.preAllocatedSharesPool.Put(preAllocatedShares)

		shares := s.newTemplateData.Window.Shares.Clone()

		// It exists, replace
		if i := shares.Index(addr); i != -1 {
			shares[i] = &sidechain.Share{
				Address: addr,
				Weight:  shares[i].Weight.Add(shares[s.newTemplateData.Window.ReservedShareIndex].Weight),
			}
			shares = slices.Delete(shares, s.newTemplateData.Window.ReservedShareIndex, s.newTemplateData.Window.ReservedShareIndex+1)
		} else {
			// Replace reserved address
			shares[s.newTemplateData.Window.ReservedShareIndex] = &sidechain.Share{
				Weight:  shares[s.newTemplateData.Window.ReservedShareIndex].Weight,
				Address: addr,
			}
		}
		shares = shares.Compact()

		// Apply consensus shuffle
		shares = ApplyShuffleMapping(shares, s.newTemplateData.Window.ShuffleMapping)

		// Allocate rewards
		{
			preAllocatedRewards := make([]uint64, 0, len(shares))
			rewards := sidechain.SplitReward(preAllocatedRewards, s.newTemplateData.TotalReward, shares)

			if rewards == nil || len(rewards) != len(shares) {
				return nil, 0, types.ZeroDifficulty, types.ZeroHash, errors.New("could not calculate rewards")
			}

			// TODO efficient, move elsewhere
			// This is deterministic. save this value
			nonce, ok := merge_mining.FindAuxiliaryNonce([]types.Hash{s.sidechain.Consensus().Id}, math.MaxUint32)
			if !ok {
				return nil, 0, types.ZeroDifficulty, types.ZeroHash, errors.New("could not find nonce")
			}
			tag := merge_mining.Tag{
				NumberAuxiliaryChains: 1,
				Nonce:                 nonce,
			}

			if blockTemplate.Main.Coinbase, err = s.createCoinbaseTransaction(s.newTemplateData.ShareVersion, tag.MarshalTreeData(), blockTemplate.GetTransactionOutputType(), shares, rewards, s.newTemplateData.MaxRewardAmountsWeight, true); err != nil {
				return nil, 0, types.ZeroDifficulty, types.ZeroHash, err
			}
		}

		tpl, err = TemplateFromPoolBlock(s.sidechain.Consensus(), blockTemplate)
		if err != nil {
			return nil, 0, types.ZeroDifficulty, types.ZeroHash, err
		}

		targetDiff := tpl.SideDifficulty
		if s.minerData.Difficulty.Cmp(targetDiff) < 0 {
			targetDiff = s.minerData.Difficulty
		}

		return tpl, 0, targetDiff, s.minerData.SeedHash, nil
	}()

	if err != nil {
		return nil, 0, types.ZeroDifficulty, types.ZeroHash, err
	}

	if forceNewTemplate || jobCounter != 0 {
		return tpl, jobCounter, targetDiff, seedHash, nil
	}

	if e != nil && ok {
		e.Lock.Lock()
		defer e.Lock.Unlock()
		var newJobCounter uint64
		for newJobCounter == 0 {
			newJobCounter = e.Counter.Add(1)
		}
		e.Templates[newJobCounter] = tpl
		e.LastTemplate.Store(newJobCounter)
		e.LastJob = time.Now()
		return tpl, newJobCounter, targetDiff, seedHash, nil
	} else {
		s.minersLock.Lock()
		defer s.minersLock.Unlock()

		e = &MinerTrackingEntry{
			Templates: make(map[uint64]*Template),
		}
		var newJobCounter uint64
		for newJobCounter == 0 {
			newJobCounter = e.Counter.Add(1)
		}
		e.Templates[newJobCounter] = tpl
		e.LastTemplate.Store(newJobCounter)
		e.LastJob = time.Now()
		s.miners[addr] = e
		return tpl, newJobCounter, targetDiff, seedHash, nil
	}
}

func (s *Server) createCoinbaseTransaction(shareVersion sidechain.ShareVersion, mergeMiningTreeData uint64, txType uint8, shares sidechain.Shares, rewards []uint64, maxRewardsAmountsWeight uint64, final bool) (tx transaction.CoinbaseTransaction, err error) {

	var mergeMineTag transaction.ExtraTag
	if shareVersion <= sidechain.ShareVersion_V2 {
		sidechainId := slices.Clone(types.ZeroHash[:])
		mergeMineTag = transaction.ExtraTag{
			Tag:       transaction.TxExtraTagMergeMining,
			VarInt:    uint64(len(sidechainId)),
			HasVarInt: true,
			Data:      sidechainId,
		}
	} else if shareVersion == sidechain.ShareVersion_V3 {

		data := make([]byte, utils.UVarInt64Size(mergeMiningTreeData)+types.HashSize)
		binary.PutUvarint(data, mergeMiningTreeData)

		mergeMineTag = transaction.ExtraTag{
			Tag:       transaction.TxExtraTagMergeMining,
			VarInt:    uint64(len(data)),
			HasVarInt: true,
			Data:      data,
		}
	} else {
		return transaction.CoinbaseTransaction{}, errors.New("unsupported share version")
	}

	tx = transaction.CoinbaseTransaction{
		Version:    2,
		UnlockTime: s.minerData.Height + monero.MinerRewardUnlockTime,
		InputCount: 1,
		InputType:  transaction.TxInGen,
		GenHeight:  s.minerData.Height,
		AuxiliaryData: transaction.CoinbaseTransactionAuxiliaryData{
			TotalReward: func() (v uint64) {
				for i := range rewards {
					v += rewards[i]
				}
				return
			}(),
		},
		Extra: transaction.ExtraTags{
			transaction.ExtraTag{
				Tag:    transaction.TxExtraTagPubKey,
				VarInt: 0,
				Data:   types.Bytes(s.newTemplateData.TransactionPublicKey),
			},
			transaction.ExtraTag{
				Tag:       transaction.TxExtraTagNonce,
				VarInt:    sidechain.SideExtraNonceSize,
				HasVarInt: true,
				Data:      make(types.Bytes, sidechain.SideExtraNonceSize),
			},
			mergeMineTag,
		},
		ExtraBaseRCT: 0,
	}

	tx.Outputs = make(transaction.Outputs, len(shares))

	if final {
		txPrivateKeySlice := s.newTemplateData.TransactionPrivateKey.AsSlice()
		txPrivateKeyScalar := s.newTemplateData.TransactionPrivateKey.AsScalar()

		hasher := crypto.GetKeccak256Hasher()
		defer crypto.PutKeccak256Hasher(hasher)

		var k ephemeralPubKeyCacheKey
		for i := range tx.Outputs {
			outputIndex := uint64(i)
			tx.Outputs[outputIndex].Index = outputIndex
			tx.Outputs[outputIndex].Type = txType
			tx.Outputs[outputIndex].Reward = rewards[outputIndex]
			copy(k[:], shares[outputIndex].Address.Bytes())
			binary.LittleEndian.PutUint64(k[crypto.PublicKeySize*2:], outputIndex)
			if e, ok := s.newTemplateData.Window.EphemeralPubKeyCache[k]; ok {
				tx.Outputs[outputIndex].EphemeralPublicKey, tx.Outputs[outputIndex].ViewTag = e.PublicKey, e.ViewTag
			} else {
				tx.Outputs[outputIndex].EphemeralPublicKey, tx.Outputs[outputIndex].ViewTag = s.sidechain.DerivationCache().GetEphemeralPublicKey(&shares[outputIndex].Address, txPrivateKeySlice, txPrivateKeyScalar, outputIndex, hasher)
			}
		}
	} else {
		for i := range tx.Outputs {
			outputIndex := uint64(i)
			tx.Outputs[outputIndex].Index = outputIndex
			tx.Outputs[outputIndex].Type = txType
			tx.Outputs[outputIndex].Reward = rewards[outputIndex]
		}
	}

	rewardAmountsWeight := uint64(utils.UVarInt64SliceSize(rewards))

	if !final {
		if rewardAmountsWeight != maxRewardsAmountsWeight {
			return transaction.CoinbaseTransaction{}, fmt.Errorf("incorrect miner rewards during the dry run, %d != %d", rewardAmountsWeight, maxRewardsAmountsWeight)
		}
	} else if rewardAmountsWeight > maxRewardsAmountsWeight {
		return transaction.CoinbaseTransaction{}, fmt.Errorf("incorrect miner rewards during the dry run, %d > %d", rewardAmountsWeight, maxRewardsAmountsWeight)
	}

	correctedExtraNonceSize := sidechain.SideExtraNonceSize + maxRewardsAmountsWeight - rewardAmountsWeight

	if correctedExtraNonceSize > sidechain.SideExtraNonceSize {
		if correctedExtraNonceSize > sidechain.SideExtraNonceMaxSize {
			return transaction.CoinbaseTransaction{}, fmt.Errorf("corrected extra_nonce size is too large, %d > %d", correctedExtraNonceSize, sidechain.SideExtraNonceMaxSize)
		}
		//Increase size to maintain transaction weight
		tx.Extra[1].Data = make(types.Bytes, correctedExtraNonceSize)
		tx.Extra[1].VarInt = correctedExtraNonceSize
	}

	return tx, nil
}

func (s *Server) HandleMempoolData(data mempool.Mempool) {
	s.incomingChanges <- func() bool {
		timeReceived := time.Now()

		s.lock.Lock()
		defer s.lock.Unlock()

		var highFeeReceived bool
		for _, tx := range data {

			if s.mempool.Add(tx) {
				if tx.Fee >= HighFeeValue {
					//prevent a lot of calls if not needed
					if utils.GlobalLogLevel&utils.LogLevelDebug > 0 {
						utils.Debugf("Stratum", "new tx id = %s, size = %d, weight = %d, fee = %s XMR", tx.Id, tx.BlobSize, tx.Weight, utils.XMRUnits(tx.Fee))
					}

					highFeeReceived = true
					utils.Noticef("Stratum", "high fee tx received: %s, %s XMR - updating template", tx.Id, utils.XMRUnits(tx.Fee))
				}
			}
		}

		// Refresh if 10 seconds have passed between templates and new transactions arrived, or a high fee was received
		if highFeeReceived || timeReceived.Sub(s.lastMempoolRefresh) >= s.refreshDuration {
			s.lastMempoolRefresh = timeReceived
			if err := s.fillNewTemplateData(types.ZeroDifficulty); err != nil {
				utils.Errorf("Stratum", "Error building new template data: %s", err)
				return false
			}
			return true
		}
		return false
	}
}

func (s *Server) HandleMinerData(minerData *p2pooltypes.MinerData) {
	s.incomingChanges <- func() bool {
		s.lock.Lock()
		defer s.lock.Unlock()

		if s.minerData == nil || s.minerData.Height <= minerData.Height {
			s.minerData = minerData
			s.mempool.Swap(minerData.TxBacklog)
			s.lastMempoolRefresh = time.Now()
			if err := s.fillNewTemplateData(types.ZeroDifficulty); err != nil {
				utils.Errorf("Stratum", "Error building new template data: %s", err)
				return false
			}
			return true
		}
		return false
	}
}

func (s *Server) HandleTip(tip *sidechain.PoolBlock) {
	currentDifficulty := s.sidechain.Difficulty()
	s.incomingChanges <- func() bool {
		s.lock.Lock()
		defer s.lock.Unlock()

		//TODO: what if tip is lower???
		if s.tip == nil || s.tip.Side.Height <= tip.Side.Height {
			s.tip = tip
			s.lastMempoolRefresh = time.Now()
			if err := s.fillNewTemplateData(currentDifficulty); err != nil {
				utils.Errorf("Stratum", "Error building new template data: %s", err)
				return false
			}
			return true
		}
		return false
	}
}

func (s *Server) HandleBroadcast(block *sidechain.PoolBlock) {
	s.incomingChanges <- func() bool {
		s.lock.Lock()
		defer s.lock.Unlock()

		if s.tip != nil && block != s.tip && block.Side.Height <= s.tip.Side.Height {
			//probably a new block was added as alternate
			if err := s.fillNewTemplateData(types.ZeroDifficulty); err != nil {
				utils.Errorf("Stratum", "Error building new template data: %s", err)
				return false
			}
			return true
		}
		return false
	}
}

func (s *Server) Update() {
	var closeClients []*Client
	defer func() {
		for _, c := range closeClients {
			s.CloseClient(c)
		}
	}()
	s.clientsLock.RLock()
	defer s.clientsLock.RUnlock()

	if len(s.clients) > 0 {
		utils.Logf("Stratum", "Sending new job to %d connection(s)", len(s.clients))
		for _, c := range s.clients {
			if err := s.SendTemplate(c, false); err != nil {
				utils.Noticef("Stratum", "Closing connection %s: %s", c.Conn.RemoteAddr().String(), err)
				closeClients = append(closeClients, c)
			}
		}
	}
}

type BanEntry struct {
	Expiration uint64
	Error      error
}

func (s *Server) CleanupBanList() {
	s.bansLock.Lock()
	defer s.bansLock.Unlock()

	currentTime := uint64(time.Now().Unix())

	for k, b := range s.bans {
		if currentTime >= b.Expiration {
			delete(s.bans, k)
		}
	}
}

func (s *Server) IsBanned(ip netip.Addr) (bool, *BanEntry) {
	if ip.IsLoopback() {
		return false, nil
	}
	ip = ip.Unmap()
	var prefix netip.Prefix
	if ip.Is6() {
		//ban the /64
		prefix, _ = ip.Prefix(64)
	} else if ip.Is4() {
		//ban only a single ip, /32
		prefix, _ = ip.Prefix(32)
	}

	if !prefix.IsValid() {
		return false, nil
	}

	k := prefix.Addr().As16()

	if b, ok := func() (entry BanEntry, ok bool) {
		s.bansLock.RLock()
		defer s.bansLock.RUnlock()
		entry, ok = s.bans[k]
		return entry, ok
	}(); ok == false {
		return false, nil
	} else if uint64(time.Now().Unix()) >= b.Expiration {
		return false, nil
	} else {
		return true, &b
	}
}

var ErrBannable = errors.New("banned")

func BanError(err error) error {
	return errors.Join(err, ErrBannable)
}

func (s *Server) Ban(ip netip.Addr, duration time.Duration, err error) {
	if ok, _ := s.IsBanned(ip); ok {
		return
	}

	if ip.IsLoopback() {
		return
	}

	utils.Noticef("Stratum", "Banned %s for %s: %s", ip.String(), duration.String(), err.Error())
	if !ip.IsLoopback() {
		ip = ip.Unmap()
		var prefix netip.Prefix
		if ip.Is6() {
			//ban the /64
			prefix, _ = ip.Prefix(64)
		} else if ip.Is4() {
			//ban only a single ip, /32
			prefix, _ = ip.Prefix(32)
		}

		if prefix.IsValid() {
			func() {
				s.bansLock.Lock()
				defer s.bansLock.Unlock()
				s.bans[prefix.Addr().As16()] = BanEntry{
					Error:      err,
					Expiration: uint64(time.Now().Unix()) + uint64(duration.Seconds()),
				}
			}()
		}
	}

}

func (s *Server) processIncoming() {
	go func() {
		defer close(s.incomingChanges)

		ctx := s.sidechain.Server().Context()
		for {
			select {
			case <-ctx.Done():
				return
			case f := <-s.incomingChanges:
				if f() {
					s.Update()
				}
			}
		}
	}()
}

func (s *Server) Listen(listen string, controlOpts ...func(network, address string, c syscall.RawConn) (err error)) error {

	ctx := s.sidechain.Server().Context()
	go func() {
		for range utils.ContextTick(ctx, time.Second*15) {
			s.CleanupMiners()
		}
	}()
	go func() {
		for range utils.ContextTick(ctx, time.Hour) {
			s.CleanupBanList()
		}
	}()

	s.processIncoming()

	if listener, err := (&net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			for _, opt := range controlOpts {
				if err := opt(network, address, c); err != nil {
					return err
				}
			}
			return nil
		},
	}).Listen(ctx, "tcp", listen); err != nil {
		return err
	} else if tcpListener, ok := listener.(*net.TCPListener); !ok {
		return errors.New("not a tcp listener")
	} else {
		defer tcpListener.Close()

		addressNetwork := s.sidechain.Consensus().NetworkType.MustAddressNetwork()

		for {
			if conn, err := tcpListener.AcceptTCP(); err != nil {
				return err
			} else {
				var addrPort netip.AddrPort
				if addrPort, err = func() (netip.AddrPort, error) {
					addrPort, err := netip.ParseAddrPort(conn.RemoteAddr().String())
					if err != nil {
						return addrPort, err
					} else if !addrPort.Addr().IsLoopback() {
						addr := addrPort.Addr().Unmap()

						if ok, b := s.IsBanned(addr); ok {
							return addrPort, fmt.Errorf("peer is banned: %w", b.Error)
						}
					}

					return addrPort, nil
				}(); err != nil {
					go func() {
						defer conn.Close()
						utils.Noticef("Stratum", "Connection from %s rejected (%s)", conn.RemoteAddr().String(), err.Error())
					}()
					continue
				}

				func() {
					utils.Noticef("Stratum", "Incoming connection from %s", conn.RemoteAddr().String())

					var rpcId uint32
					for rpcId == 0 {
						rpcId = unsafeRandom.Uint32()
					}
					decoder := utils.NewJSONDecoder(conn)
					decoder.UseNumber()
					client := &Client{
						RpcId:   rpcId,
						Conn:    conn,
						decoder: decoder,

						// Default to donation address if not specified
						Address: address.FromBase58(types.DonationAddress).ToPackedAddress(),
					}
					// Use deadline
					client.encoder = utils.NewJSONEncoder(client)

					func() {
						s.clientsLock.Lock()
						defer s.clientsLock.Unlock()
						s.clients = append(s.clients, client)
					}()
					go func() {
						var err error
						defer s.CloseClient(client)
						defer func() {
							if err != nil {
								utils.Noticef("Stratum", "Connection %s closed with error: %s", client.Conn.RemoteAddr().String(), err)
								if errors.Is(err, ErrBannable) {
									s.Ban(addrPort.Addr(), time.Minute*15, err)
								}
							} else {
								utils.Noticef("Stratum", "Connection %s closed", client.Conn.RemoteAddr().String())
							}
						}()
						defer func() {
							if e := recover(); e != nil {
								if err = e.(error); err == nil {
									err = fmt.Errorf("panic called: %v", e)
								}
								s.CloseClient(client)
							}
						}()

						for client.decoder.More() {
							var msg JsonRpcMessage
							if err = client.decoder.Decode(&msg); err != nil {
								return
							}

							if idStr, ok := msg.Id.(string); ok {
								if len(idStr) == 0 || len(idStr) > 64 {
									err = errors.New("invalid string id")
									return
								}
							} else if _, ok := msg.Id.(gojson.Number); ok {

							} else {
								err = errors.New("invalid id format")
								return
							}

							switch msg.Method {
							case "login":
								if err = func() error {
									client.Lock.Lock()
									defer client.Lock.Unlock()
									if client.Login {
										return errors.New("already logged in")
									}
									if m, ok := msg.Params.(map[string]any); ok {
										if str, ok := m["agent"].(string); ok {
											if len(str) > 512 {
												return errors.New("agent too long")
											}
											client.Agent = str
										}
										if str, ok := m["pass"].(string); ok {
											if len(str) > 512 {
												return errors.New("pass too long")
											}
											client.Password = str
										}

										if str, ok := m["rigid"].(string); ok {
											if len(str) > 512 {
												return errors.New("rigid too long")
											}
											client.RigId = str
										} else if str, ok := m["rig-id"].(string); ok {
											if len(str) > 512 {
												return errors.New("rig-id too long")
											}
											client.RigId = str
										}

										if str, ok := m["login"].(string); ok {
											//TODO: support merge mining addresses
											a := address.FromBase58(str)
											if a == nil || !a.Valid() {
												return errors.New("invalid address in user, or integrated addresses not supported")
											} else if a.BaseNetwork() != addressNetwork {
												return errors.New("invalid address in user, wrong network")
											} else if a.IsSubaddress() {
												if h, err := types.HashFromString(client.Password); err == nil {
													viewKey := crypto.PrivateKeyBytes(h)
													fa := address.GetSubaddressFakeAddress(a, &viewKey)
													if fa == nil {
														return errors.New("invalid address in user, invalid subaddress conversion")
													}
													client.Address = fa.ToPackedAddress()
													copy(client.SubaddressViewPub[:], a.ViewPub[:])
													// cleanup
													client.Password = ""
												} else {
													return errors.New("invalid address in user, subaddress specified but no valid viewkey on pass field")
												}
											} else {
												if sa := address.FromBase58(client.Password); sa != nil && sa.BaseNetwork() == a.BaseNetwork() && sa.IsSubaddress() {
													if !sa.Valid() {
														return errors.New("invalid subaddress in pass")
													}
													// allow sending to subaddress when specified
													client.Address = address.PackedAddress{sa.SpendPublicKey().AsBytes(), a.ViewPublicKey().AsBytes()}
													copy(client.SubaddressViewPub[:], sa.ViewPub[:])
													// cleanup
													client.Password = ""
												} else {
													client.Address = a.ToPackedAddress()
												}
											}
										}
										// algo extension
										var hasRx0 bool
										if algos, ok := m["algo"].([]any); ok {
											for _, v := range algos {
												if str, ok := v.(string); !ok {
													return errors.New("invalid algo")
												} else if str == "rx/0" {
													hasRx0 = true
													client.Extensions.Algo = true
													break
												}
											}
										} else {
											// default rx0 true
											hasRx0 = true
										}

										if !hasRx0 {
											return errors.New("algo rx/0 not found")
										}

										utils.Debugf("Stratum", "Connection %s address = %s, agent = \"%s\", pass = \"%s\"", client.Conn.RemoteAddr().String(), client.Address.ToAddress(addressNetwork).ToBase58(), client.Agent, client.Password)

										client.Login = true
										return nil
									} else {
										return errors.New("could not read login params")
									}
								}(); err != nil {
									_ = client.encoder.Encode(JsonRpcResult{
										Id:             msg.Id,
										JsonRpcVersion: "2.0",
										Error: map[string]any{
											"code":    int(-1),
											"message": err.Error(),
										},
									})
									return
								} else if err = s.SendTemplateResponse(client, msg.Id, false); err != nil {
									_ = client.encoder.Encode(JsonRpcResult{
										Id:             msg.Id,
										JsonRpcVersion: "2.0",
										Error: map[string]any{
											"code":    int(-1),
											"message": err.Error(),
										},
									})
								}
							/*
								// send new job directly
								// TODO: is this even necessary?
								case "getjob":
									if submitError, ban := func() (error, bool) {
										client.Lock.RLock()
										defer client.Lock.RUnlock()
										if !client.Login {
											return errors.New("unauthenticated"), true
										}

										//TODO: limit cadence
										return nil, false
									}(); submitError != nil {
										err = client.encoder.Encode(JsonRpcResult{
											Id:             msg.Id,
											JsonRpcVersion: "2.0",
											Error: map[string]any{
												"code":    int(-1),
												"message": submitError.Error(),
											},
										})
										if err != nil || ban {
											return
										}
									} else if err = s.SendTemplateResponse(client, msg.Id); err != nil {
										_ = client.encoder.Encode(JsonRpcResult{
											Id:             msg.Id,
											JsonRpcVersion: "2.0",
											Error: map[string]any{
												"code":    int(-1),
												"message": err.Error(),
											},
										})
									}
							*/
							case "submit":
								if submitError, ban := func() (error, bool) {
									client.Lock.RLock()
									defer client.Lock.RUnlock()
									if !client.Login {
										return errors.New("unauthenticated"), true
									}
									var err error
									var jobId Job
									var resultHash types.Hash
									var nonce uint32
									if m, ok := msg.Params.(map[string]any); ok {
										if str, ok := m["job_id"].(string); ok {
											if jobId, err = JobFromString(str); err != nil {
												return err, true
											}
										} else {
											return errors.New("no job_id specified"), true
										}
										if str, ok := m["nonce"].(string); ok {
											var nonceBuf []byte
											if nonceBuf, err = fasthex.DecodeString(str); err != nil {
												return err, true
											}
											if len(nonceBuf) != 4 {
												return errors.New("invalid nonce size"), true
											}
											nonce = binary.LittleEndian.Uint32(nonceBuf)
										} else {
											return errors.New("no nonce specified"), true
										}
										if str, ok := m["result"].(string); ok {
											if resultHash, err = types.HashFromString(str); err != nil {
												return err, true
											}
										} else {
											return errors.New("no result specified"), true
										}

										if err, ban := func() (error, bool) {
											if e, ok := func() (*MinerTrackingEntry, bool) {
												s.minersLock.RLock()
												defer s.minersLock.RUnlock()
												e, ok := s.miners[client.Address]
												return e, ok
											}(); ok {
												b := &sidechain.PoolBlock{}
												if blob := e.GetJobBlob(client, s.sidechain.Consensus(), jobId, nonce); blob == nil {
													return errors.New("invalid job id"), true
												} else if err := b.UnmarshalBinary(s.sidechain.Consensus(), s.sidechain.DerivationCache(), blob); err != nil {
													return err, true
												} else {
													powDiff := types.DifficultyFromPoW(resultHash)
													if powDiff.Cmp(b.Side.Difficulty) >= 0 {
														//passes difficulty
														if err := s.SubmitFunc(b); err != nil {
															return fmt.Errorf("submit error: %w", err), true
														}
													} else {
														// explicitly allow low diff shares that pass main difficulty but not sidechain one, useful for testnet
														if s.SubmitMainFunc != nil && powDiff.Cmp64(s.sidechain.Consensus().MinimumDifficulty) < 0 {
															if mainDiff := func() types.Difficulty {
																s.lock.RLock()
																defer s.lock.RUnlock()
																if s.minerData == nil {
																	return types.ZeroDifficulty
																}
																return s.minerData.Difficulty
															}(); mainDiff != types.ZeroDifficulty && mainDiff.CheckPoW(resultHash) {
																//passes main difficulty
																if err := s.SubmitMainFunc(&b.Main); err != nil {
																	return fmt.Errorf("submit main error: %w", err), false
																}
																return nil, false
															}
														}
														return errors.New("low difficulty share"), true
													}
												}
											} else {
												return errors.New("unknown miner"), true
											}
											return nil, false
										}(); err != nil {
											return err, ban
										}
										return nil, false
									} else {
										return errors.New("could not read submit params"), true
									}
								}(); submitError != nil {
									err = client.encoder.Encode(JsonRpcResult{
										Id:             msg.Id,
										JsonRpcVersion: "2.0",
										Error: map[string]any{
											"code":    int(-1),
											"message": submitError.Error(),
										},
									})
									if err != nil || ban {
										return
									}
								} else {
									if err = client.encoder.Encode(JsonRpcResult{
										Id:             msg.Id,
										JsonRpcVersion: "2.0",
										Error:          nil,
										Result: map[string]any{
											"status": "OK",
										},
									}); err != nil {
										return
									}
								}
							case "keepalived":
								if err = client.encoder.Encode(JsonRpcResult{
									Id:             msg.Id,
									JsonRpcVersion: "2.0",
									Error:          nil,
									Result: map[string]any{
										"status": "KEEPALIVED",
									},
								}); err != nil {
									return
								}
							default:
								err = fmt.Errorf("unknown command %s", msg.Method)
								_ = client.encoder.Encode(JsonRpcResult{
									Id:             msg.Id,
									JsonRpcVersion: "2.0",
									Error: map[string]any{
										"code":    int(-1),
										"message": err.Error(),
									},
								})
								return
							}
						}
					}()
				}()
			}

		}
	}

	return nil
}

func (s *Server) SendTemplate(c *Client, supportsTemplate bool) (err error) {
	c.Lock.Lock()
	defer c.Lock.Unlock()
	tpl, jobCounter, targetDifficulty, seedHash, err := s.BuildTemplate(c.Address, false)

	if err != nil {
		return err
	}

	bufLen := tpl.HashingBlobBufferLength()
	if cap(c.buf) < bufLen {
		c.buf = make([]byte, 0, bufLen)
	}

	sideRandomNumber := unsafeRandom.Uint32()
	sideExtraNonce := unsafeRandom.Uint32()
	extraNonce := uint32(0)
	if !supportsTemplate {
		extraNonce = sideExtraNonce
	}

	jobId := Job{
		TemplateCounter:  jobCounter,
		ExtraNonce:       extraNonce,
		SideRandomNumber: sideRandomNumber,
		SideExtraNonce:   sideExtraNonce,
	}

	// implicit addition
	mmExtra := jobId.MergeMiningExtra
	if c.SubaddressViewPub != [33]byte{} {
		mmExtra = mmExtra.Set(sidechain.ExtraChainKeySubaddressViewPub, c.SubaddressViewPub[:])
	}

	hasher := crypto.GetKeccak256Hasher()
	defer crypto.PutKeccak256Hasher(hasher)

	// todo merkle root with merge mine
	var templateId types.Hash
	tpl.TemplateId(hasher, c.buf, s.sidechain.Consensus(), jobId.SideRandomNumber, jobId.SideExtraNonce, jobId.MerkleProof, mmExtra, p2pooltypes.CurrentSoftwareId, p2pooltypes.CurrentSoftwareVersion, &templateId)
	jobId.MerkleRoot = templateId

	job := copyBaseJob()
	job.Params.Blob = fasthex.EncodeToString(tpl.HashingBlob(hasher, c.buf, 0, jobId.ExtraNonce, jobId.MerkleRoot))

	job.Params.JobId = jobId.Id()

	target := targetDifficulty.Target()
	job.Params.Target = TargetHex(target)
	job.Params.Height = tpl.MainHeight
	job.Params.SeedHash = seedHash

	if !c.Extensions.Algo {
		job.Params.Algo = ""
	}

	if err = c.encoder.EncodeWithOption(job, utils.JsonEncodeOptions...); err != nil {
		return
	}
	return nil
}

func (s *Server) SendTemplateResponse(c *Client, id any, supportsTemplate bool) (err error) {
	c.Lock.Lock()
	defer c.Lock.Unlock()
	tpl, jobCounter, targetDifficulty, seedHash, err := s.BuildTemplate(c.Address, false)

	if err != nil {
		return
	}

	bufLen := tpl.HashingBlobBufferLength()
	if cap(c.buf) < bufLen {
		c.buf = make([]byte, 0, bufLen)
	}
	var hexBuf [4]byte
	binary.LittleEndian.PutUint32(hexBuf[:], c.RpcId)

	sideRandomNumber := unsafeRandom.Uint32()
	sideExtraNonce := unsafeRandom.Uint32()
	extraNonce := uint32(0)
	if !supportsTemplate {
		extraNonce = sideExtraNonce
	}

	jobId := Job{
		TemplateCounter:  jobCounter,
		ExtraNonce:       extraNonce,
		SideRandomNumber: sideRandomNumber,
		SideExtraNonce:   sideExtraNonce,
	}

	// implicit addition
	mmExtra := jobId.MergeMiningExtra
	if c.SubaddressViewPub != [33]byte{} {
		mmExtra = mmExtra.Set(sidechain.ExtraChainKeySubaddressViewPub, c.SubaddressViewPub[:])
	}

	hasher := crypto.GetKeccak256Hasher()
	defer crypto.PutKeccak256Hasher(hasher)

	// todo merkle root with merge mine
	var templateId types.Hash
	tpl.TemplateId(hasher, c.buf, s.sidechain.Consensus(), jobId.SideRandomNumber, jobId.SideExtraNonce, jobId.MerkleProof, mmExtra, p2pooltypes.CurrentSoftwareId, p2pooltypes.CurrentSoftwareVersion, &templateId)
	jobId.MerkleRoot = templateId

	job := copyBaseResponseJob()
	job.Id = id
	job.Result.Id = fasthex.EncodeToString(hexBuf[:])
	job.Result.Job.Blob = fasthex.EncodeToString(tpl.HashingBlob(hasher, c.buf, 0, jobId.ExtraNonce, jobId.MerkleRoot))

	job.Result.Job.JobId = jobId.Id()

	target := targetDifficulty.Target()
	job.Result.Job.Target = TargetHex(target)
	job.Result.Job.Height = tpl.MainHeight
	job.Result.Job.SeedHash = seedHash

	if !c.Extensions.Algo {
		job.Result.Job.Algo = ""
	}

	if err = c.encoder.EncodeWithOption(job, utils.JsonEncodeOptions...); err != nil {
		return
	}
	return nil
}

func (s *Server) CloseClient(c *Client) {
	c.Conn.Close()

	//TODO: ban bad clients after n failed attempts

	s.clientsLock.Lock()
	defer s.clientsLock.Unlock()
	if i := slices.Index(s.clients, c); i != -1 {
		s.clients = slices.Delete(s.clients, i, i+1)
	}
}

// Target4BytesLimit Use short target format (4 bytes) for diff <= 4 million
const Target4BytesLimit = math.MaxUint64 / 4000001

func TargetHex(target uint64) string {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], target)
	result := fasthex.EncodeToString(buf[:])
	if target >= Target4BytesLimit {
		return result[4*2:]
	} else {
		return result
	}
}
