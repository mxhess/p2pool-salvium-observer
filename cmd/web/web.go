package main

import (
	"bytes"
	"flag"
	"fmt"
	"math"
	"net/http"
	_ "net/http/pprof"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	address2 "git.gammaspectra.live/P2Pool/consensus/v4/monero/address"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/client"
	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/sidechain"
	types2 "git.gammaspectra.live/P2Pool/consensus/v4/p2pool/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
	"git.gammaspectra.live/P2Pool/go-json"
	"git.gammaspectra.live/P2Pool/observer-cmd-utils/api"
	"git.gammaspectra.live/P2Pool/observer-cmd-utils/index"
	cmdutils "git.gammaspectra.live/P2Pool/observer-cmd-utils/utils"
	"git.gammaspectra.live/P2Pool/observer/cmd/web/views"
	"github.com/gorilla/mux"
	"github.com/valyala/quicktemplate"
)

func toUint64(t any) uint64 {
	if x, ok := t.(uint64); ok {
		return x
	} else if x, ok := t.(int64); ok {
		return uint64(x)
	} else if x, ok := t.(uint); ok {
		return uint64(x)
	} else if x, ok := t.(int); ok {
		return uint64(x)
	} else if x, ok := t.(uint32); ok {
		return uint64(x)
	} else if x, ok := t.(types2.SoftwareId); ok {
		return uint64(x)
	} else if x, ok := t.(types2.SoftwareVersion); ok {
		return uint64(x)
	} else if x, ok := t.(int32); ok {
		return uint64(x)
	} else if x, ok := t.(float64); ok {
		return uint64(x)
	} else if x, ok := t.(float32); ok {
		return uint64(x)
	} else if x, ok := t.(string); ok {
		if n, err := strconv.ParseUint(x, 10, 0); err == nil {
			return n
		}
	}

	return 0
}

func toString(t any) string {

	if s, ok := t.(string); ok {
		return s
	} else if h, ok := t.(types.Hash); ok {
		return h.String()
	}

	return ""
}

func toInt64(t any) int64 {
	if x, ok := t.(uint64); ok {
		return int64(x)
	} else if x, ok := t.(int64); ok {
		return x
	} else if x, ok := t.(uint); ok {
		return int64(x)
	} else if x, ok := t.(uint32); ok {
		return int64(x)
	} else if x, ok := t.(int32); ok {
		return int64(x)
	} else if x, ok := t.(int); ok {
		return int64(x)
	} else if x, ok := t.(float64); ok {
		return int64(x)
	} else if x, ok := t.(float32); ok {
		return int64(x)
	} else if x, ok := t.(string); ok {
		if n, err := strconv.ParseInt(x, 10, 0); err == nil {
			return n
		}
	}

	return 0
}

func toFloat64(t any) float64 {
	if x, ok := t.(float64); ok {
		return x
	} else if x, ok := t.(float32); ok {
		return float64(x)
	} else if x, ok := t.(uint64); ok {
		return float64(x)
	} else if x, ok := t.(int64); ok {
		return float64(x)
	} else if x, ok := t.(uint); ok {
		return float64(x)
	} else if x, ok := t.(int); ok {
		return float64(x)
	} else if x, ok := t.(string); ok {
		if n, err := strconv.ParseFloat(x, 0); err == nil {
			return n
		}
	}

	return 0
}

//go:generate go run github.com/valyala/quicktemplate/qtc@v1.8.0 -dir=views
func main() {

	var responseBufferPool sync.Pool
	responseBufferPool.New = func() any {
		return make([]byte, 0, 1024*1024) //1 MiB allocations
	}

	//monerod related
	moneroHost := flag.String("host", "127.0.0.1", "IP address of your Salvium node")
	moneroRpcPort := flag.Uint("rpc-port", 19081, "salviumd RPC API port number")
	debugListen := flag.String("debug-listen", "", "Provide a bind address and port to expose a pprof HTTP API on it.")
	flag.Parse()

	client.SetDefaultClientSettings(fmt.Sprintf("http://%s:%d", *moneroHost, *moneroRpcPort))

	var ircLinkTitle, ircLink, webchatLink, matrixLink string
	ircUrl, err := url.Parse(os.Getenv("SITE_IRC_URL"))
	if err == nil && ircUrl.Host != "" {
		ircLink = ircUrl.String()
		humanHost := ircUrl.Host
		splitChan := strings.Split(ircUrl.Fragment, "/")

		if len(splitChan) > 1 {
			ircLink = strings.ReplaceAll(ircLink, "/"+splitChan[1], "")
		}

		switch strings.Split(humanHost, ":")[0] {
		case "irc.libera.chat":
			if len(splitChan) > 1 {
				matrixLink = fmt.Sprintf("https://matrix.to/#/#%s:%s?via=matrix.org&via=%s", splitChan[0], splitChan[1], splitChan[1])
				webchatLink = fmt.Sprintf("https://web.libera.chat/?nick=Guest?#%s", splitChan[0])
			} else {
				humanHost = "libera.chat"
				matrixLink = fmt.Sprintf("https://matrix.to/#/#%s:%s?via=matrix.org", ircUrl.Fragment, humanHost)
				webchatLink = fmt.Sprintf("https://web.libera.chat/?nick=Guest%%3F#%s", ircUrl.Fragment)
			}
		case "irc.hackint.org":
			if len(splitChan) > 1 {
				matrixLink = fmt.Sprintf("https://matrix.to/#/#%s:%s?via=matrix.org&via=%s", splitChan[0], splitChan[1], splitChan[1])
			} else {
				humanHost = "hackint.org"
				matrixLink = fmt.Sprintf("https://matrix.to/#/#%s:%s?via=matrix.org", ircUrl.Fragment, humanHost)
			}
		default:
			if len(splitChan) > 1 {
				matrixLink = fmt.Sprintf("https://matrix.to/#/#%s:%s?via=matrix.org&via=%s", splitChan[0], splitChan[1], splitChan[1])
			}
		}
		ircLinkTitle = fmt.Sprintf("#%s@%s", splitChan[0], humanHost)
	}

	var basePoolInfo *cmdutils.PoolInfoResult

	for {
		t := getTypeFromAPI[cmdutils.PoolInfoResult]("pool_info")
		if t == nil {
			time.Sleep(1)
			continue
		}
		if t.SideChain.LastBlock != nil {
			basePoolInfo = t
			break
		}
		time.Sleep(1)
	}

	consensusData, _ := utils.MarshalJSON(basePoolInfo.SideChain.Consensus)
	consensus, err := sidechain.NewConsensusFromJSON(consensusData)
	if err != nil {
		utils.Panic(err)
	}

	utils.Logf("Consensus", "Consensus id = %s", consensus.Id)

	var lastPoolInfo atomic.Pointer[cmdutils.PoolInfoResult]

	ensureGetLastPoolInfo := func() *cmdutils.PoolInfoResult {
		poolInfo := lastPoolInfo.Load()
		if poolInfo == nil {
			poolInfo = getTypeFromAPI[cmdutils.PoolInfoResult]("pool_info", 5)
			lastPoolInfo.Store(poolInfo)
		}
		return poolInfo
	}

	baseContext := views.GlobalRequestContext{
		DonationAddress: types.DonationAddress,
		SiteTitle:       os.Getenv("SITE_TITLE"),
		//TODO change to args
		NetServiceAddress: os.Getenv("NET_SERVICE_ADDRESS"),
		TorServiceAddress: os.Getenv("TOR_SERVICE_ADDRESS"),
		BasePath:          os.Getenv("BASE_PATH"),
		Consensus:         consensus,
		Pool:              nil,
	}

	baseContext.Socials.Irc.Link = ircLink
	baseContext.Socials.Irc.Title = ircLinkTitle
	baseContext.Socials.Irc.WebChat = webchatLink
	baseContext.Socials.Matrix.Link = matrixLink

	renderPage := func(request *http.Request, writer http.ResponseWriter, page views.ContextSetterPage, pool ...*cmdutils.PoolInfoResult) {
		w := bytes.NewBuffer(responseBufferPool.Get().([]byte))
		defer func() {
			defer responseBufferPool.Put(w.Bytes()[:0])
			_, _ = writer.Write(w.Bytes())
		}()

		ctx := baseContext
		ctx.IsOnion = request.Host == ctx.TorServiceAddress
		if len(pool) == 0 || pool[0] == nil {
			ctx.Pool = lastPoolInfo.Load()
		} else {
			ctx.Pool = pool[0]
		}

		defer func() {
			if err := recover(); err != nil {

				defer func() {
					// error page error'd
					if err := recover(); err != nil {
						w = bytes.NewBuffer(nil)
						writer.Header().Set("content-type", "text/plain")
						_, _ = w.Write([]byte(fmt.Sprintf("%s", err)))
					}
				}()
				w = bytes.NewBuffer(nil)
				writer.WriteHeader(http.StatusInternalServerError)
				errorPage := views.NewErrorPage(http.StatusInternalServerError, "Internal Server Error", err)
				errorPage.SetContext(&ctx)

				views.WritePageTemplate(w, errorPage)
			}
		}()

		page.SetContext(&ctx)

		bufferedWriter := quicktemplate.AcquireWriter(w)
		defer quicktemplate.ReleaseWriter(bufferedWriter)
		views.StreamPageTemplate(bufferedWriter, page)
	}

	serveMux := mux.NewRouter()

	serveMux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		params := request.URL.Query()
		refresh := 0
		if params.Has("refresh") {
			writer.Header().Set("refresh", "120")
			refresh = 100
		}

		poolInfo := getTypeFromAPI[cmdutils.PoolInfoResult]("pool_info", 5)
		lastPoolInfo.Store(poolInfo)

		secondsPerBlock := float64(poolInfo.MainChain.Difficulty.Lo) / float64(poolInfo.SideChain.LastBlock.Difficulty/consensus.TargetBlockTime)

		blocksToFetch := uint64(math.Ceil((((time.Hour*24).Seconds()/secondsPerBlock)*2)/100) * 100)

		blocks := getSliceFromAPI[*index.FoundBlock](fmt.Sprintf("found_blocks?limit=%d", blocksToFetch), 5)
		shares := getSideBlocksFromAPI("side_blocks?limit=50", 5)

		windowDurationSeconds := poolInfo.SideChain.Consensus.TargetBlockTime * poolInfo.SideChain.Consensus.ChainWindowSize
		windowDuration := time.Second * time.Duration(windowDurationSeconds)
		windowsPerDay := uint64((time.Hour * 24) / windowDuration)
		if (time.Hour*24)%windowDuration > 0 {
			windowsPerDay++
		}

		blocksFound := cmdutils.NewPositionChart(30*windowsPerDay, consensus.ChainWindowSize*windowsPerDay)

		tip := int64(poolInfo.SideChain.LastBlock.SideHeight)
		for _, b := range blocks {
			blocksFound.Add(int(tip-int64(b.SideHeight)), 1)
		}

		if len(blocks) > 20 {
			blocks = blocks[:20]
		}

		renderPage(request, writer, &views.IndexPage{
			Refresh: refresh,
			Positions: struct {
				BlocksFound *cmdutils.PositionChart
			}{
				BlocksFound: blocksFound,
			},
			Shares:      shares,
			FoundBlocks: blocks,
		}, poolInfo)
	})

	serveMux.HandleFunc("/api", func(writer http.ResponseWriter, request *http.Request) {
		poolInfo := *ensureGetLastPoolInfo()
		if len(poolInfo.SideChain.Effort.Last) > 5 {
			poolInfo.SideChain.Effort.Last = poolInfo.SideChain.Effort.Last[:5]
		}

		p := &views.ApiPage{
			PoolInfoExample: poolInfo,
		}
		renderPage(request, writer, p)
	})

	serveMux.HandleFunc("/calculate-share-time", func(writer http.ResponseWriter, request *http.Request) {
		poolInfo := getTypeFromAPI[cmdutils.PoolInfoResult]("pool_info", 5)
		lastPoolInfo.Store(poolInfo)
		hashRate := float64(0)
		magnitude := float64(1000)

		params := request.URL.Query()
		if params.Has("hashrate") {
			hashRate = toFloat64(params.Get("hashrate"))
		}
		if params.Has("magnitude") {
			magnitude = toFloat64(params.Get("magnitude"))
		}

		currentHashRate := magnitude * hashRate

		var effortSteps = []float64{25, 50, 75, 100, 150, 200, 300, 400, 500, 600, 700, 800, 900, 1000}
		const shareSteps = 15

		calculatePage := &views.CalculateShareTimePage{
			Hashrate:              hashRate,
			Magnitude:             magnitude,
			Efforts:               nil,
			EstimatedRewardPerDay: 0,
			EstimatedSharesPerDay: 0,
			EstimatedBlocksPerDay: 0,
		}

		if currentHashRate > 0 {
			var efforts []views.CalculateShareTimePageEffortEntry

			for _, effort := range effortSteps {
				e := views.CalculateShareTimePageEffortEntry{
					Effort:             effort,
					Probability:        cmdutils.ProbabilityEffort(effort) * 100,
					Between:            (float64(poolInfo.SideChain.LastBlock.Difficulty) * (effort / 100)) / currentHashRate,
					BetweenSolo:        (float64(poolInfo.MainChain.Difficulty.Lo) * (effort / 100)) / currentHashRate,
					ShareProbabilities: make([]float64, shareSteps+1),
				}

				for i := uint64(0); i <= shareSteps; i++ {
					e.ShareProbabilities[i] = cmdutils.ProbabilityNShares(i, effort)
				}

				efforts = append(efforts, e)
			}
			calculatePage.Efforts = efforts

			longWeight := types.DifficultyFrom64(uint64(currentHashRate)).Mul64(3600 * 24)
			calculatePage.EstimatedSharesPerDay = float64(longWeight.Mul64(1000).Div64(poolInfo.SideChain.LastBlock.Difficulty).Lo) / 1000
			calculatePage.EstimatedBlocksPerDay = float64(longWeight.Mul64(1000).Div(poolInfo.MainChain.NextDifficulty).Lo) / 1000
			calculatePage.EstimatedRewardPerDay = longWeight.Mul64(poolInfo.MainChain.BaseReward).Div(poolInfo.MainChain.NextDifficulty).Lo
		}

		renderPage(request, writer, calculatePage, poolInfo)
	})

	serveMux.HandleFunc("/connectivity-check", func(writer http.ResponseWriter, request *http.Request) {

		params := request.URL.Query()

		var addressPort netip.AddrPort
		var err error
		if params.Has("address") {
			addressPort, err = netip.ParseAddrPort(params.Get("address"))
			if err != nil {
				addr, err := netip.ParseAddr(params.Get("address"))
				if err == nil {
					addressPort = netip.AddrPortFrom(addr, consensus.DefaultPort())
				}
			}
		}

		if addressPort.IsValid() && !addressPort.Addr().IsUnspecified() {
			checkInformation := getTypeFromAPI[api.P2PoolConnectionCheckInformation[*sidechain.PoolBlock]]("consensus/connection_check/" + addressPort.String())
			var rawTip *sidechain.PoolBlock
			ourTip := getTypeFromAPI[index.SideBlock]("redirect/tip")
			var theirTip *index.SideBlock
			if checkInformation != nil {
				if checkInformation.Tip != nil {
					rawTip = checkInformation.Tip
					theirTip = getTypeFromAPI[index.SideBlock](fmt.Sprintf("block_by_id/%s", rawTip.FastSideTemplateId(consensus)))
				}
			}
			renderPage(request, writer, &views.ConnectivityCheckPage{
				Address:    addressPort,
				YourTip:    theirTip,
				YourTipRaw: rawTip,
				OurTip:     ourTip,
				Check:      checkInformation,
			})
		} else {

			renderPage(request, writer, &views.ConnectivityCheckPage{
				Address:    netip.AddrPort{},
				YourTip:    nil,
				YourTipRaw: nil,
				OurTip:     nil,
				Check:      nil,
			})
		}
	})



	serveMux.HandleFunc("/blocks", func(writer http.ResponseWriter, request *http.Request) {
		params := request.URL.Query()
		refresh := 0
		if params.Has("refresh") {
			writer.Header().Set("refresh", "600")
			refresh = 600
		}

		var miner *cmdutils.MinerInfoResult
		if params.Has("miner") {
			miner = getTypeFromAPI[cmdutils.MinerInfoResult](fmt.Sprintf("miner_info/%s?noShares", params.Get("miner")))
			if miner == nil || miner.Address == nil {
				renderPage(request, writer, views.NewErrorPage(http.StatusNotFound, "Address Not Found", "You need to have mined at least one share in the past. Come back later :)"))
				return
			}
		}

		poolInfo := getTypeFromAPI[cmdutils.PoolInfoResult]("pool_info", 5)
		lastPoolInfo.Store(poolInfo)

		if miner != nil {
			renderPage(request, writer, &views.BlocksPage{
				Refresh:     refresh,
				FoundBlocks: getSliceFromAPI[*index.FoundBlock](fmt.Sprintf("found_blocks?&limit=100&miner=%d", miner.Id)),
				Miner:       GetPayout(miner.Address, miner.PayoutAddress),
			}, poolInfo)
		} else {
			renderPage(request, writer, &views.BlocksPage{
				Refresh:     refresh,
				FoundBlocks: getSliceFromAPI[*index.FoundBlock]("found_blocks?limit=100", 30),
				Miner:       nil,
			}, poolInfo)
		}
	})

	serveMux.HandleFunc("/miners", func(writer http.ResponseWriter, request *http.Request) {
		params := request.URL.Query()
		if params.Has("refresh") {
			writer.Header().Set("refresh", "600")
		}

		poolInfo := getTypeFromAPI[cmdutils.PoolInfoResult]("pool_info", 5)
		lastPoolInfo.Store(poolInfo)

		currentWindowSize := uint64(poolInfo.SideChain.Window.Blocks)
		windowSize := currentWindowSize
		if poolInfo.SideChain.LastBlock.SideHeight <= windowSize {
			windowSize = consensus.ChainWindowSize
		}
		size := uint64(30)
		cacheTime := 30
		if params.Has("weekly") {
			windowSize = consensus.ChainWindowSize * 4 * 7
			size *= 2
			if params.Has("refresh") {
				writer.Header().Set("refresh", "3600")
			}
			cacheTime = 60
		}

		shares := getSideBlocksFromAPI(fmt.Sprintf("side_blocks_in_window?window=%d&noMainStatus&noUncles", windowSize), cacheTime)

		// Handle empty shares (pool just started or PPLNS window broken)
		if len(shares) == 0 {
			renderPage(request, writer, views.NewErrorPage(http.StatusServiceUnavailable, "No Shares in PPLNS Window", "The P2Pool sidechain has no shares yet. Please wait for mining activity to start."))
			return
		}

		miners := make(map[uint64]*views.MinersPageMinerEntry)

		tipHeight := poolInfo.SideChain.LastBlock.SideHeight
		wend := tipHeight - windowSize

		tip := shares[0]

		createMiner := func(miner uint64, share *index.SideBlock) {
			if _, ok := miners[miner]; !ok {
				miners[miner] = &views.MinersPageMinerEntry{
					Address:         share.MinerAddress,
					PayoutAddress:   share.MinerPayoutAddress,
					Alias:           share.MinerAlias,
					SoftwareId:      share.SoftwareId,
					SoftwareVersion: share.SoftwareVersion,
					Shares:          cmdutils.NewPositionChart(size, windowSize),
					Uncles:          cmdutils.NewPositionChart(size, windowSize),
				}
			}
		}

		var totalWeight types.Difficulty
		var uncleShareIndex int
		for i, share := range shares {
			miner := share.Miner

			if share.IsUncle() {
				if share.SideHeight <= wend {
					continue
				}
				createMiner(share.Miner, share)
				miners[miner].Uncles.Add(int(int64(tip.SideHeight)-int64(share.SideHeight)), 1)

				uncleWeight, unclePenalty := consensus.ApplyUnclePenalty(types.DifficultyFrom64(share.Difficulty))

				if shares[uncleShareIndex].TemplateId == share.UncleOf {
					parent := shares[uncleShareIndex]
					createMiner(parent.Miner, parent)
					miners[parent.Miner].Weight = miners[parent.Miner].Weight.Add64(unclePenalty.Lo)
				}
				miners[miner].Weight = miners[miner].Weight.Add64(uncleWeight.Lo)

				totalWeight = totalWeight.Add64(share.Difficulty)
			} else {
				uncleShareIndex = i
				createMiner(share.Miner, share)
				miners[miner].Shares.Add(int(int64(tip.SideHeight)-int64(share.SideHeight)), 1)
				miners[miner].Weight = miners[miner].Weight.Add64(share.Difficulty)
				totalWeight = totalWeight.Add64(share.Difficulty)
			}
		}

		minerKeys := utils.Keys(miners)
		slices.SortFunc(minerKeys, func(a uint64, b uint64) int {
			return miners[a].Weight.Cmp(miners[b].Weight) * -1
		})

		sortedMiners := make([]*views.MinersPageMinerEntry, len(minerKeys))

		for i, k := range minerKeys {
			sortedMiners[i] = miners[k]
		}

		renderPage(request, writer, &views.MinersPage{
			Refresh:      0,
			Weekly:       params.Has("weekly"),
			Miners:       sortedMiners,
			WindowWeight: totalWeight,
		}, poolInfo)
	})

	serveMux.HandleFunc("/share/{block:[0-9a-f]+|[0-9]+}", func(writer http.ResponseWriter, request *http.Request) {
		identifier := mux.Vars(request)["block"]

		var block *index.SideBlock
		var coinbase index.MainCoinbaseOutputs

		if len(identifier) == 64 {
			block = getTypeFromAPI[index.SideBlock](fmt.Sprintf("block_by_id/%s", identifier))
		} else {
			block = getTypeFromAPI[index.SideBlock](fmt.Sprintf("block_by_height/%s", identifier))
		}

		if block == nil {
			renderPage(request, writer, views.NewErrorPage(http.StatusNotFound, "Share Not Found", nil))
			return
		}

		raw := GetPoolBlockFromAPIRaw(consensus, block.MainId)

		coinbase = getSliceFromAPI[index.MainCoinbaseOutput](fmt.Sprintf("block_by_id/%s/coinbase", block.MainId))

		poolInfo := getTypeFromAPI[cmdutils.PoolInfoResult]("pool_info", 5)
		lastPoolInfo.Store(poolInfo)

		payouts := getStreamFromAPI[*index.Payout](fmt.Sprintf("block_by_id/%s/payouts", block.MainId))


		if block.Timestamp < uint64(time.Now().Unix()-60) {
			writer.Header().Set("cache-control", "public; max-age=604800")
		} else {
			writer.Header().Set("cache-control", "public; max-age=60")
		}

		renderPage(request, writer, &views.SharePage{
			Block:           block,
			PoolBlock:       raw,
			Payouts:         payouts,
			CoinbaseOutputs: coinbase,
		}, poolInfo)
	})

	serveMux.HandleFunc("/miner/{miner:[^ ]+}", func(writer http.ResponseWriter, request *http.Request) {
		params := request.URL.Query()
		refresh := 0
		if params.Has("refresh") {
			writer.Header().Set("refresh", "300")
			refresh = 300
		}
		address := mux.Vars(request)["miner"]

		minerPayoutAddress := address2.FromBase58(address)

		miner := getTypeFromAPI[cmdutils.MinerInfoResult](fmt.Sprintf("miner_info/%s?shareEstimates", address))
		if miner == nil || miner.Address == nil {
			if addr := address2.FromBase58(address); addr != nil {
				miner = &cmdutils.MinerInfoResult{
					Id:                 0,
					Address:            addr,
					LastShareHeight:    0,
					LastShareTimestamp: 0,
				}
			} else {
				renderPage(request, writer, views.NewErrorPage(http.StatusNotFound, "Invalid Address", nil))
				return
			}
		}

		if minerPayoutAddress != nil && minerPayoutAddress.IsSubaddress() && miner.PayoutAddress == nil && minerPayoutAddress.SpendPub == miner.Address.SpendPub {
			miner.PayoutAddress = minerPayoutAddress
		}

		poolInfo := getTypeFromAPI[cmdutils.PoolInfoResult]("pool_info", 5)
		lastPoolInfo.Store(poolInfo)

		windowDurationSeconds := poolInfo.SideChain.Consensus.TargetBlockTime * poolInfo.SideChain.Consensus.ChainWindowSize
		windowDuration := time.Second * time.Duration(windowDurationSeconds)
		windowsPerDay := uint64((time.Hour * 24) / windowDuration)
		if (time.Hour*24)%windowDuration > 0 {
			windowsPerDay++
		}

		wsize := consensus.ChainWindowSize * windowsPerDay

		currentWindowSize := uint64(poolInfo.SideChain.Window.Blocks)

		tipHeight := poolInfo.SideChain.LastBlock.SideHeight

		var shares, lastShares, lastOrphanedShares []*index.SideBlock

		var lastFound []*index.FoundBlock
		var payouts []*index.Payout

		var raw *sidechain.PoolBlock

		if miner.Id != 0 {
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				shares = getSideBlocksFromAPI(fmt.Sprintf("side_blocks_in_window/%d?from=%d&window=%d&noMiner&noMainStatus&noUncles", miner.Id, tipHeight, wsize))
			}()
			wg.Add(1)
			go func() {
				defer wg.Done()
				lastShares = getSideBlocksFromAPI(fmt.Sprintf("side_blocks?limit=50&miner=%d", miner.Id))

				if len(lastShares) > 0 {
					raw = getTypeFromAPI[sidechain.PoolBlock](fmt.Sprintf("block_by_id/%s/light", lastShares[0].MainId))
					if raw == nil || raw.ShareVersion() == sidechain.ShareVersion_None {
						raw = nil
					}
				}
			}()
			wg.Add(1)
			go func() {
				defer wg.Done()
				lastOrphanedShares = getSideBlocksFromAPI(fmt.Sprintf("side_blocks?limit=10&miner=%d&inclusion=%d", miner.Id, index.InclusionOrphan))
			}()
			wg.Add(1)
			go func() {
				defer wg.Done()
				lastFound = getSliceFromAPI[*index.FoundBlock](fmt.Sprintf("found_blocks?limit=10&miner=%d", miner.Id))
			}()
			wg.Add(1)
			go func() {
				defer wg.Done()
				shares = getSideBlocksFromAPI(fmt.Sprintf("side_blocks_in_window/%d?from=%d&window=%d&noMiner&noMainStatus&noUncles", miner.Id, tipHeight, wsize))
			}()
			wg.Add(1)
			go func() {
				defer wg.Done()
				//get a bit over the expected required, minimum two weeks
				t := max(time.Hour*24*7*2, time.Duration(consensus.ChainWindowSize*consensus.TargetBlockTime*(windowsPerDay+1))*time.Second)
				payouts = getSliceFromAPI[*index.Payout](fmt.Sprintf("payouts/%d?from_timestamp=%d", miner.Id, time.Now().Add(-t).Unix()))
			}()
			wg.Wait()
		} else {
		}

		sharesInWindow := cmdutils.NewPositionChart(30, uint64(poolInfo.SideChain.Window.Blocks))
		unclesInWindow := cmdutils.NewPositionChart(30, uint64(poolInfo.SideChain.Window.Blocks))

		sharesFound := cmdutils.NewPositionChart(30*windowsPerDay, consensus.ChainWindowSize*windowsPerDay)
		unclesFound := cmdutils.NewPositionChart(30*windowsPerDay, consensus.ChainWindowSize*windowsPerDay)

		var longDiff, windowDiff types.Difficulty

		wend := tipHeight - currentWindowSize

		foundPayout := cmdutils.NewPositionChart(30*windowsPerDay, consensus.ChainWindowSize*windowsPerDay)
		for _, p := range payouts {
			foundPayout.Add(int(int64(tipHeight)-int64(p.SideHeight)), 1)
		}

		for _, share := range shares {
			if share.IsUncle() {

				unclesFound.Add(int(int64(tipHeight)-int64(share.SideHeight)), 1)

				uncleWeight, unclePenalty := consensus.ApplyUnclePenalty(types.DifficultyFrom64(share.Difficulty))

				if i := slices.IndexFunc(shares, func(block *index.SideBlock) bool {
					return block.TemplateId == share.UncleOf
				}); i != -1 {
					if shares[i].SideHeight > wend {
						windowDiff = windowDiff.Add64(unclePenalty.Lo)
					}
					longDiff = longDiff.Add64(unclePenalty.Lo)
				}
				if share.SideHeight > wend {
					windowDiff = windowDiff.Add64(uncleWeight.Lo)
					unclesInWindow.Add(int(int64(tipHeight)-int64(share.SideHeight)), 1)
				}
				longDiff = longDiff.Add64(uncleWeight.Lo)
			} else {
				sharesFound.Add(int(int64(tipHeight)-toInt64(share.SideHeight)), 1)
				if share.SideHeight > wend {
					windowDiff = windowDiff.Add64(share.Difficulty)
					sharesInWindow.Add(int(int64(tipHeight)-toInt64(share.SideHeight)), 1)
				}
				longDiff = longDiff.Add64(share.Difficulty)
			}
		}

		if len(payouts) > 10 {
			payouts = payouts[:10]
		}

		minerPage := &views.MinerPage{
			Refresh: refresh,
			Positions: struct {
				Resolution       int
				ResolutionWindow int
				SeparatorIndex   int
				Blocks           *cmdutils.PositionChart
				Uncles           *cmdutils.PositionChart
				BlocksInWindow   *cmdutils.PositionChart
				UnclesInWindow   *cmdutils.PositionChart
				Payouts          *cmdutils.PositionChart
			}{
				Resolution:       int(foundPayout.Resolution()),
				ResolutionWindow: int(sharesInWindow.Resolution()),
				SeparatorIndex:   int(consensus.ChainWindowSize*windowsPerDay - currentWindowSize),
				Blocks:           sharesFound,
				BlocksInWindow:   sharesInWindow,
				Uncles:           unclesFound,
				UnclesInWindow:   unclesInWindow,
				Payouts:          foundPayout,
			},
			Weight:             longDiff.Lo,
			WindowWeight:       windowDiff.Lo,
			Miner:              miner,
			LastPoolBlock:      raw,
			LastShares:         lastShares,
			LastOrphanedShares: lastOrphanedShares,
			LastFound:          lastFound,
			LastPayouts:        payouts,
		}

		dailyWeight := poolInfo.SideChain.Window.Weight.Mul64(windowDurationSeconds * windowsPerDay).Div64(uint64(poolInfo.SideChain.Window.Blocks))

		if windowDiff.Cmp64(0) > 0 {
			averageRewardPerBlock := longDiff.Mul64(poolInfo.MainChain.BaseReward).Div(dailyWeight).Lo
			minerPage.ExpectedRewardPerDay = dailyWeight.Mul64(averageRewardPerBlock).Div(poolInfo.MainChain.NextDifficulty).Lo

			expectedRewardNextBlock := windowDiff.Mul64(poolInfo.MainChain.BaseReward).Div(poolInfo.SideChain.Window.Weight).Lo
			minerPage.ExpectedRewardPerWindow = poolInfo.SideChain.Window.Weight.Mul64(expectedRewardNextBlock).Div(poolInfo.MainChain.NextDifficulty).Lo
		}

		dailyHashRate := types.DifficultyFrom64(poolInfo.SideChain.LastBlock.Difficulty).Mul(longDiff).Div(dailyWeight).Lo

		hashRate := float64(0)
		magnitude := float64(1000)

		if dailyHashRate >= 1000000000 {
			hashRate = float64(dailyHashRate) / 1000000000
			magnitude = 1000000000
		} else if dailyHashRate >= 1000000 {
			hashRate = float64(dailyHashRate) / 1000000
			magnitude = 1000000
		} else if dailyHashRate >= 1000 {
			hashRate = float64(dailyHashRate) / 1000
			magnitude = 1000
		}

		if params.Has("magnitude") {
			magnitude = toFloat64(params.Get("magnitude"))
		}
		if params.Has("hashrate") {
			hashRate = toFloat64(params.Get("hashrate"))

			if hashRate > 0 && magnitude > 0 {
				dailyHashRate = uint64(hashRate * magnitude)
			}
			minerPage.HashrateSubmit = true
		}

		minerPage.HashrateLocal = hashRate
		minerPage.MagnitudeLocal = magnitude

		efforts := make([]float64, len(lastShares))
		for i := len(lastShares) - 1; i >= 0; i-- {
			s := lastShares[i]
			if i == (len(lastShares) - 1) {
				efforts[i] = -1
				continue
			}
			previous := lastShares[i+1]

			timeDelta := uint64(max(int64(s.Timestamp)-int64(previous.Timestamp), 0))

			expectedCumDiff := types.DifficultyFrom64(dailyHashRate).Mul64(timeDelta)

			efforts[i] = float64(expectedCumDiff.Mul64(100).Lo) / float64(s.Difficulty)
		}
		minerPage.LastSharesEfforts = efforts

		renderPage(request, writer, minerPage, poolInfo)
	})

	serveMux.HandleFunc("/miner-options/{miner:[^ ]+}/signed_action", func(writer http.ResponseWriter, request *http.Request) {
		params := request.URL.Query()

		address := mux.Vars(request)["miner"]
		miner := getTypeFromAPI[cmdutils.MinerInfoResult](fmt.Sprintf("miner_info/%s?noShares", address))
		if miner == nil || miner.Address == nil {
			renderPage(request, writer, views.NewErrorPage(http.StatusNotFound, "Address Not Found", "You need to have mined at least one share in the past. Come back later :)"))
			return
		}

		var signedAction *cmdutils.SignedAction

		if err := json.Unmarshal([]byte(params.Get("message")), &signedAction); err != nil || signedAction == nil {
			renderPage(request, writer, views.NewErrorPage(http.StatusBadRequest, "Invalid Message", nil))
			return
		}

		values := make(url.Values)
		values.Set("signature", params.Get("signature"))
		values.Set("message", signedAction.String())

		var jsonErr struct {
			Error string `json:"error"`
		}
		statusCode, buf := getFromAPI("miner_signed_action/" + string(GetPayout(miner.Address, miner.PayoutAddress).ToBase58()) + "?" + values.Encode())
		_ = json.Unmarshal(buf, &jsonErr)
		if statusCode != http.StatusOK {
			renderPage(request, writer, views.NewErrorPage(http.StatusBadRequest, "Could not verify message", jsonErr.Error))
			return
		}
	})

	serveMux.HandleFunc("/miner-options/{miner:[^ ]+}/{action:set_miner_alias|unset_miner_alias|add_webhook|remove_webhook}", func(writer http.ResponseWriter, request *http.Request) {
		params := request.URL.Query()

		address := mux.Vars(request)["miner"]
		miner := getTypeFromAPI[cmdutils.MinerInfoResult](fmt.Sprintf("miner_info/%s?noShares", address))
		if miner == nil || miner.Address == nil {
			renderPage(request, writer, views.NewErrorPage(http.StatusNotFound, "Address Not Found", "You need to have mined at least one share in the past. Come back later :)"))
			return
		}

		var signedAction *cmdutils.SignedAction

		action := mux.Vars(request)["action"]
		switch action {
		case "set_miner_alias":
			signedAction = cmdutils.SignedActionSetMinerAlias(baseContext.NetServiceAddress, params.Get("alias"))
		case "unset_miner_alias":
			signedAction = cmdutils.SignedActionUnsetMinerAlias(baseContext.NetServiceAddress)
		case "add_webhook":
			signedAction = cmdutils.SignedActionAddWebHook(baseContext.NetServiceAddress, params.Get("type"), params.Get("url"))
			for _, s := range []string{"side_blocks", "payouts", "found_blocks", "orphaned_blocks", "other"} {
				value := "false"
				if params.Get(s) == "on" {
					value = "true"
				}
				signedAction.Data = append(signedAction.Data, cmdutils.SignedActionEntry{
					Key:   "send_" + s,
					Value: value,
				})
			}
		case "remove_webhook":
			signedAction = cmdutils.SignedActionRemoveWebHook(baseContext.NetServiceAddress, params.Get("type"), params.Get("url_hash"))
		default:
			renderPage(request, writer, views.NewErrorPage(http.StatusNotFound, "Invalid Action", nil))
			return
		}

		renderPage(request, writer, &views.MinerOptionsPage{
			Miner:        miner,
			SignedAction: signedAction,
			WebHooks:     getSliceFromAPI[*index.MinerWebHook](fmt.Sprintf("miner_webhooks/%s", address)),
		})
	})

	serveMux.HandleFunc("/miner-options/{miner:[^ ]+}", func(writer http.ResponseWriter, request *http.Request) {
		//params := request.URL.Query()

		address := mux.Vars(request)["miner"]
		miner := getTypeFromAPI[cmdutils.MinerInfoResult](fmt.Sprintf("miner_info/%s?noShares", address))
		if miner == nil || miner.Address == nil {
			renderPage(request, writer, views.NewErrorPage(http.StatusNotFound, "Address Not Found", "You need to have mined at least one share in the past. Come back later :)"))
			return
		}

		renderPage(request, writer, &views.MinerOptionsPage{
			Miner:        miner,
			SignedAction: nil,
			WebHooks:     getSliceFromAPI[*index.MinerWebHook](fmt.Sprintf("miner_webhooks/%s", address)),
		})
	})

	serveMux.HandleFunc("/miner", func(writer http.ResponseWriter, request *http.Request) {
		params := request.URL.Query()
		if params.Get("address") == "" {
			http.Redirect(writer, request, "/", http.StatusMovedPermanently)
			return
		}
		http.Redirect(writer, request, fmt.Sprintf("/miner/%s", params.Get("address")), http.StatusMovedPermanently)
	})

	serveMux.HandleFunc("/proof/{block:[0-9a-f]+|[0-9]+}/{index:[0-9]+}", func(writer http.ResponseWriter, request *http.Request) {
		identifier := cmdutils.DecodeHexBinaryNumber(mux.Vars(request)["block"])
		requestIndex := toUint64(mux.Vars(request)["index"])

		block := getTypeFromAPI[index.SideBlock](fmt.Sprintf("block_by_id/%s", identifier))

		if block == nil || !block.MinedMainAtHeight {
			renderPage(request, writer, views.NewErrorPage(http.StatusNotFound, "Share Was Not Found", nil))
			return
		}

		raw := getTypeFromAPI[sidechain.PoolBlock](fmt.Sprintf("block_by_id/%s/light", block.MainId))
		if raw == nil || raw.ShareVersion() == sidechain.ShareVersion_None {
			raw = nil
		}

		payouts := getSliceFromAPI[index.MainCoinbaseOutput](fmt.Sprintf("block_by_id/%s/coinbase", block.MainId))

		if raw == nil {
			renderPage(request, writer, views.NewErrorPage(http.StatusNotFound, "Coinbase Was Not Found", nil))
			return
		}

		if uint64(len(payouts)) <= requestIndex {
			renderPage(request, writer, views.NewErrorPage(http.StatusNotFound, "Payout Was Not Found", nil))
			return
		}

		poolInfo := getTypeFromAPI[cmdutils.PoolInfoResult]("pool_info", 5)
		lastPoolInfo.Store(poolInfo)

		payout := payouts[requestIndex]

		addr := GetPayout(payout.MinerAddress, payout.MinerPayoutAddress)

		if addrStr := request.URL.Query().Get("address"); addrStr != "" {
			addr2 := address2.FromBase58(addrStr)
			if addr2 != nil && addr2.IsSubaddress() && addr2.SpendPub == payout.MinerAddress.SpendPub {
				addr = addr2
			}
		}

		if addr.IsSubaddress() {
			payout.MinerPayoutAddress = addr
		}

		/*
			proof := ""

			if addr.IsSubaddress() {
				payout.MinerPayoutAddress = addr
				//TODO
				proof = GetOutProofV2_SpecialPayout(payout.MinerAddress, addr, payout.Id, &raw.Side.CoinbasePrivateKey, "")
			} else {
				proof = address2.GetOutProofV2(payout.MinerAddress, payout.Id, &raw.Side.CoinbasePrivateKey, "")
			}
		*/
		proof := address2.GetOutProofV2(payout.MinerAddress, payout.Id, &raw.Side.CoinbasePrivateKey, "")

		renderPage(request, writer, &views.ProofPage{
			Output:   &payout,
			Block:    block,
			Raw:      raw,
			OutProof: proof,
		}, poolInfo)
	})

	serveMux.HandleFunc("/payouts/{miner:[0-9]+|[48][123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz]+}", func(writer http.ResponseWriter, request *http.Request) {
		params := request.URL.Query()
		refresh := 0
		if params.Has("refresh") {
			writer.Header().Set("refresh", "600")
			refresh = 600
		}

		address := mux.Vars(request)["miner"]
		if params.Has("address") {
			address = params.Get("address")
		}

		miner := getTypeFromAPI[cmdutils.MinerInfoResult](fmt.Sprintf("miner_info/%s?noShares", address))

		if miner == nil || miner.Address == nil {
			renderPage(request, writer, views.NewErrorPage(http.StatusNotFound, "Address Not Found", "You need to have mined at least one share in the past. Come back later :)"))
			return
		}

		payouts := getStreamFromAPI[*index.Payout](fmt.Sprintf("payouts/%d?search_limit=0", miner.Id))
		renderPage(request, writer, &views.PayoutsPage{
			Miner:   GetPayout(miner.Address, miner.PayoutAddress),
			Payouts: payouts,
			Refresh: refresh,
		})
	})

	server := &http.Server{
		Addr:        "0.0.0.0:8444",
		ReadTimeout: time.Second * 2,
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != "GET" && request.Method != "HEAD" {
				writer.WriteHeader(http.StatusForbidden)
				return
			}

			writer.Header().Set("content-type", "text/html; charset=utf-8")

			serveMux.ServeHTTP(writer, request)
		}),
	}

	if *debugListen != "" {
		go func() {
			if err := http.ListenAndServe(*debugListen, nil); err != nil {
				utils.Panic(err)
			}
		}()
	}

	if err := server.ListenAndServe(); err != nil {
		utils.Panic(err)
	}
}
