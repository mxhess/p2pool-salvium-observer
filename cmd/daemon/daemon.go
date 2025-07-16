package main

import (
	"context"
	"flag"
	"fmt"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/client"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/client/rpc/daemon"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/randomx"
	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/sidechain"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
	p2poolapi "git.gammaspectra.live/P2Pool/observer-cmd-utils/api"
	"git.gammaspectra.live/P2Pool/observer-cmd-utils/index"
	cmdutils "git.gammaspectra.live/P2Pool/observer-cmd-utils/utils"
	"net/http"
	_ "net/http/pprof"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

var sideBlocksLock sync.RWMutex

func main() {

	moneroHost := flag.String("host", "127.0.0.1", "IP address of your Monero node")
	moneroRpcPort := flag.Uint("rpc-port", 18081, "monerod RPC API port number")
	startFromHeight := flag.Uint64("from", 0, "Start sync from this height")
	dbString := flag.String("db", "", "")
	p2poolApiHost := flag.String("api-host", "", "Host URL for p2pool go observer consensus")
	fullMode := flag.Bool("full-mode", false, "Allocate RandomX dataset, uses 2GB of RAM")
	debugListen := flag.String("debug-listen", "", "Provide a bind address and port to expose a pprof HTTP API on it.")
	hookProxy := flag.String("hook-proxy", "", "socks5 proxy host:port for webhook requests")
	flag.Parse()

	client.SetDefaultClientSettings(fmt.Sprintf("http://%s:%d", *moneroHost, *moneroRpcPort))

	if *hookProxy != "" {
		cmdutils.SetWebHookProxy(*hookProxy)
	}

	p2api := p2poolapi.NewP2PoolApi(*p2poolApiHost)

	if err := p2api.WaitSync(); err != nil {
		utils.Panic(err)
	}

	if *fullMode {
		if err := p2api.Consensus().InitHasher(1, randomx.FlagSecure, randomx.FlagFullMemory); err != nil {
			utils.Panic(err)
		}
	} else {
		if err := p2api.Consensus().InitHasher(1, randomx.FlagSecure); err != nil {
			utils.Panic(err)
		}
	}

	indexDb, err := index.OpenIndex(*dbString, p2api.Consensus(), p2api.DifficultyByHeight, p2api.SeedByHeight, p2api.ByTemplateId)
	if err != nil {
		utils.Panic(err)
	}
	defer indexDb.Close()

	dbTip := indexDb.GetSideBlockTip()

	var tipHeight uint64
	if dbTip != nil {
		tipHeight = dbTip.SideHeight
	}
	utils.Logf("CHAIN", "Last known database tip is %d\n", tipHeight)

	window, uncles := p2api.StateFromTip()
	if *startFromHeight != 0 {
		tip := p2api.BySideHeight(*startFromHeight)
		if len(tip) != 0 {
			window, uncles = p2api.WindowFromTemplateId(tip[0].FastSideTemplateId(p2api.Consensus()))
		} else {
			tip = p2api.BySideHeight(*startFromHeight + p2api.Consensus().ChainWindowSize)
			if len(tip) != 0 {
				window, uncles = p2api.WindowFromTemplateId(tip[0].FastSideTemplateId(p2api.Consensus()))
			}
		}
	}

	insertFromTip := func(tip *sidechain.PoolBlock) {
		if indexDb.GetTipSideBlockByTemplateId(tip.FastSideTemplateId(p2api.Consensus())) != nil {
			//reached old tip
			return
		}
		for cur := tip; cur != nil; cur = p2api.ByTemplateId(cur.Side.Parent) {
			utils.Logf("CHAIN", "Inserting share %s at height %d\n", cur.FastSideTemplateId(p2api.Consensus()), cur.Side.Height)
			for _, u := range cur.Side.Uncles {
				utils.Logf("CHAIN", "Inserting uncle %s at parent height %d\n", u.String(), cur.Side.Height)
			}
			if err := indexDb.InsertOrUpdatePoolBlock(cur, index.InclusionInVerifiedChain); err != nil {
				utils.Panic(err)
			}
			if indexDb.GetTipSideBlockByTemplateId(cur.Side.Parent) != nil {
				//reached old tip
				break
			}
		}
	}

	var backfillScan []types.Hash

	for len(window) > 0 {
		utils.Logf("CHAIN", "Found range %d -> %d (%s to %s), %d shares, %d uncles", window[0].Side.Height, window[len(window)-1].Side.Height, window[0].SideTemplateId(p2api.Consensus()), window[len(window)-1].SideTemplateId(p2api.Consensus()), len(window), len(uncles))
		for _, b := range window {
			indexDb.CachePoolBlock(b)
		}
		for _, u := range uncles {
			indexDb.CachePoolBlock(u)
		}
		for _, b := range window {
			if indexDb.GetTipSideBlockByTemplateId(b.SideTemplateId(p2api.Consensus())) != nil {
				//reached old tip
				window = nil
				break
			}
			utils.Logf("CHAIN", "Inserting share %s at height %d\n", b.FastSideTemplateId(p2api.Consensus()), b.Side.Height)
			for _, u := range b.Side.Uncles {
				utils.Logf("CHAIN", "Inserting uncle %s at parent height %d\n", u.String(), b.Side.Height)
			}
			if err := indexDb.InsertOrUpdatePoolBlock(b, index.InclusionInVerifiedChain); err != nil {
				utils.Panic(err)
			}

			if b.IsProofHigherThanMainDifficulty(p2api.Consensus().GetHasher(), indexDb.GetDifficultyByHeight, indexDb.GetSeedByHeight) {
				backfillScan = append(backfillScan, b.MainId())
			}

			for _, uncleId := range b.Side.Uncles {
				if u := uncles.Get(p2api.Consensus(), uncleId); u != nil && u.IsProofHigherThanMainDifficulty(p2api.Consensus().GetHasher(), indexDb.GetDifficultyByHeight, indexDb.GetSeedByHeight) {
					backfillScan = append(backfillScan, u.MainId())
				}
			}
		}

		if len(window) == 0 {
			break
		}

		parent := p2api.ByTemplateId(window[len(window)-1].Side.Parent)
		if parent == nil {
			break
		}
		window, uncles = p2api.WindowFromTemplateId(parent.FastSideTemplateId(p2api.Consensus()))
		if len(window) == 0 {
			insertFromTip(parent)
			break
		}
	}

	var maxHeightPtr *uint64
	var minHeightPtr *uint64
	var currentHeightPtr *uint64
	if err = indexDb.Query("SELECT (SELECT MAX(main_height) FROM side_blocks) AS max_height, (SELECT MIN(main_height) FROM side_blocks) AS min_height, (SELECT MAX(height) FROM main_blocks) AS current_height;", func(row index.RowScanInterface) error {
		return row.Scan(&maxHeightPtr, &minHeightPtr, &currentHeightPtr)
	}); err != nil {
		utils.Panic(err)
	}

	var minHeight, maxHeight uint64
	if minHeightPtr != nil {
		minHeight = *minHeightPtr
	}
	if maxHeightPtr != nil {
		maxHeight = *maxHeightPtr
	}

	ctx := context.Background()

	scanHeader := func(h daemon.BlockHeader) error {
		window := make(map[types.Hash]*sidechain.PoolBlock)
		getById := func(id types.Hash) *sidechain.PoolBlock {
			if block := window[id]; block != nil {
				return block
			}
			return indexDb.GetByTemplateId(id)
		}

		if err := cmdutils.FindAndInsertMainHeader(h, indexDb, func(b *sidechain.PoolBlock) {
			p2api.InsertAlternate(b)
		}, client.GetDefaultClient(), indexDb.GetDifficultyByHeight, getById, p2api.ByMainId, p2api.LightByMainHeight, func(b *sidechain.PoolBlock) error {
			// fill cache window!
			if len(window) == 0 {
				chain, uncles := p2api.WindowFromTemplateId(b.FastSideTemplateId(p2api.Consensus()))
				for _, b := range chain {
					window[b.FastSideTemplateId(p2api.Consensus())] = b
				}
				for _, b := range uncles {
					window[b.FastSideTemplateId(p2api.Consensus())] = b
				}
			}
			_, err := b.PreProcessBlock(p2api.Consensus(), &sidechain.NilDerivationCache{}, sidechain.PreAllocateShares(p2api.Consensus().ChainWindowSize*2), indexDb.GetDifficultyByHeight, getById)
			return err
		}); err != nil {
			return err
		}
		return nil
	}

	var currentHeight uint64
	if currentHeightPtr == nil {
		//latest
		currentHeight = min(minHeight, p2api.MainTip().Height-10000)
	} else {
		currentHeight = *currentHeightPtr
	}

	heightCount := maxHeight - 1 - currentHeight + 1

	const strideSize = 1000
	strides := heightCount / strideSize

	//backfill headers
	for stride := uint64(0); stride <= strides; stride++ {
		start := currentHeight + stride*strideSize
		end := min(maxHeight-1, currentHeight+stride*strideSize+strideSize)
		utils.Logf("", "checking %d to %d", start, end)
		if headers, err := client.GetDefaultClient().GetBlockHeadersRangeResult(start, end, ctx); err != nil {
			utils.Panic(err)
		} else {
			for _, h := range headers.Headers {
				if err := scanHeader(h); err != nil {
					utils.Panic(err)
					continue
				}
			}
		}
	}

	//scan blocks that we missed that have exact main id
	if err = indexDb.Query(`
SELECT
	m.id AS main_id,
	m.height AS main_height,
	s.template_id AS template_id,
	s.side_height AS side_height
FROM
	(SELECT * FROM main_blocks WHERE root_hash IS NULL AND side_template_id IS NULL) AS m
	JOIN
	(SELECT * FROM side_blocks) AS s ON s.main_id = m.id ORDER BY side_height DESC;
`, func(row index.RowScanInterface) error {
		var mainId, templateId types.Hash
		var mainHeight, sideHeight uint64

		err := row.Scan(&mainId, &mainHeight, &templateId, &sideHeight)
		if err != nil {
			return err
		}

		if !slices.Contains(backfillScan, mainId) {
			backfillScan = append(backfillScan, mainId)
		}

		return nil
	}); err != nil {
		utils.Panic(err)
	}

	// backfill any missing headers when p2pool was down
	for _, mainId := range backfillScan {
		utils.Logf("", "checking backfill %s", mainId)
		if header, err := client.GetDefaultClient().GetBlockHeaderByHash(mainId, ctx); err != nil {
			utils.Errorf("", "not found %s", mainId)
		} else {
			if err := scanHeader(*header); err != nil {
				utils.Panic(err)
				continue
			}
		}
	}

	setupEventHandler(p2api, indexDb)

	var doCheckOfOldBlocks atomic.Bool

	doCheckOfOldBlocks.Store(true)

	go func() {
		//do deep scan for any missed main headers or deep reorgs every once in a while
		for range time.Tick(time.Second * monero.BlockTime) {
			if !doCheckOfOldBlocks.Load() {
				continue
			}
			mainTip := indexDb.GetMainBlockTip()
			for h := mainTip.Height; h >= 0 && h >= (mainTip.Height-monero.TransactionUnlockTime); h-- {
				header := indexDb.GetMainBlockByHeight(h)
				if header == nil {
					break
				}
				cur, _ := client.GetDefaultClient().GetBlockHeaderByHash(header.Id, ctx)
				if cur == nil {
					break
				}
				go func() {
					sideBlocksLock.Lock()
					defer sideBlocksLock.Unlock()
					if err := scanHeader(*cur); err != nil {
						utils.Panic(err)
					}
				}()
			}
		}
	}()

	go func() {
		//process older full blocks and sweeps
		for range time.Tick(time.Second * monero.BlockTime) {

			actualTip := indexDb.GetMainBlockTip()
			mainTip := actualTip
			maxDepth := mainTip.Height - randomx.SeedHashEpochBlocks*4

			//find top start height
			for h := mainTip.Height - monero.TransactionUnlockTime; h >= maxDepth; h-- {
				mainTip = indexDb.GetMainBlockByHeight(h)
				if mainTip == nil {
					continue
				}
				if isProcessed, ok := mainTip.GetMetadata("processed").(bool); ok && isProcessed {
					break
				}
			}

			if mainTip.Height == maxDepth {
				utils.Logf("", "Reached maxdepth %d: Use scansweeps to backfill data", maxDepth)
			}

			for h := mainTip.Height - monero.MinerRewardUnlockTime; h <= actualTip.Height-monero.TransactionUnlockTime; h++ {
				b := indexDb.GetMainBlockByHeight(h)
				if b == nil {
					continue
				}
				if isProcessed, ok := b.GetMetadata("processed").(bool); ok && isProcessed {
					continue
				}

				if err := cmdutils.ProcessFullBlock(b, indexDb); err != nil {
					utils.Logf("", "error processing block %s at %d: %s", b.Id, b.Height, err)
				}
			}
		}
	}()

	if *debugListen != "" {
		go func() {
			if err := http.ListenAndServe(*debugListen, nil); err != nil {
				utils.Panic(err)
			}
		}()
	}

	for range time.Tick(time.Second * 1) {
		currentTip := indexDb.GetSideBlockTip()
		currentMainTip := indexDb.GetMainBlockTip()

		tip := p2api.Tip()
		mainTip := p2api.MainTip()

		if tip == nil || mainTip == nil || currentTip == nil {
			utils.Errorf("", "could not fetch tip or main tip")
			continue
		}

		if tip.FastSideTemplateId(p2api.Consensus()) != currentTip.TemplateId {
			if tip.Side.Height < currentTip.SideHeight {
				//wtf
				utils.Panicf("tip height less than ours, abort: %d < %d", tip.Side.Height, currentTip.SideHeight)
			} else {
				func() {
					sideBlocksLock.Lock()
					defer sideBlocksLock.Unlock()
					insertFromTip(tip)
				}()
			}
		}

		if mainTip.Id != currentMainTip.Id {
			if mainTip.Height < currentMainTip.Height {
				//wtf
				utils.Panicf("main tip height less than ours, abort: %d < %d", mainTip.Height, currentMainTip.Height)
			} else {
				var prevHash types.Hash
				for cur, _ := client.GetDefaultClient().GetBlockHeaderByHash(mainTip.Id, ctx); cur != nil; cur, _ = client.GetDefaultClient().GetBlockHeaderByHash(prevHash, ctx) {
					curDb := indexDb.GetMainBlockByHeight(cur.Height)
					if curDb != nil {
						if curDb.Id == cur.Hash {
							break
						} else { //there has been a swap
							doCheckOfOldBlocks.Store(true)
						}
					}
					utils.Logf("MAIN", "Insert main block %d, id %s", cur.Height, cur.Hash)
					func() {
						sideBlocksLock.Lock()
						defer sideBlocksLock.Unlock()
						doCheckOfOldBlocks.Store(true)

						if err := scanHeader(*cur); err != nil {
							utils.Panic(err)
						}
						prevHash = cur.PrevHash
					}()
				}
			}
		}
	}
}
