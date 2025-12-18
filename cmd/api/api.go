package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	_ "net/http/pprof"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"git.gammaspectra.live/P2Pool/consensus/v4/monero"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/address"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/block"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/client"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/crypto"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/transaction"
	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/sidechain"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
	p2poolapi "git.gammaspectra.live/P2Pool/observer-cmd-utils/api"
	"git.gammaspectra.live/P2Pool/observer-cmd-utils/httputils"
	"git.gammaspectra.live/P2Pool/observer-cmd-utils/index"
	cmdutils "git.gammaspectra.live/P2Pool/observer-cmd-utils/utils"
	"github.com/gorilla/mux"
	"github.com/redis/go-redis/v9"
	fasthex "github.com/tmthrgd/go-hex"
)

// Redis data structures matching daemon2 output
type ShareDataRedis struct {
	TemplateId      string `json:"template_id"`
	SideHeight      uint64 `json:"side_height"`
	MainHeight      uint64 `json:"main_height"`
	MainId          string `json:"main_id"`
	Timestamp       uint64 `json:"timestamp"`
	Difficulty      uint64 `json:"difficulty"`
	MinerAddress    string `json:"miner_address"`
	SoftwareId      uint32 `json:"software_id"`
	SoftwareVersion uint32 `json:"software_version"`
	UncleOf         string `json:"uncle_of,omitempty"`
	IsOrphan        bool   `json:"is_orphan"`
	WindowWeight    uint64 `json:"window_weight"`
}

type FoundBlockDataRedis struct {
	MainId          string `json:"main_id"`
	MainHeight      uint64 `json:"main_height"`
	TemplateId      string `json:"template_id"`
	SideHeight      uint64 `json:"side_height"`
	Timestamp       uint64 `json:"timestamp"`
	Reward          uint64 `json:"reward"`
	Difficulty      uint64 `json:"difficulty"`
	MinerAddress    string `json:"miner_address"`
	UncleCount      int    `json:"uncle_count"`
}

type PPLNSWindowDataRedis struct {
	Miners int    `json:"miners"`
	Blocks int    `json:"blocks"`
	Uncles int    `json:"uncles"`
	Weight uint64 `json:"weight"`
}

type EffortDataRedis struct {
	Current    float64 `json:"current"`
	Average10  float64 `json:"average10"`
	Average50  float64 `json:"average50"`
	Average200 float64 `json:"average200"`
}

// Helper functions to read from Redis
func getRedisString(ctx context.Context, rdb *redis.Client, key string) (string, error) {
	return rdb.Get(ctx, key).Result()
}

func getRedisUint64(ctx context.Context, rdb *redis.Client, key string) (uint64, error) {
	val, err := rdb.Get(ctx, key).Uint64()
	if err != nil {
		return 0, err
	}
	return val, nil
}

func getRedisJSON(ctx context.Context, rdb *redis.Client, key string, v interface{}) error {
	data, err := rdb.Get(ctx, key).Result()
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(data), v)
}

// Miner data structure for Redis
type MinerRedis struct {
	address string
	alias   string
}

func (m *MinerRedis) Id() uint64 {
	// Use hash of address as ID for compatibility
	h := sha256.Sum256([]byte(m.address))
	return uint64(h[0]) | uint64(h[1])<<8 | uint64(h[2])<<16 | uint64(h[3])<<24 |
		uint64(h[4])<<32 | uint64(h[5])<<40 | uint64(h[6])<<48 | uint64(h[7])<<56
}

func (m *MinerRedis) Address() string {
	return m.address
}

func (m *MinerRedis) Alias() string {
	return m.alias
}

func (m *MinerRedis) OnlyPayoutAddress() string {
	return m.address // In Redis model, address and payout address are the same
}

func (m *MinerRedis) PayoutAddress() string {
	return m.address
}

// Miner alias functions
func getMinerAlias(ctx context.Context, rdb *redis.Client, minerAddr string) (string, error) {
	key := fmt.Sprintf("miner:%s:alias", minerAddr)
	alias, err := rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil // No alias set
	}
	return alias, err
}

func setMinerAlias(ctx context.Context, rdb *redis.Client, minerAddr string, alias string) error {
	key := fmt.Sprintf("miner:%s:alias", minerAddr)
	if alias == "" {
		// Delete alias
		return rdb.Del(ctx, key).Err()
	}
	// Set alias with no TTL (permanent)
	return rdb.Set(ctx, key, alias, 0).Err()
}

// Get miner by address
func getMinerByAddress(ctx context.Context, rdb *redis.Client, address string) *MinerRedis {
	// Get alias if exists
	alias, _ := getMinerAlias(ctx, rdb, address)

	return &MinerRedis{
		address: address,
		alias:   alias,
	}
}

// Get miner by alias
func getMinerByAlias(ctx context.Context, rdb *redis.Client, alias string) *MinerRedis {
	// Scan all miner alias keys to find matching alias
	iter := rdb.Scan(ctx, 0, "miner:*:alias", 0).Iterator()
	for iter.Next(ctx) {
		storedAlias, err := rdb.Get(ctx, iter.Val()).Result()
		if err == nil && storedAlias == alias {
			// Extract address from key: miner:{address}:alias
			key := iter.Val()
			address := key[6 : len(key)-6] // Remove "miner:" prefix and ":alias" suffix
			return &MinerRedis{
				address: address,
				alias:   alias,
			}
		}
	}
	return nil
}

// Get miner by ID (we'll use address hash as ID)
func getMinerById(ctx context.Context, rdb *redis.Client, id uint64) *MinerRedis {
	// For ID lookup, we need to scan all miners
	// This is inefficient but necessary without maintaining an ID->address mapping
	// In practice, most lookups will be by address, not ID
	return nil // Return nil for now, as ID lookups should be avoided in Redis-only mode
}

// Tip functions
type SideChainTipRedis struct {
	TemplateId  string `json:"template_id"`
	SideHeight  uint64 `json:"side_height"`
	MainHeight  uint64 `json:"main_height"`
	MainId      string `json:"main_id"`
	Timestamp   uint64 `json:"timestamp"`
	Difficulty  uint64 `json:"difficulty"`
	WindowDepth uint64 `json:"window_depth"`
}

func getSideBlockTipRedis(ctx context.Context, rdb *redis.Client) *SideChainTipRedis {
	var tip SideChainTipRedis
	if err := getRedisJSON(ctx, rdb, "chain:tip:side", &tip); err != nil {
		return nil
	}
	return &tip
}

// Get mainchain tip from Redis
func getMainBlockTipRedis(ctx context.Context, rdb *redis.Client) (*MainBlockData, error) {
	// Get tip height from stats
	height, err := getRedisUint64(ctx, rdb, "stats:network:height")
	if err != nil {
		return nil, err
	}

	// Fetch the block at that height
	return getMainBlockByHeight(ctx, rdb, height)
}

// Convert Redis tip to index.SideBlock-like structure
type SideBlockFromTip struct {
	TemplateId  types.Hash
	SideHeight  uint64
	MainHeight  uint64
	Timestamp   uint64
	Difficulty  types.Difficulty
	WindowDepth uint64
}

func convertTipToSideBlock(tip *SideChainTipRedis) *SideBlockFromTip {
	if tip == nil {
		return nil
	}
	templateId, _ := types.HashFromString(tip.TemplateId)
	return &SideBlockFromTip{
		TemplateId:  templateId,
		SideHeight:  tip.SideHeight,
		MainHeight:  tip.MainHeight,
		Timestamp:   tip.Timestamp,
		Difficulty:  types.DifficultyFrom64(tip.Difficulty),
		WindowDepth: tip.WindowDepth,
	}
}

// Found blocks functions
func getFoundBlocksRedis(ctx context.Context, rdb *redis.Client, limit int) []FoundBlockDataRedis {
	var blocks []FoundBlockDataRedis
	if err := getRedisJSON(ctx, rdb, "blocks:found:recent", &blocks); err != nil {
		return []FoundBlockDataRedis{}
	}

	// Return last N blocks
	if limit > 0 && len(blocks) > limit {
		return blocks[len(blocks)-limit:]
	}
	return blocks
}

// Share/block functions
func getRecentSharesRedis(ctx context.Context, rdb *redis.Client) []ShareDataRedis {
	var shares []ShareDataRedis
	if err := getRedisJSON(ctx, rdb, "shares:recent", &shares); err != nil {
		return []ShareDataRedis{}
	}
	return shares
}

// Mainchain block functions
func getMainBlockByHeight(ctx context.Context, rdb *redis.Client, height uint64) (*MainBlockData, error) {
	key := fmt.Sprintf("block:main:height:%d", height)
	var block MainBlockData
	if err := getRedisJSON(ctx, rdb, key, &block); err != nil {
		return nil, err
	}
	return &block, nil
}

func getMainBlockById(ctx context.Context, rdb *redis.Client, id string) (*MainBlockData, error) {
	key := fmt.Sprintf("block:main:id:%s", id)
	var block MainBlockData
	if err := getRedisJSON(ctx, rdb, key, &block); err != nil {
		return nil, err
	}
	return &block, nil
}

// MainBlockData matches daemon2's structure
type MainBlockData struct {
	Id         string `json:"id"`
	Height     uint64 `json:"height"`
	Timestamp  uint64 `json:"timestamp"`
	Difficulty uint64 `json:"difficulty"`
	Reward     uint64 `json:"reward"`
	Size       uint64 `json:"size"`
	Weight     uint64 `json:"weight"`
	TxCount    int    `json:"tx_count"`
}

// Pool statistics functions
func getPoolHashrate(ctx context.Context, rdb *redis.Client) (uint64, error) {
	return getRedisUint64(ctx, rdb, "stats:pool:hashrate")
}

func getPoolMinersCount(ctx context.Context, rdb *redis.Client) (uint64, error) {
	return getRedisUint64(ctx, rdb, "stats:pool:miners")
}

func getNetworkHeight(ctx context.Context, rdb *redis.Client) (uint64, error) {
	return getRedisUint64(ctx, rdb, "stats:network:height")
}

func getNetworkDifficulty(ctx context.Context, rdb *redis.Client) (uint64, error) {
	return getRedisUint64(ctx, rdb, "stats:network:difficulty")
}

func getTotalBlocksFound(ctx context.Context, rdb *redis.Client) (uint64, error) {
	return getRedisUint64(ctx, rdb, "stats:total:blocks")
}

func getEffortStats(ctx context.Context, rdb *redis.Client) (*EffortDataRedis, error) {
	var effort EffortDataRedis
	if err := getRedisJSON(ctx, rdb, "stats:effort", &effort); err != nil {
		return nil, err
	}
	return &effort, nil
}

func getPPLNSWindowStats(ctx context.Context, rdb *redis.Client) (*PPLNSWindowDataRedis, error) {
	var pplns PPLNSWindowDataRedis
	if err := getRedisJSON(ctx, rdb, "stats:pplns:window", &pplns); err != nil {
		return nil, err
	}
	return &pplns, nil
}

// ============================================================================
// Side Block Retrieval Functions
// ============================================================================

// Get side block by template ID
func getSideBlockByTemplateId(ctx context.Context, rdb *redis.Client, templateId string) (*ShareDataRedis, error) {
	key := fmt.Sprintf("sideblock:template:%s", templateId)
	var share ShareDataRedis
	if err := getRedisJSON(ctx, rdb, key, &share); err != nil {
		return nil, err
	}
	return &share, nil
}

// Get side block by main ID (for blocks that found mainchain blocks)
func getSideBlockByMainId(ctx context.Context, rdb *redis.Client, mainId string) (*ShareDataRedis, error) {
	// First get the template ID from the index
	key := fmt.Sprintf("sideblock:main:%s", mainId)
	templateId, err := rdb.Get(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	// Then fetch the actual share data
	return getSideBlockByTemplateId(ctx, rdb, templateId)
}

// Get side block at height (returns first one if multiple exist)
func getTipSideBlockByHeight(ctx context.Context, rdb *redis.Client, height uint64) (*ShareDataRedis, error) {
	// Get set of template IDs at this height
	key := fmt.Sprintf("sideblock:height:%d", height)
	templateIds, err := rdb.SMembers(ctx, key).Result()
	if err != nil || len(templateIds) == 0 {
		return nil, fmt.Errorf("no blocks at height %d", height)
	}
	// Return the first one (in production, might want to return the canonical one)
	return getSideBlockByTemplateId(ctx, rdb, templateIds[0])
}

// Get all side blocks at height
func getSideBlocksByHeight(ctx context.Context, rdb *redis.Client, height uint64) ([]*ShareDataRedis, error) {
	key := fmt.Sprintf("sideblock:height:%d", height)
	templateIds, err := rdb.SMembers(ctx, key).Result()
	if err != nil {
		return nil, err
	}

	blocks := make([]*ShareDataRedis, 0, len(templateIds))
	for _, templateId := range templateIds {
		if block, err := getSideBlockByTemplateId(ctx, rdb, templateId); err == nil {
			blocks = append(blocks, block)
		}
	}
	return blocks, nil
}

// Get side blocks in window (from height, going back 'window' blocks)
func getSideBlocksInWindow(ctx context.Context, rdb *redis.Client, from, window uint64) ([]*ShareDataRedis, error) {
	blocks := make([]*ShareDataRedis, 0, window)

	for i := uint64(0); i < window && from >= i; i++ {
		height := from - i
		heightBlocks, err := getSideBlocksByHeight(ctx, rdb, height)
		if err == nil {
			blocks = append(blocks, heightBlocks...)
		}
	}
	return blocks, nil
}

// Get side blocks by miner in window
func getSideBlocksByMinerInWindow(ctx context.Context, rdb *redis.Client, minerAddress string, from, window uint64) ([]*ShareDataRedis, error) {
	// Get miner's recent shares list
	key := fmt.Sprintf("miner:%s:shares", minerAddress)
	templateIds, err := rdb.LRange(ctx, key, 0, int64(window)-1).Result()
	if err != nil {
		return nil, err
	}

	blocks := make([]*ShareDataRedis, 0, len(templateIds))
	for _, templateId := range templateIds {
		if block, err := getSideBlockByTemplateId(ctx, rdb, templateId); err == nil {
			// Filter by height window
			if block.SideHeight <= from && block.SideHeight > from-window {
				blocks = append(blocks, block)
			}
		}
	}
	return blocks, nil
}

// Get side blocks that include a specific uncle
func getSideBlocksByUncleOf(ctx context.Context, rdb *redis.Client, uncleHash string) ([]*ShareDataRedis, error) {
	key := fmt.Sprintf("sideblock:uncle:%s", uncleHash)
	templateIds, err := rdb.SMembers(ctx, key).Result()
	if err != nil {
		return nil, err
	}

	blocks := make([]*ShareDataRedis, 0, len(templateIds))
	for _, templateId := range templateIds {
		if block, err := getSideBlockByTemplateId(ctx, rdb, templateId); err == nil {
			blocks = append(blocks, block)
		}
	}
	return blocks, nil
}

// ============================================================================
// Found Block Retrieval Functions
// ============================================================================

// Get found blocks with optional miner filter and limit
func getFoundBlocksWithFilter(ctx context.Context, rdb *redis.Client, minerAddress string, limit int) ([]*FoundBlockDataRedis, error) {
	var mainIds []string

	if minerAddress != "" {
		// Get blocks found by specific miner
		key := fmt.Sprintf("miner:%s:blocks:found", minerAddress)
		ids, err := rdb.LRange(ctx, key, 0, int64(limit)-1).Result()
		if err != nil {
			return nil, err
		}
		mainIds = ids
	} else {
		// Get recent found blocks from sorted set (reverse chronological)
		ids, err := rdb.ZRevRange(ctx, "blocks:found:all", 0, int64(limit)-1).Result()
		if err != nil {
			return nil, err
		}
		mainIds = ids
	}

	// Fetch full block data
	blocks := make([]*FoundBlockDataRedis, 0, len(mainIds))
	for _, mainId := range mainIds {
		key := fmt.Sprintf("block:found:%s", mainId)
		var block FoundBlockDataRedis
		if err := getRedisJSON(ctx, rdb, key, &block); err == nil {
			blocks = append(blocks, &block)
		}
	}
	return blocks, nil
}

// Get found block by side height
func getFoundBlockBySideHeight(ctx context.Context, rdb *redis.Client, sideHeight uint64) (*FoundBlockDataRedis, error) {
	key := fmt.Sprintf("block:found:sideheight:%d", sideHeight)
	mainId, err := rdb.Get(ctx, key).Result()
	if err != nil {
		return nil, err
	}

	blockKey := fmt.Sprintf("block:found:%s", mainId)
	var block FoundBlockDataRedis
	if err := getRedisJSON(ctx, rdb, blockKey, &block); err != nil {
		return nil, err
	}
	return &block, nil
}

// Get found block by main height
func getFoundBlockByMainHeight(ctx context.Context, rdb *redis.Client, mainHeight uint64) (*FoundBlockDataRedis, error) {
	key := fmt.Sprintf("block:found:mainheight:%d", mainHeight)
	mainId, err := rdb.Get(ctx, key).Result()
	if err != nil {
		return nil, err
	}

	blockKey := fmt.Sprintf("block:found:%s", mainId)
	var block FoundBlockDataRedis
	if err := getRedisJSON(ctx, rdb, blockKey, &block); err != nil {
		return nil, err
	}
	return &block, nil
}

// ============================================================================
// Share Query Functions
// ============================================================================

// Get shares with filters (limit, miner, onlyBlocks, inclusion)
func getSharesFiltered(ctx context.Context, rdb *redis.Client, limit int, minerAddress string, onlyBlocks bool) ([]*ShareDataRedis, error) {
	if minerAddress != "" {
		// Get shares from miner's list
		key := fmt.Sprintf("miner:%s:shares", minerAddress)
		templateIds, err := rdb.LRange(ctx, key, 0, int64(limit)-1).Result()
		if err != nil {
			return nil, err
		}

		shares := make([]*ShareDataRedis, 0, len(templateIds))
		for _, templateId := range templateIds {
			if share, err := getSideBlockByTemplateId(ctx, rdb, templateId); err == nil {
				// Filter by onlyBlocks if needed
				if onlyBlocks {
					// Check if this share found a block
					mainIdKey := fmt.Sprintf("sideblock:main:%s", share.MainId)
					if _, err := rdb.Get(ctx, mainIdKey).Result(); err == nil {
						shares = append(shares, share)
					}
				} else {
					shares = append(shares, share)
				}
			}
		}
		return shares, nil
	} else {
		// Get recent shares from global list
		var shares []*ShareDataRedis
		if err := getRedisJSON(ctx, rdb, "shares:recent", &shares); err != nil {
			return nil, err
		}

		if limit > 0 && len(shares) > limit {
			shares = shares[len(shares)-limit:]
		}
		return shares, nil
	}
}

// ============================================================================
// Payout Retrieval Functions
// ============================================================================

// Get payout data for a specific block
func getPayoutsBySideHeight(ctx context.Context, rdb *redis.Client, sideHeight uint64) (map[string]interface{}, error) {
	key := fmt.Sprintf("block:payouts:%d", sideHeight)
	var payouts map[string]interface{}
	if err := getRedisJSON(ctx, rdb, key, &payouts); err != nil {
		return nil, err
	}
	return payouts, nil
}

// Webhook functions
type MinerWebhookRedis struct {
	Type     string            `json:"type"`
	URL      string            `json:"url"`
	Settings map[string]string `json:"settings"`
}

func getMinerWebhooksRedis(ctx context.Context, rdb *redis.Client, minerAddr string) ([]MinerWebhookRedis, error) {
	key := fmt.Sprintf("miner:%s:webhooks", minerAddr)
	data, err := rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return []MinerWebhookRedis{}, nil // No webhooks
	}
	if err != nil {
		return nil, err
	}

	var webhooks []MinerWebhookRedis
	if err := json.Unmarshal([]byte(data), &webhooks); err != nil {
		return nil, err
	}
	return webhooks, nil
}

func setMinerWebhookRedis(ctx context.Context, rdb *redis.Client, minerAddr string, webhook MinerWebhookRedis) error {
	// Get existing webhooks
	webhooks, err := getMinerWebhooksRedis(ctx, rdb, minerAddr)
	if err != nil {
		return err
	}

	// Update or add webhook
	found := false
	for i := range webhooks {
		if webhooks[i].Type == webhook.Type {
			webhooks[i] = webhook
			found = true
			break
		}
	}
	if !found {
		webhooks = append(webhooks, webhook)
	}

	// Save back to Redis
	key := fmt.Sprintf("miner:%s:webhooks", minerAddr)
	data, err := json.Marshal(webhooks)
	if err != nil {
		return err
	}
	return rdb.Set(ctx, key, string(data), 0).Err()
}

func deleteMinerWebhookRedis(ctx context.Context, rdb *redis.Client, minerAddr string, webhookType string) error {
	// Get existing webhooks
	webhooks, err := getMinerWebhooksRedis(ctx, rdb, minerAddr)
	if err != nil {
		return err
	}

	// Remove webhook of specified type
	newWebhooks := make([]MinerWebhookRedis, 0)
	for _, wh := range webhooks {
		if wh.Type != webhookType {
			newWebhooks = append(newWebhooks, wh)
		}
	}

	// Save back to Redis
	key := fmt.Sprintf("miner:%s:webhooks", minerAddr)
	if len(newWebhooks) == 0 {
		// Delete key if no webhooks left
		return rdb.Del(ctx, key).Err()
	}

	data, err := json.Marshal(newWebhooks)
	if err != nil {
		return err
	}
	return rdb.Set(ctx, key, string(data), 0).Err()
}

func main() {
	torHost := os.Getenv("TOR_SERVICE_ADDRESS")
	moneroHost := flag.String("host", "127.0.0.1", "IP address of your Monero node")
	moneroRpcPort := flag.Uint("rpc-port", 18081, "monerod RPC API port number")
	dbString := flag.String("db", "", "")
	redisAddr := flag.String("redis", "localhost:6379", "Redis address (host:port)")
	p2poolApiHost := flag.String("api-host", "", "Host URL for p2pool go observer consensus")
	debugListen := flag.String("debug-listen", "", "Provide a bind address and port to expose a pprof HTTP API on it.")
	flag.Parse()

	client.SetDefaultClientSettings(fmt.Sprintf("http://%s:%d", *moneroHost, *moneroRpcPort))

	p2api := p2poolapi.NewP2PoolApi(*p2poolApiHost)

	if err := p2api.WaitSyncStart(); err != nil {
		utils.Panic(err)
	}
	consensus := p2api.Consensus()

	ctx := context.Background()

	// Initialize Redis client
	rdb := redis.NewClient(&redis.Options{
		Addr:         *redisAddr,
		Password:     "",
		DB:           0,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolSize:     10,
		MinIdleConns: 2,
	})

	// Test Redis connection
	if err := rdb.Ping(ctx).Err(); err != nil {
		utils.Logf("REDIS", "Redis connection failed: %s (will continue with PostgreSQL fallback)", err)
	} else {
		utils.Logf("REDIS", "Connected to Redis at %s", *redisAddr)
	}

	indexDb, err := index.OpenIndex(*dbString, consensus, p2api.DifficultyByHeight, p2api.SeedByHeight, p2api.ByTemplateId)
	if err != nil {
		utils.Panic(err)
	}
	defer indexDb.Close()
	defer rdb.Close()

	var lastPoolInfo atomic.Pointer[cmdutils.PoolInfoResult]

	fillMainCoinbaseOutputs := func(params url.Values, outputs index.MainCoinbaseOutputs) (result index.MainCoinbaseOutputs) {
		result = make(index.MainCoinbaseOutputs, 0, len(outputs))
		fillMiner := !params.Has("noMiner")
		for _, output := range outputs {
			if fillMiner {
				miner := indexDb.GetMiner(output.Miner)
				output.MinerAddress = miner.Address()
				output.MinerPayoutAddress = miner.OnlyPayoutAddress()
				output.MinerAlias = miner.Alias()
			}
			result = append(result, output)
		}
		return result
	}

	fillFoundBlockResult := func(fillMiner bool, foundBlock *index.FoundBlock) (result *index.FoundBlock) {
		if fillMiner {
			miner := indexDb.GetMiner(foundBlock.Miner)
			foundBlock.MinerAddress = miner.Address()
			foundBlock.MinerPayoutAddress = miner.OnlyPayoutAddress()
			foundBlock.MinerAlias = miner.Alias()
		}
		return foundBlock
	}

	fillSideBlockResult := func(fillUncles, fillMined, fillMiner bool, sideBlock *index.SideBlock) *index.SideBlock {
		if fillMiner {
			miner := indexDb.GetMiner(sideBlock.Miner)
			sideBlock.MinerAddress = miner.Address()
			sideBlock.MinerPayoutAddress = miner.OnlyPayoutAddress()
			sideBlock.MinerAlias = miner.Alias()
		}
		if fillMined {
			mainTipAtHeight := indexDb.GetMainBlockByHeight(sideBlock.MainHeight)
			if mainTipAtHeight != nil {
				sideBlock.MinedMainAtHeight = mainTipAtHeight.Id == sideBlock.MainId
				sideBlock.MainDifficulty = mainTipAtHeight.Difficulty
			} else {
				poolInfo := lastPoolInfo.Load()
				if (poolInfo.MainChain.Height + 1) == sideBlock.MainHeight {
					sideBlock.MainDifficulty = lastPoolInfo.Load().MainChain.NextDifficulty.Lo
				} else if poolInfo.MainChain.Height == sideBlock.MainHeight {
					sideBlock.MainDifficulty = lastPoolInfo.Load().MainChain.Difficulty.Lo
				}
			}
		}
		if fillUncles {
			index.QueryIterate(indexDb.GetSideBlocksByUncleOfId(sideBlock.TemplateId), func(_ int, u *index.SideBlock) (stop bool) {
				sideBlock.Uncles = append(sideBlock.Uncles, index.SideBlockUncleEntry{
					TemplateId: u.TemplateId,
					Miner:      u.Miner,
					SideHeight: u.SideHeight,
					Difficulty: u.Difficulty,
				})
				return false
			})
		}
		return sideBlock
	}

	getBlockWithUncles := func(id types.Hash) (block *sidechain.PoolBlock, uncles []*sidechain.PoolBlock) {
		block = p2api.ByTemplateId(id)
		if block == nil {
			return nil, nil
		}
		for _, uncleId := range block.Side.Uncles {
			if u := p2api.ByTemplateId(uncleId); u == nil {
				return nil, nil
			} else {
				uncles = append(uncles, u)
			}
		}
		return block, uncles
	}
	_ = getBlockWithUncles

	serveMux := mux.NewRouter()

	getPoolInfo := func() {

		oldPoolInfo := lastPoolInfo.Load()

		tip := indexDb.GetSideBlockTip()

		if oldPoolInfo != nil && oldPoolInfo.SideChain.LastBlock.TemplateId == tip.TemplateId {
			//no changes!

			//this race is ok
			oldPoolInfo.SideChain.SecondsSinceLastBlock = max(0, time.Now().Unix()-int64(tip.Timestamp))
			return
		}

		var bottom *index.SideBlock
		var bottomUncles []*index.SideBlock
		blockCount := 0
		uncleCount := 0

		miners := make(map[uint64]uint64)

		versions := make([]cmdutils.SideChainVersionEntry, 0)

		var pplnsWeight types.Difficulty

		if err := index.IterateSideBlocksInPPLNSWindowFast(indexDb, tip, indexDb.GetDifficultyByHeight, func(b *index.SideBlock, weight types.Difficulty) {
			miners[indexDb.GetMiner(b.Miner).Id()]++
			pplnsWeight = pplnsWeight.Add(weight)

			if i := slices.IndexFunc(versions, func(entry cmdutils.SideChainVersionEntry) bool {
				return entry.SoftwareId == b.SoftwareId && entry.SoftwareVersion == b.SoftwareVersion
			}); i != -1 {
				versions[i].Weight = versions[i].Weight.Add(weight)
				versions[i].Count++
			} else {
				versions = append(versions, cmdutils.SideChainVersionEntry{
					Weight:          weight,
					Count:           1,
					SoftwareId:      b.SoftwareId,
					SoftwareVersion: b.SoftwareVersion,
					SoftwareString:  fmt.Sprintf("%s %s", b.SoftwareId, b.SoftwareVersion.SoftwareAwareString(b.SoftwareId)),
				})
			}
		}, func(slot index.SideBlockWindowSlot) {
			blockCount++
			bottomUncles = slot.Uncles
			uncleCount += len(slot.Uncles)
			bottom = slot.Block
		}); err != nil {
			utils.Errorf("", "error scanning PPLNS window: %s", err)

			if oldPoolInfo != nil {
				// got error, just update last pool

				//this race is ok
				oldPoolInfo.SideChain.SecondsSinceLastBlock = max(0, time.Now().Unix()-int64(tip.Timestamp))
			}
			return
		}

		slices.SortFunc(versions, func(a, b cmdutils.SideChainVersionEntry) int {
			return a.Weight.Cmp(b.Weight) * -1
		})

		for i := range versions {
			versions[i].Share = float64(versions[i].Weight.Mul64(100).Lo) / float64(pplnsWeight.Lo)
		}

		type totalKnownResult struct {
			blocksFound uint64
			minersKnown uint64
		}

		totalKnown := cacheResult(CacheTotalKnownBlocksAndMiners, time.Minute, func() any {
			result := &totalKnownResult{}
			if err := indexDb.Query("SELECT (SELECT COUNT(*) FROM "+indexDb.GetView("found_main_blocks")+") as found, (SELECT COUNT(*) FROM miners) as miners;", func(row index.RowScanInterface) error {
				return row.Scan(&result.blocksFound, &result.minersKnown)
			}); err != nil {
				return nil
			}

			return result
		}).(*totalKnownResult)

		lastBlocksFound := index.QueryIterateToSlice(indexDb.GetFoundBlocks("", 201))

		mainTip, err := getMainBlockTipRedis(ctx, rdb)
		if err != nil {
			utils.Errorf("", "error getting main tip: %s", err)
			return
		}
		mainTipId, _ := types.HashFromString(mainTip.Id)
		mainTipCoinbaseId := types.ZeroHash // TODO: need to store coinbase ID in daemon2
		networkDifficulty := types.DifficultyFrom64(mainTip.Difficulty)
		minerData := p2api.MinerData()
		expectedBaseBlockReward := block.GetBlockReward(minerData.MedianWeight, minerData.MedianWeight, minerData.AlreadyGeneratedCoins, minerData.MajorVersion)
		minerDifficulty := minerData.Difficulty

		getBlockEffort := func(blockCumulativeDifficulty, previousBlockCumulativeDifficulty, networkDifficulty types.Difficulty) float64 {
			return float64(blockCumulativeDifficulty.Sub(previousBlockCumulativeDifficulty).Mul64(100).Lo) / float64(networkDifficulty.Lo)
		}

		var lastDiff types.Difficulty
		if len(lastBlocksFound) > 0 {
			lastDiff = lastBlocksFound[0].CumulativeDifficulty
		}

		tipCumDiff := tip.CumulativeDifficulty
		if lastDiff.Cmp(tipCumDiff) > 0 {
			tipCumDiff = lastDiff
		}

		currentEffort := getBlockEffort(tipCumDiff, lastDiff, networkDifficulty)

		if currentEffort <= 0 || lastDiff.Cmp64(0) == 0 {
			currentEffort = 0
		}

		var blockEfforts []cmdutils.PoolInfoResultSideChainEffortLastEntry
		for i, b := range lastBlocksFound {
			if i < (len(lastBlocksFound)-1) && b.CumulativeDifficulty.Cmp64(0) > 0 && lastBlocksFound[i+1].CumulativeDifficulty.Cmp64(0) > 0 {
				blockEfforts = append(blockEfforts, cmdutils.PoolInfoResultSideChainEffortLastEntry{
					Id:     b.MainBlock.Id,
					Effort: getBlockEffort(b.CumulativeDifficulty, lastBlocksFound[i+1].CumulativeDifficulty, types.DifficultyFrom64(b.MainBlock.Difficulty)),
				})
			}
		}

		averageEffort := func(limit int) (result float64) {
			maxI := min(limit, len(blockEfforts))
			for i, e := range blockEfforts {
				result += e.Effort
				if i+1 == maxI {
					break
				}
			}
			return result / float64(maxI)
		}

		result := &cmdutils.PoolInfoResult{
			SideChain: cmdutils.PoolInfoResultSideChain{
				Consensus:             consensus,
				LastBlock:             fillSideBlockResult(true, false, true, tip),
				SecondsSinceLastBlock: max(0, time.Now().Unix()-int64(tip.Timestamp)),
				LastFound:             nil, //to be filled later
				Effort: cmdutils.PoolInfoResultSideChainEffort{
					Current:    currentEffort,
					Average10:  averageEffort(10),
					Average50:  averageEffort(50),
					Average200: averageEffort(200),
					Last:       blockEfforts,
				},
				Window: cmdutils.PoolInfoResultSideChainWindow{
					Miners:   len(miners),
					Blocks:   blockCount,
					Uncles:   uncleCount,
					Weight:   pplnsWeight,
					Versions: versions,

					Top:    tip.TemplateId,
					Bottom: bottom.TemplateId,
					BottomUncles: func() (result []types.Hash) {
						for _, u := range bottomUncles {
							result = append(result, u.TemplateId)
						}
						return result
					}(),
				},

				Found:  totalKnown.blocksFound,
				Miners: totalKnown.minersKnown,

				Id:                   tip.TemplateId,
				Height:               tip.SideHeight,
				Version:              sidechain.P2PoolShareVersion(consensus, tip.Timestamp),
				Difficulty:           types.DifficultyFrom64(tip.Difficulty),
				CumulativeDifficulty: tip.CumulativeDifficulty,
				Timestamp:            tip.Timestamp,
				WindowSize:           blockCount,
				MaxWindowSize:        int(consensus.ChainWindowSize),
				BlockTime:            int(consensus.TargetBlockTime),
				UnclePenalty:         int(consensus.UnclePenalty),
			},
			MainChain: cmdutils.PoolInfoResultMainChain{
				Consensus: cmdutils.PoolInfoResultMainChainConsensus{
					BlockTime:                monero.BlockTime,
					TransactionUnlockTime:    monero.TransactionUnlockTime,
					MinerRewardUnlockTime:    monero.MinerRewardUnlockTime,
					HardForkSupportedVersion: monero.HardForkSupportedVersion,
					HardForks:                monero.NetworkHardFork(consensus.NetworkType.MustAddressNetwork()),
				},
				Id:             mainTipId,
				CoinbaseId:     mainTipCoinbaseId,
				Height:         mainTip.Height,
				Difficulty:     networkDifficulty,
				Reward:         mainTip.Reward,
				BaseReward:     expectedBaseBlockReward,
				NextDifficulty: minerDifficulty,

				BlockTime: monero.BlockTime,
			},
			Versions: struct {
				P2Pool cmdutils.VersionInfo `json:"p2pool"`
				Monero cmdutils.VersionInfo `json:"monero"`
			}{P2Pool: getP2PoolVersion(), Monero: getMoneroVersion()},
		}

		if len(lastBlocksFound) > 0 {
			result.SideChain.LastFound = fillFoundBlockResult(false, lastBlocksFound[0])
		}

		lastPoolInfo.Store(result)
	}

	getPoolInfo()

	go func() {
		for range time.Tick(time.Second * 2) {
			getPoolInfo()
		}
	}()

	serveMux.HandleFunc("/api/pool_info", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_ = httputils.EncodeJson(request, writer, lastPoolInfo.Load())
	})

	serveMux.HandleFunc("/api/miner_info/{miner:[^ ]+}", func(writer http.ResponseWriter, request *http.Request) {
		minerId := mux.Vars(request)["miner"]
		params := request.URL.Query()

		var miner *index.Miner
		if len(minerId) > 10 && (minerId[0] == '4' || minerId[0] == '8') {
			miner = indexDb.GetMinerByStringAddress(minerId)
		}

		if miner == nil {
			miner = indexDb.GetMinerByAlias(minerId)
		}

		if miner == nil {
			if i, err := strconv.Atoi(minerId); err == nil {
				miner = indexDb.GetMiner(uint64(i))
			}
		}

		if miner == nil {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusNotFound)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "not_found",
			})
			_, _ = writer.Write(buf)
			return
		}

		var lastShareHeight uint64
		var lastShareTimestamp uint64

		var foundBlocksData [index.InclusionCount]cmdutils.MinerInfoBlockData
		if !params.Has("noShares") {
			if params.Has("shareEstimates") {
				// estimate counts
				_ = indexDb.Query(fmt.Sprintf("SELECT count_estimate('SELECT 1 FROM side_blocks WHERE side_blocks.miner = %d AND uncle_of IS NULL AND inclusion = %d;') AS shares_estimate, count_estimate('SELECT 1 FROM side_blocks WHERE side_blocks.miner = %d AND uncle_of IS NOT NULL AND inclusion = %d;') uncles_estimate, side_height AS last_height, timestamp AS last_timestamp FROM side_blocks WHERE side_blocks.miner = %d AND inclusion = %d ORDER BY side_height DESC LIMIT 1;", miner.Id(), index.InclusionInVerifiedChain, miner.Id(), index.InclusionInVerifiedChain, miner.Id(), index.InclusionInVerifiedChain), func(row index.RowScanInterface) error {
					var shareEstimate, uncleEstimate, lastHeight, lastTimestamp uint64
					if err := row.Scan(&shareEstimate, &uncleEstimate, &lastHeight, &lastTimestamp); err != nil {
						return err
					}
					foundBlocksData[index.InclusionInVerifiedChain].ShareCount = shareEstimate
					foundBlocksData[index.InclusionInVerifiedChain].UncleCount = uncleEstimate
					foundBlocksData[index.InclusionInVerifiedChain].LastShareHeight = lastHeight
					lastShareHeight = lastHeight
					lastShareTimestamp = lastTimestamp
					return nil
				})
			} else {
				_ = indexDb.Query("SELECT COUNT(*) FILTER (WHERE uncle_of IS NULL) AS share_count, COUNT(*) FILTER (WHERE uncle_of IS NOT NULL) AS uncle_count, coalesce(MAX(side_height), 0) AS last_height, inclusion FROM side_blocks WHERE side_blocks.miner = $1 GROUP BY side_blocks.inclusion ORDER BY inclusion ASC;", func(row index.RowScanInterface) error {
					var d cmdutils.MinerInfoBlockData
					var inclusion index.BlockInclusion
					if err := row.Scan(&d.ShareCount, &d.UncleCount, &d.LastShareHeight, &inclusion); err != nil {
						return err
					}
					if inclusion < index.InclusionCount {
						foundBlocksData[inclusion] = d
					}
					return nil
				}, miner.Id())
			}
		}

		if lastShareHeight == 0 && foundBlocksData[index.InclusionInVerifiedChain].ShareCount > 0 && foundBlocksData[index.InclusionInVerifiedChain].LastShareHeight > lastShareHeight {
			lastShareHeight = foundBlocksData[index.InclusionInVerifiedChain].LastShareHeight
		}

		if lastShareTimestamp == 0 && lastShareHeight > 0 {
			lastShareTimestamp = indexDb.GetTipSideBlockByHeight(lastShareHeight).Timestamp
		}

		writer.Header().Set("Content-Type", "application/json; charset=utf-8")

		if lastShareTimestamp < uint64(time.Now().Unix()-3600) {
			writer.Header().Set("cache-control", "public; max-age=600")
		} else {
			writer.Header().Set("cache-control", "public; max-age=60")
		}

		writer.WriteHeader(http.StatusOK)
		_ = httputils.EncodeJson(request, writer, cmdutils.MinerInfoResult{
			Id:                 miner.Id(),
			Address:            miner.Address(),
			PayoutAddress:      miner.OnlyPayoutAddress(),
			Alias:              miner.Alias(),
			Shares:             foundBlocksData,
			LastShareHeight:    lastShareHeight,
			LastShareTimestamp: lastShareTimestamp,
		})
	})

	serveMux.HandleFunc("/api/global_indices_lookup", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != "POST" {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusMethodNotAllowed)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "not_allowed",
			})
			_, _ = writer.Write(buf)
			return
		}

		buf, err := io.ReadAll(request.Body)
		if err != nil {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusBadRequest)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "bad_request",
			})
			_, _ = writer.Write(buf)
			return
		}
		var indices []uint64
		if err = utils.UnmarshalJSON(buf, &indices); err != nil || len(indices) == 0 {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusBadRequest)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "bad_request",
			})
			_, _ = writer.Write(buf)
			return
		}

		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_ = httputils.EncodeJson(request, writer, indexDb.QueryGlobalOutputIndices(indices))
	})

	serveMux.HandleFunc("/api/sweeps_by_spending_global_output_indices", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != "POST" {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusMethodNotAllowed)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "not_allowed",
			})
			_, _ = writer.Write(buf)
			return
		}

		buf, err := io.ReadAll(request.Body)
		if err != nil {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusBadRequest)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "bad_request",
			})
			_, _ = writer.Write(buf)
			return
		}
		var indices []uint64
		if err = utils.UnmarshalJSON(buf, &indices); err != nil || len(indices) == 0 {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusBadRequest)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "bad_request",
			})
			_, _ = writer.Write(buf)
			return
		}

		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_ = httputils.EncodeJson(request, writer, indexDb.GetMainLikelySweepTransactionBySpendingGlobalOutputIndices(indices...))
	})

	serveMux.HandleFunc("/api/transaction_lookup/{txid:[0-9a-f]+}", func(writer http.ResponseWriter, request *http.Request) {
		txId, err := types.HashFromString(mux.Vars(request)["txid"])
		if err != nil {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusNotFound)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "not_found",
			})
			_, _ = writer.Write(buf)
			return
		}

		var otherLookupHostFunc func(ctx context.Context, indices []uint64) []*index.MatchedOutput

		if os.Getenv("TRANSACTION_LOOKUP_OTHER") != "" {
			otherLookupHostFunc = func(ctx context.Context, indices []uint64) (result []*index.MatchedOutput) {
				data, _ := utils.MarshalJSON(indices)

				result = make([]*index.MatchedOutput, len(indices))

				for _, host := range strings.Split(os.Getenv("TRANSACTION_LOOKUP_OTHER"), ",") {
					host = strings.TrimSpace(host)
					if host == "" {
						continue
					}
					uri, _ := url.Parse(host + "/api/global_indices_lookup")
					if response, err := http.DefaultClient.Do(&http.Request{
						Method: "POST",
						URL:    uri,
						Body:   io.NopCloser(bytes.NewReader(data)),
					}); err == nil {
						func() {
							defer response.Body.Close()
							if response.StatusCode == http.StatusOK {
								if data, err := io.ReadAll(response.Body); err == nil {
									r := make([]*index.MatchedOutput, 0, len(indices))
									if utils.UnmarshalJSON(data, &r) == nil && len(r) == len(indices) {
										for i := range r {
											if result[i] == nil {
												result[i] = r[i]
											}
										}
									}
								}
							}
						}()
					}
				}
				return result
			}
		}

		results := cmdutils.LookupTransactions(otherLookupHostFunc, indexDb, request.Context(), 0, txId)

		if len(results) == 0 {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusNotFound)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "not_found",
			})
			_, _ = writer.Write(buf)
			return
		}

		indicesToLookup := make([]uint64, 0, len(results[0]))
		for _, i := range results[0] {
			indicesToLookup = append(indicesToLookup, i.Input.KeyOffsets...)
		}

		if outs, err := client.GetDefaultClient().GetOuts(indicesToLookup...); err != nil {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusBadRequest)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "bad_request",
			})
			_, _ = writer.Write(buf)
			return
		} else {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusOK)
			for i, out := range outs {
				if mb := indexDb.GetMainBlockByHeight(out.Height); mb != nil {
					outs[i].Timestamp = mb.Timestamp
				}
			}
			_ = httputils.EncodeJson(request, writer, cmdutils.TransactionLookupResult{
				Id:     txId,
				Inputs: results[0],
				Outs:   outs,
				Match:  results[0].Match(),
			})
		}

	})

	serveMux.HandleFunc("/api/miner_webhooks/{miner:[48][123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz]+}", func(writer http.ResponseWriter, request *http.Request) {
		minerId := mux.Vars(request)["miner"]
		miner := indexDb.GetMinerByStringAddress(minerId)

		if miner == nil {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusNotFound)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "miner_not_found",
			})
			_, _ = writer.Write(buf)
			return
		}

		wh, _ := indexDb.GetMinerWebHooks(miner.Id())
		defer wh.Close()
		_ = httputils.StreamJsonIterator(request, writer, func() (int, *index.MinerWebHook) {
			i, w := wh.Next()
			if w == nil {
				return 0, nil
			}

			newHook := &index.MinerWebHook{
				Miner: w.Miner,
				Type:  w.Type,
			}
			// Sanitize private data by hashing
			newHook.Url = types.Hash(sha256.Sum256([]byte(w.Url))).String()
			settings := make(map[string]string)
			for k, v := range w.Settings {
				if strings.HasPrefix(k, "send_") {
					settings[k] = v
				}
			}
			newHook.Settings = settings
			return i, newHook
		})
	})

	serveMux.HandleFunc("/api/miner_signed_action/{miner:[48][123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz]+}", func(writer http.ResponseWriter, request *http.Request) {
		minerId := mux.Vars(request)["miner"]
		miner := indexDb.GetMinerByStringAddress(minerId)

		if miner == nil {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusNotFound)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "miner_not_found",
			})
			_, _ = writer.Write(buf)
			return
		}

		params := request.URL.Query()

		sig := strings.TrimSpace(params.Get("signature"))

		var signedAction *cmdutils.SignedAction
		if err := utils.UnmarshalJSON([]byte(strings.TrimSpace(params.Get("message"))), &signedAction); err != nil {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusNotFound)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: err.Error(),
			})
			_, _ = writer.Write(buf)
			return
		} else if signedAction == nil {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusNotFound)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "invalid_message",
			})
			_, _ = writer.Write(buf)
			return
		}

		result := signedAction.VerifyFallbackToZero(os.Getenv("NET_SERVICE_ADDRESS"), miner.PayoutAddress(), sig)
		if result == address.ResultFail {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusUnauthorized)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "signature_verify_fail",
			})
			_, _ = writer.Write(buf)
			return
		} else if result == address.ResultFailZeroSpend || result == address.ResultFailZeroView {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusUnauthorized)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "signature_verify_fail_zero_private_key",
			})
			_, _ = writer.Write(buf)
			return
		} else if result != address.ResultSuccessSpend && result != address.ResultSuccessView {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusUnauthorized)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "signature_verify_fail_unknown",
			})
			_, _ = writer.Write(buf)
			return
		}

		switch signedAction.Action {
		case "set_miner_alias":
			if result == address.ResultSuccessView {
				writer.Header().Set("Content-Type", "application/json; charset=utf-8")
				writer.WriteHeader(http.StatusUnauthorized)
				buf, _ := utils.MarshalJSON(struct {
					Error string `json:"error"`
				}{
					Error: "signature_verify_view_signature",
				})
				_, _ = writer.Write(buf)
				return
			}

			if newAlias, ok := signedAction.Get("alias"); ok {
				if len(newAlias) > 20 || len(newAlias) < 3 || !func() bool {
					for _, c := range newAlias {
						if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && c != '_' && c != '-' && c != '.' {
							return false
						}
					}

					return true
				}() || !unicode.IsLetter(rune(newAlias[0])) {
					writer.Header().Set("Content-Type", "application/json; charset=utf-8")
					writer.WriteHeader(http.StatusBadRequest)
					buf, _ := utils.MarshalJSON(struct {
						Error string `json:"error"`
					}{
						Error: "invalid_alias",
					})
					_, _ = writer.Write(buf)
					return
				}

				// Use Redis for alias storage
				if err := setMinerAlias(ctx, rdb, minerId, newAlias); err != nil {
					writer.Header().Set("Content-Type", "application/json; charset=utf-8")
					writer.WriteHeader(http.StatusBadRequest)
					buf, _ := utils.MarshalJSON(struct {
						Error string `json:"error"`
					}{
						Error: "duplicate_message",
					})
					_, _ = writer.Write(buf)
					return
				} else {
					writer.Header().Set("Content-Type", "application/json; charset=utf-8")
					writer.WriteHeader(http.StatusOK)
					return
				}
			} else {
				writer.Header().Set("Content-Type", "application/json; charset=utf-8")
				writer.WriteHeader(http.StatusBadRequest)
				buf, _ := utils.MarshalJSON(struct {
					Error string `json:"error"`
				}{
					Error: "invalid_alias",
				})
				_, _ = writer.Write(buf)
				return
			}
		case "unset_miner_alias":
			if result == address.ResultSuccessView {
				writer.Header().Set("Content-Type", "application/json; charset=utf-8")
				writer.WriteHeader(http.StatusUnauthorized)
				buf, _ := utils.MarshalJSON(struct {
					Error string `json:"error"`
				}{
					Error: "signature_verify_view_signature",
				})
				_, _ = writer.Write(buf)
				return
			}

			// Use Redis to delete alias
			if err := setMinerAlias(ctx, rdb, minerId, ""); err != nil {
				writer.Header().Set("Content-Type", "application/json; charset=utf-8")
				writer.WriteHeader(http.StatusBadRequest)
				buf, _ := utils.MarshalJSON(struct {
					Error string `json:"error"`
				}{
					Error: "duplicate_message",
				})
				_, _ = writer.Write(buf)
				return
			} else {
				writer.Header().Set("Content-Type", "application/json; charset=utf-8")
				writer.WriteHeader(http.StatusOK)
				return
			}
		case "add_webhook":
			whType, _ := signedAction.Get("type")
			whUrl, _ := signedAction.Get("url")
			wh := &index.MinerWebHook{
				Miner:    miner.Id(),
				Type:     index.WebHookType(whType),
				Url:      whUrl,
				Settings: make(map[string]string),
			}
			for _, e := range signedAction.Data {
				if strings.HasPrefix(e.Key, "send_") {
					wh.Settings[e.Key] = e.Value
				}
			}

			err := wh.Verify()
			if err != nil {
				writer.Header().Set("Content-Type", "application/json; charset=utf-8")
				writer.WriteHeader(http.StatusBadRequest)
				buf, _ := utils.MarshalJSON(struct {
					Error string `json:"error"`
				}{
					Error: err.Error(),
				})
				_, _ = writer.Write(buf)
				return
			}

			// Store webhook in Redis
			redisWebhook := MinerWebhookRedis{
				Type:     whType,
				URL:      whUrl,
				Settings: wh.Settings,
			}
			err = setMinerWebhookRedis(ctx, rdb, minerId, redisWebhook)
			if err != nil {
				writer.Header().Set("Content-Type", "application/json; charset=utf-8")
				writer.WriteHeader(http.StatusBadRequest)
				buf, _ := utils.MarshalJSON(struct {
					Error string `json:"error"`
				}{
					Error: err.Error(),
				})
				_, _ = writer.Write(buf)
				return
			}
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusOK)
			return
		case "remove_webhook":
			urlHash, ok := signedAction.Get("url_hash")
			urlType, ok2 := signedAction.Get("type")
			if !ok || !ok2 || urlHash == "" {
				writer.Header().Set("Content-Type", "application/json; charset=utf-8")
				writer.WriteHeader(http.StatusBadRequest)
				buf, _ := utils.MarshalJSON(struct {
					Error string `json:"error"`
				}{
					Error: "invalid_url",
				})
				_, _ = writer.Write(buf)
				return
			}

			// Get webhooks from Redis
			webhooks, err := getMinerWebhooksRedis(ctx, rdb, minerId)
			if err != nil {
				writer.Header().Set("Content-Type", "application/json; charset=utf-8")
				writer.WriteHeader(http.StatusBadRequest)
				buf, _ := utils.MarshalJSON(struct {
					Error string `json:"error"`
				}{
					Error: "internal_error",
				})
				_, _ = writer.Write(buf)
				return
			}

			// Find and delete matching webhook
			for _, wh := range webhooks {
				if wh.Type == urlType && types.Hash(sha256.Sum256([]byte(wh.URL))).String() == urlHash {
					_ = deleteMinerWebhookRedis(ctx, rdb, minerId, wh.Type)
				}
			}
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusOK)
			return
		default:
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusBadRequest)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "invalid_action",
			})
			_, _ = writer.Write(buf)
			return
		}
	})

	serveMux.HandleFunc("/api/side_blocks_in_window", func(writer http.ResponseWriter, request *http.Request) {
		params := request.URL.Query()

		tip := getSideBlockTipRedis(ctx, rdb)
		if tip == nil {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		window := tip.WindowDepth
		if params.Has("window") {
			if i, err := strconv.Atoi(params.Get("window")); err == nil {
				if i <= int(consensus.ChainWindowSize*4*7) {
					window = uint64(i)
				}
			}
		}

		var from uint64
		if params.Has("from") {
			if i, err := strconv.Atoi(params.Get("from")); err == nil {
				if i >= 0 {
					from = uint64(i)
				}
			}
		}

		if from == 0 {
			from = tip.SideHeight
		}

		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusOK)

		fillUncles := !params.Has("noUncles")
		fillMined := !params.Has("noMainStatus")
		fillMiner := !params.Has("noMiner")

		sideBlocks := indexDb.GetSideBlocksInWindow(from, window)
		defer sideBlocks.Close()
		_ = httputils.StreamJsonIterator(request, writer, func() (int, *index.SideBlock) {
			i, v := sideBlocks.Next()
			if v != nil {
				v = fillSideBlockResult(fillUncles, fillMined, fillMiner, v)
			}
			return i, v
		})
	})

	serveMux.HandleFunc("/api/side_blocks_in_window/{miner:[0-9]+|[48][123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz]+$}", func(writer http.ResponseWriter, request *http.Request) {
		minerId := mux.Vars(request)["miner"]
		var miner *index.Miner
		if len(minerId) > 10 && (minerId[0] == '4' || minerId[0] == '8') {
			miner = indexDb.GetMinerByStringAddress(minerId)
		}

		if miner == nil {
			if i, err := strconv.Atoi(minerId); err == nil {
				miner = indexDb.GetMiner(uint64(i))
			}
		}

		if miner == nil {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusNotFound)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "not_found",
			})
			_, _ = writer.Write(buf)
			return
		}

		params := request.URL.Query()

		tip := getSideBlockTipRedis(ctx, rdb)
		if tip == nil {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		window := tip.WindowDepth
		if params.Has("window") {
			if i, err := strconv.Atoi(params.Get("window")); err == nil {
				if i <= int(consensus.ChainWindowSize*4*7) {
					window = uint64(i)
				}
			}
		}

		var from uint64
		if params.Has("from") {
			if i, err := strconv.Atoi(params.Get("from")); err == nil {
				if i >= 0 {
					from = uint64(i)
				}
			}
		}

		if from == 0 {
			from = tip.SideHeight
		}

		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusOK)

		fillUncles := !params.Has("noUncles")
		fillMined := !params.Has("noMainStatus")
		fillMiner := !params.Has("noMiner")

		sideBlocks := indexDb.GetSideBlocksByMinerIdInWindow(miner.Id(), from, window)
		defer sideBlocks.Close()
		_ = httputils.StreamJsonIterator(request, writer, func() (int, *index.SideBlock) {
			i, v := sideBlocks.Next()
			if v != nil {
				v = fillSideBlockResult(fillUncles, fillMined, fillMiner, v)
			}
			return i, v
		})
	})

	serveMux.HandleFunc("/api/sweeps", func(writer http.ResponseWriter, request *http.Request) {

		params := request.URL.Query()

		var limit uint64 = 10

		if params.Has("limit") {
			if i, err := strconv.Atoi(params.Get("limit")); err == nil {
				limit = uint64(i)
			}
		}

		if limit > 200 {
			limit = 200
		} else if limit <= 0 {
			limit = 10
		}

		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusOK)

		result, err := indexDb.GetMainLikelySweepTransactions(limit)
		if err != nil {
			panic(err)
		}
		defer result.Close()
		_ = httputils.StreamJsonIterator(request, writer, result.Next)
	})

	serveMux.HandleFunc("/api/sweeps/{miner:[0-9]+|[48][123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz]+$}", func(writer http.ResponseWriter, request *http.Request) {
		minerId := mux.Vars(request)["miner"]
		var miner *index.Miner
		if len(minerId) > 10 && (minerId[0] == '4' || minerId[0] == '8') {
			miner = indexDb.GetMinerByStringAddress(minerId)
		}

		if miner == nil {
			if i, err := strconv.Atoi(minerId); err == nil {
				miner = indexDb.GetMiner(uint64(i))
			}
		}

		if miner == nil {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusNotFound)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "not_found",
			})
			_, _ = writer.Write(buf)
			return
		}

		params := request.URL.Query()

		var limit uint64 = 10

		if params.Has("limit") {
			if i, err := strconv.Atoi(params.Get("limit")); err == nil {
				limit = uint64(i)
			}
		}

		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusOK)

		result, err := indexDb.GetMainLikelySweepTransactionsByAddress(miner.Address(), limit)
		if err != nil {
			panic(err)
		}
		defer result.Close()
		_ = httputils.StreamJsonIterator(request, writer, result.Next)
	})

	serveMux.HandleFunc("/api/payouts/{miner:[0-9]+|[48][123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz]+$}", func(writer http.ResponseWriter, request *http.Request) {
		minerId := mux.Vars(request)["miner"]
		var miner *index.Miner
		if len(minerId) > 10 && (minerId[0] == '4' || minerId[0] == '8') {
			miner = indexDb.GetMinerByStringAddress(minerId)
		}

		if miner == nil {
			if i, err := strconv.Atoi(minerId); err == nil {
				miner = indexDb.GetMiner(uint64(i))
			}
		}

		if miner == nil {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusNotFound)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "not_found",
			})
			_, _ = writer.Write(buf)
			return
		}

		params := request.URL.Query()

		var limit, timestamp, height uint64 = 10, 0, 0

		if params.Has("search_limit") {
			if i, err := strconv.Atoi(params.Get("search_limit")); err == nil {
				limit = uint64(i)
			}
		}
		if params.Has("limit") {
			if i, err := strconv.Atoi(params.Get("limit")); err == nil {
				limit = uint64(i)
			}
		}
		if params.Has("from_timestamp") {
			if i, err := strconv.Atoi(params.Get("from_timestamp")); err == nil {
				timestamp = uint64(i)
			}
		}
		if params.Has("from_height") {
			if i, err := strconv.Atoi(params.Get("from_height")); err == nil {
				height = uint64(i)
			}
		}

		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusOK)

		var result index.QueryIterator[index.Payout]
		var err error

		if timestamp > 0 {
			result, err = indexDb.GetPayoutsByMinerIdFromTimestamp(miner.Id(), timestamp)
		} else if height > 0 {
			result, err = indexDb.GetPayoutsByMinerIdFromHeight(miner.Id(), height)
		} else {
			result, err = indexDb.GetPayoutsByMinerId(miner.Id(), limit)
		}

		if err != nil {
			panic(err)
		}
		defer result.Close()
		_ = httputils.StreamJsonIterator(request, writer, result.Next)
	})

	serveMux.HandleFunc("/api/redirect/block/{main_height:[0-9]+|.?[0-9A-Za-z]+$}", func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, fmt.Sprintf("%s/explorer/block/%d", cmdutils.GetSiteUrl(cmdutils.SiteKeyP2PoolIo, request.Host == torHost), cmdutils.DecodeBinaryNumber(mux.Vars(request)["main_height"])), http.StatusFound)
	})
	serveMux.HandleFunc("/api/redirect/transaction/{tx_id:.?[0-9A-Za-z]+}", func(writer http.ResponseWriter, request *http.Request) {
		txId := cmdutils.DecodeHexBinaryNumber(mux.Vars(request)["tx_id"])
		if len(txId) != types.HashSize*2 {
			writer.WriteHeader(http.StatusNotFound)
			return
		}

		http.Redirect(writer, request, fmt.Sprintf("%s/explorer/tx/%s", cmdutils.GetSiteUrl(cmdutils.SiteKeyP2PoolIo, request.Host == torHost), txId), http.StatusFound)
	})
	serveMux.HandleFunc("/api/redirect/coinbase/{coinbase:[0-9]+|.?[0-9A-Za-z]+$}", func(writer http.ResponseWriter, request *http.Request) {
		foundTarget := index.QueryFirstResult(indexDb.GetFoundBlocks("WHERE side_height = $1 AND reward > 0", 1, cmdutils.DecodeBinaryNumber(mux.Vars(request)["coinbase"])))
		if foundTarget == nil {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusNotFound)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "not_found",
			})
			_, _ = writer.Write(buf)
			return
		}

		http.Redirect(writer, request, fmt.Sprintf("%s/explorer/tx/%s", cmdutils.GetSiteUrl(cmdutils.SiteKeyP2PoolIo, request.Host == torHost), foundTarget.MainBlock.Id.String()), http.StatusFound)
	})
	serveMux.HandleFunc("/api/redirect/share/{height:[0-9]+|.?[0-9A-Za-z]+$}", func(writer http.ResponseWriter, request *http.Request) {
		c := cmdutils.DecodeBinaryNumber(mux.Vars(request)["height"])

		blockHeight := c >> 16
		blockIdStart := c & 0xFFFF

		blockStart := []byte{byte((blockIdStart >> 8) & 0xFF), byte(blockIdStart & 0xFF)}
		sideBlocks, err := indexDb.GetSideBlocksByQuery("WHERE side_height = $1;", blockHeight)
		if err != nil {
			panic(err)
		}

		var b *index.SideBlock
		index.QueryIterate(sideBlocks, func(_ int, s *index.SideBlock) (stop bool) {
			if bytes.Compare(s.MainId[:2], blockStart) == 0 {
				b = s
				return true
			}

			if bytes.Compare(s.TemplateId[:2], blockStart) == 0 {
				b = s
				return true
			}
			return false
		})

		if b == nil {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusNotFound)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "not_found",
			})
			_, _ = writer.Write(buf)
			return
		}

		http.Redirect(writer, request, fmt.Sprintf("/share/%s", b.TemplateId.String()), http.StatusFound)
	})

	serveMux.HandleFunc("/api/redirect/prove/{height_index:[0-9]+|.[0-9A-Za-z]+}", func(writer http.ResponseWriter, request *http.Request) {
		i := cmdutils.DecodeBinaryNumber(mux.Vars(request)["height_index"])
		n := uint64(math.Ceil(math.Log2(float64(consensus.ChainWindowSize * 4))))

		height := i >> n
		outputIndex := i & ((1 << n) - 1)

		b := index.QueryFirstResult(indexDb.GetFoundBlocks("WHERE side_height = $1 AND reward > 0", 1, height))
		var tx *index.MainCoinbaseOutput
		if b != nil {
			tx = indexDb.GetMainCoinbaseOutputByIndex(b.MainBlock.CoinbaseId, outputIndex)
		}

		if b == nil || tx == nil {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusNotFound)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "not_found",
			})
			_, _ = writer.Write(buf)
			return
		}

		http.Redirect(writer, request, fmt.Sprintf("/proof/%s/%d", b.MainBlock.Id.String(), tx.Index), http.StatusFound)
	})

	serveMux.HandleFunc("/api/redirect/prove/{height:[0-9]+|.[0-9A-Za-z]+}/{miner:[0-9]+|.?[0-9A-Za-z]+}", func(writer http.ResponseWriter, request *http.Request) {
		b := index.QueryFirstResult(indexDb.GetFoundBlocks("WHERE side_height = $1 AND reward > 0", 1, cmdutils.DecodeBinaryNumber(mux.Vars(request)["height"])))
		miner := indexDb.GetMiner(cmdutils.DecodeBinaryNumber(mux.Vars(request)["miner"]))
		if b == nil || miner == nil {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusNotFound)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "not_found",
			})
			_, _ = writer.Write(buf)
			return
		}

		tx := indexDb.GetMainCoinbaseOutputByMinerId(b.MainBlock.Id, miner.Id())

		if tx == nil {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusNotFound)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "not_found",
			})
			_, _ = writer.Write(buf)
			return
		}

		http.Redirect(writer, request, fmt.Sprintf("/proof/%s/%d", b.MainBlock.Id.String(), tx.Index), http.StatusFound)
	})

	serveMux.HandleFunc("/api/redirect/miner/{miner:[0-9]+|.?[0-9A-Za-z]+}", func(writer http.ResponseWriter, request *http.Request) {
		miner := indexDb.GetMiner(cmdutils.DecodeBinaryNumber(mux.Vars(request)["miner"]))
		if miner == nil {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusNotFound)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "not_found",
			})
			_, _ = writer.Write(buf)
			return
		}

		http.Redirect(writer, request, fmt.Sprintf("/miner/%s", miner.PayoutAddress().ToBase58()), http.StatusFound)
	})

	//other redirects
	serveMux.HandleFunc("/api/redirect/last_found{kind:|/full|/light|/raw|/blob|/info|/payouts|/coinbase}", func(writer http.ResponseWriter, request *http.Request) {
		b := index.QueryFirstResult(indexDb.GetFoundBlocks("WHERE reward > 0", 1))
		if b == nil {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		http.Redirect(writer, request, fmt.Sprintf("/api/block_by_id/%s%s?%s", b.MainBlock.SideTemplateId.String(), mux.Vars(request)["kind"], request.URL.RawQuery), http.StatusFound)
	})
	serveMux.HandleFunc("/api/redirect/tip{kind:|/full|/light|/raw|/blob|/info|/payouts|/coinbase}", func(writer http.ResponseWriter, request *http.Request) {
		tip := getSideBlockTipRedis(ctx, rdb)
		if tip == nil {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		http.Redirect(writer, request, fmt.Sprintf("/api/block_by_id/%s%s?%s", tip.TemplateId, mux.Vars(request)["kind"], request.URL.RawQuery), http.StatusFound)
	})

	serveMux.HandleFunc("/api/found_blocks", func(writer http.ResponseWriter, request *http.Request) {

		params := request.URL.Query()

		var limit uint64 = 50
		if params.Has("limit") {
			if i, err := strconv.Atoi(params.Get("limit")); err == nil {
				limit = uint64(i)
			}
		}

		var minerId uint64

		if params.Has("miner") {
			id := params.Get("miner")
			var miner *index.Miner

			if len(id) > 10 && (id[0] == '4' || id[0] == '8') {
				miner = indexDb.GetMinerByStringAddress(id)
			}

			if miner == nil {
				if i, err := strconv.Atoi(id); err == nil {
					miner = indexDb.GetMiner(uint64(i))
				}
			}

			if miner == nil {
				writer.Header().Set("Content-Type", "application/json; charset=utf-8")
				writer.WriteHeader(http.StatusNotFound)
				buf, _ := utils.MarshalJSON(struct {
					Error string `json:"error"`
				}{
					Error: "not_found",
				})
				_, _ = writer.Write(buf)
				return
			}

			minerId = miner.Id()
		}

		if limit > 1000 {
			limit = 1000
		}

		if limit == 0 {
			limit = 50
		}

		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusOK)

		var result index.QueryIterator[index.FoundBlock]
		var err error

		if minerId != 0 {
			result, err = indexDb.GetFoundBlocks("WHERE miner = $1", limit, minerId)
		} else {
			result, err = indexDb.GetFoundBlocks("", limit)
		}

		fillMiner := !params.Has("noMiner")

		if err != nil {
			panic(err)
		}
		defer result.Close()
		_ = httputils.StreamJsonIterator(request, writer, func() (int, *index.FoundBlock) {
			i, v := result.Next()
			if v != nil {
				v = fillFoundBlockResult(fillMiner, v)
			}
			return i, v
		})
	})

	serveMux.HandleFunc("/api/side_blocks", func(writer http.ResponseWriter, request *http.Request) {

		params := request.URL.Query()

		var limit uint64 = 50
		inclusion := index.InclusionInVerifiedChain
		if params.Has("limit") {
			if i, err := strconv.Atoi(params.Get("limit")); err == nil {
				limit = uint64(i)
			}
		}
		if params.Has("inclusion") {
			if i, err := strconv.Atoi(params.Get("inclusion")); err == nil {
				inclusion = index.BlockInclusion(i)
			}
		}

		if limit > consensus.ChainWindowSize*4*7 {
			limit = consensus.ChainWindowSize * 4 * 7
		}
		if limit == 0 {
			limit = 50
		}

		onlyBlocks := params.Has("onlyBlocks")

		var minerId uint64

		var miner *index.Miner
		if params.Has("miner") {
			id := params.Get("miner")

			if len(id) > 10 && (id[0] == '4' || id[0] == '8') {
				miner = indexDb.GetMinerByStringAddress(id)
			}

			if miner == nil {
				if i, err := strconv.Atoi(id); err == nil {
					miner = indexDb.GetMiner(uint64(i))
				}
			}

			if miner == nil {
				writer.Header().Set("Content-Type", "application/json; charset=utf-8")
				writer.WriteHeader(http.StatusNotFound)
				buf, _ := utils.MarshalJSON(struct {
					Error string `json:"error"`
				}{
					Error: "not_found",
				})
				_, _ = writer.Write(buf)
				return
			}

			minerId = miner.Id()
		}

		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusOK)

		fillUncles := !params.Has("noUncles")
		fillMined := !params.Has("noMainStatus")
		fillMiner := !params.Has("noMiner")

		result, err := indexDb.GetShares(limit, minerId, onlyBlocks, inclusion)
		if err != nil {
			panic(err)
		}
		defer result.Close()
		_ = httputils.StreamJsonIterator(request, writer, func() (int, *index.SideBlock) {
			i, v := result.Next()
			if v != nil {
				v = fillSideBlockResult(fillUncles, fillMined, fillMiner, v)
			}
			return i, v
		})
	})

	serveMux.HandleFunc("/api/shares", func(writer http.ResponseWriter, request *http.Request) {

		params := request.URL.Query()

		var limit uint64 = 50
		if params.Has("limit") {
			if i, err := strconv.Atoi(params.Get("limit")); err == nil {
				limit = uint64(i)
			}
		}

		if limit > consensus.ChainWindowSize*4*7 {
			limit = consensus.ChainWindowSize * 4 * 7
		}
		if limit == 0 {
			limit = 50
		}

		onlyBlocks := params.Has("onlyBlocks")

		var minerId uint64

		if params.Has("miner") {
			id := params.Get("miner")
			var miner *index.Miner

			if len(id) > 10 && (id[0] == '4' || id[0] == '8') {
				miner = indexDb.GetMinerByStringAddress(id)
			}

			if miner == nil {
				if i, err := strconv.Atoi(id); err == nil {
					miner = indexDb.GetMiner(uint64(i))
				}
			}

			if miner == nil {
				writer.Header().Set("Content-Type", "application/json; charset=utf-8")
				writer.WriteHeader(http.StatusNotFound)
				buf, _ := utils.MarshalJSON(struct {
					Error string `json:"error"`
				}{
					Error: "not_found",
				})
				_, _ = writer.Write(buf)
				return
			}

			minerId = miner.Id()
		}

		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusOK)

		fillUncles := !params.Has("noUncles")
		fillMined := !params.Has("noMainStatus")
		fillMiner := !params.Has("noMiner")

		result, err := indexDb.GetShares(limit, minerId, onlyBlocks, index.InclusionInVerifiedChain)
		if err != nil {
			panic(err)
		}
		defer result.Close()
		_ = httputils.StreamJsonIterator(request, writer, func() (int, *index.SideBlock) {
			i, v := result.Next()
			if v != nil {
				v = fillSideBlockResult(fillUncles, fillMined, fillMiner, v)
			}
			return i, v
		})
	})

	serveMux.HandleFunc("/api/main_block_by_{by:id|height}/{block:[0-9a-f]+|[0-9]+}", func(writer http.ResponseWriter, request *http.Request) {

		var block *index.MainBlock
		if mux.Vars(request)["by"] == "id" {
			if id, err := types.HashFromString(mux.Vars(request)["block"]); err == nil {
				if b := indexDb.GetMainBlockById(id); b != nil {
					block = b
				}
			}
		} else if mux.Vars(request)["by"] == "height" {
			if height, err := strconv.ParseUint(mux.Vars(request)["block"], 10, 0); err == nil {
				if b := indexDb.GetMainBlockByHeight(height); b != nil {
					block = b
				}
			}
		}

		if block == nil {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusNotFound)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "not_found",
			})
			_, _ = writer.Write(buf)
			return
		}

		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_ = httputils.EncodeJson(request, writer, block)
	})

	serveMux.HandleFunc("/api/main_difficulty_by_{by:id|height}/{block:[0-9a-f]+|[0-9]+}", func(writer http.ResponseWriter, request *http.Request) {

		var difficulty types.Difficulty
		if mux.Vars(request)["by"] == "id" {
			if id, err := types.HashFromString(mux.Vars(request)["block"]); err == nil {
				if b := indexDb.GetMainBlockById(id); b != nil {
					difficulty = types.DifficultyFrom64(b.Difficulty)
				} else if header := p2api.MainHeaderById(id); header != nil && !header.Difficulty.IsZero() {
					difficulty = header.Difficulty
				}
			}
		} else if mux.Vars(request)["by"] == "height" {
			if height, err := strconv.ParseUint(mux.Vars(request)["block"], 10, 0); err == nil {
				if b := indexDb.GetMainBlockByHeight(height); b != nil {
					difficulty = types.DifficultyFrom64(b.Difficulty)
				} else if diff := p2api.MainDifficultyByHeight(height); !diff.IsZero() {
					difficulty = diff
				}
			}
		}

		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_ = httputils.EncodeJson(request, writer, difficulty)
	})

	serveMux.HandleFunc("/api/block_by_{by:id|height}/{block:[0-9a-f]+|[0-9]+}{kind:|/full|/light|/raw|/blob|/payouts|/coinbase}", func(writer http.ResponseWriter, request *http.Request) {

		params := request.URL.Query()

		blockId := mux.Vars(request)["block"]

		var sideBlock *index.SideBlock
		if mux.Vars(request)["by"] == "id" {
			if id, err := types.HashFromString(blockId); err == nil {
				if b := indexDb.GetSideBlockByMainId(id); b != nil {
					sideBlock = b
				} else if b = indexDb.GetTipSideBlockByTemplateId(id); b != nil {
					sideBlock = b
				} else if bs := index.QueryIterateToSlice(indexDb.GetSideBlocksByTemplateId(id)); len(bs) != 0 {
					sideBlock = bs[0]
				} else if mb := indexDb.GetMainBlockByCoinbaseId(id); mb != nil {
					if b = indexDb.GetSideBlockByMainId(mb.Id); b != nil {
						sideBlock = b
					}
				}
			}
		} else if mux.Vars(request)["by"] == "height" {
			if height, err := strconv.ParseUint(blockId, 10, 0); err == nil {
				if b := indexDb.GetTipSideBlockByHeight(height); b != nil {
					sideBlock = b
				}
			}
		}

		if sideBlock == nil {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusNotFound)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "not_found",
			})
			_, _ = writer.Write(buf)
			return
		}

		requestKind := mux.Vars(request)["kind"]

		switch requestKind {
		case "/light", "/full", "/raw", "/blob":
			raw := p2api.LightByMainIdWithHint(sideBlock.MainId, sideBlock.TemplateId)

			if raw == nil {
				writer.Header().Set("Content-Type", "application/json; charset=utf-8")
				writer.WriteHeader(http.StatusNotFound)
				buf, _ := utils.MarshalJSON(struct {
					Error string `json:"error"`
				}{
					Error: "not_found",
				})
				_, _ = writer.Write(buf)
				return
			}

			if requestKind == "/full" || requestKind == "/raw" || requestKind == "/blob" {
				// Process block if needed
				if _, err := raw.PreProcessBlockWithOutputs(consensus, func(h types.Hash) *sidechain.PoolBlock {
					b := p2api.LightByTemplateId(h)
					if len(b) > 0 {
						return b[0]
					}
					return nil
				}, func() (outputs transaction.Outputs, bottomHeight uint64, err error) {
					preAllocatedShares := sidechain.PreAllocateShares(consensus.ChainWindowSize * 2)
					preAllocatedRewards := make([]uint64, 0, len(preAllocatedShares))
					return Outputs(p2api, indexDb, sideBlock, indexDb.DerivationCache(), preAllocatedShares, preAllocatedRewards)
				}); err != nil {
					writer.Header().Set("Content-Type", "application/json; charset=utf-8")
					writer.WriteHeader(http.StatusNotFound)
					buf, _ := utils.MarshalJSON(struct {
						Error string `json:"error"`
					}{
						Error: "could_not_process",
					})
					_, _ = writer.Write(buf)
					return
				}
			}

			if requestKind == "/raw" {
				metaBlob, _ := raw.Metadata.MarshalBinary()
				writer.Header().Set("X-Metadata", fasthex.EncodeToString(metaBlob))
				writer.Header().Set("Content-Type", "text/plain")
				writer.WriteHeader(http.StatusOK)
				buf, _ := raw.MarshalBinary()
				_, _ = writer.Write(buf)
			} else if requestKind == "/blob" {
				writer.Header().Set("Content-Type", "text/plain")
				writer.WriteHeader(http.StatusOK)
				buf, _ := raw.Main.MarshalBinary()
				_, _ = writer.Write([]byte(hex.EncodeToString(buf)))
			} else {
				writer.Header().Set("Content-Type", "application/json; charset=utf-8")
				writer.WriteHeader(http.StatusOK)
				_ = httputils.EncodeJson(request, writer, raw)
			}
		case "/payouts":
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusOK)

			result, err := indexDb.GetPayoutsBySideBlock(sideBlock)
			if err != nil {
				panic(err)
			}
			defer result.Close()
			_ = httputils.StreamJsonIterator(request, writer, result.Next)
		case "/coinbase":
			foundBlock := indexDb.GetMainBlockById(sideBlock.MainId)
			if foundBlock == nil {
				shares, _ := PayoutHint(p2api, indexDb, sideBlock)
				if shares != nil {
					poolBlock := p2api.LightByMainId(sideBlock.MainId)
					if poolBlock != nil {

						addresses := make(map[address.PackedAddress]*index.MainCoinbaseOutput, len(shares))
						for minerId, amount := range PayoutAmountHint(shares, poolBlock.Main.Coinbase.AuxiliaryData.TotalReward) {
							miner := indexDb.GetMiner(minerId)
							addresses[miner.Address().ToPackedAddress()] = &index.MainCoinbaseOutput{
								Id:                 types.ZeroHash,
								Miner:              miner.Id(),
								MinerAddress:       miner.Address(),
								MinerPayoutAddress: miner.OnlyPayoutAddress(),
								MinerAlias:         miner.Alias(),
								Value:              amount,
								GlobalOutputIndex:  0,
							}
						}

						sortedAddresses := utils.Keys(addresses)

						//Sort based on addresses
						slices.SortFunc(sortedAddresses, func(a address.PackedAddress, b address.PackedAddress) int {
							return a.Compare(&b)
						})

						//Shuffle shares
						sidechain.ShuffleShares(sortedAddresses, poolBlock.ShareVersion(), poolBlock.Side.CoinbasePrivateKeySeed)

						result := make(index.MainCoinbaseOutputs, len(sortedAddresses))

						for i, key := range sortedAddresses {
							addresses[key].Index = uint32(i)
							result[i] = *addresses[key]
						}

						writer.Header().Set("Content-Type", "application/json; charset=utf-8")
						writer.WriteHeader(http.StatusOK)
						_ = httputils.EncodeJson(request, writer, result)
						return
					}
				}

				writer.Header().Set("Content-Type", "application/json; charset=utf-8")
				writer.WriteHeader(http.StatusNotFound)
				buf, _ := utils.MarshalJSON(struct {
					Error string `json:"error"`
				}{
					Error: "not_found",
				})
				_, _ = writer.Write(buf)
				return
			} else {
				writer.Header().Set("Content-Type", "application/json; charset=utf-8")
				writer.WriteHeader(http.StatusOK)

				mainOutputs, err := indexDb.GetMainCoinbaseOutputs(foundBlock.CoinbaseId)
				if err != nil {
					panic(err)
				}
				defer mainOutputs.Close()
				_ = httputils.EncodeJson(request, writer, fillMainCoinbaseOutputs(params, index.IterateToSliceWithoutPointer[index.MainCoinbaseOutput](mainOutputs)))
			}
		default:
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusOK)
			_ = httputils.EncodeJson(request, writer, fillSideBlockResult(!params.Has("noUncles"), !params.Has("noMainStatus"), !params.Has("noMiner"), sideBlock))
		}
	})

	type difficultyStatResult struct {
		Height                    uint64 `json:"height"`
		Timestamp                 uint64 `json:"timestamp"`
		Difficulty                uint64 `json:"difficulty"`
		CumulativeDifficultyTop64 uint64 `json:"cumulative_difficulty_top64"`
		CumulativeDifficulty      uint64 `json:"cumulative_difficulty"`
	}

	type minerSeenResult struct {
		Height    uint64 `json:"height"`
		Timestamp uint64 `json:"timestamp"`
		Miners    uint64 `json:"miners"`
	}

	serveMux.HandleFunc("/api/stats/{kind:difficulty|miner_seen|miner_seen_window|version_seen$}", func(writer http.ResponseWriter, request *http.Request) {
		switch mux.Vars(request)["kind"] {
		case "difficulty":
			result := make(chan *difficultyStatResult)

			go func() {
				defer close(result)
				_ = indexDb.Query("SELECT side_height,timestamp,difficulty,cumulative_difficulty FROM side_blocks WHERE side_height % $1 = 0 ORDER BY side_height DESC;", func(row index.RowScanInterface) error {
					r := &difficultyStatResult{}

					var cumDiff types.Difficulty
					if err := row.Scan(&r.Height, &r.Timestamp, &r.Difficulty, &cumDiff); err != nil {
						return err
					}
					r.CumulativeDifficulty = cumDiff.Lo
					r.CumulativeDifficultyTop64 = cumDiff.Hi

					result <- r
					return nil
				}, 3600/consensus.TargetBlockTime)
			}()

			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusOK)
			_ = httputils.StreamJsonChan(request, writer, result)

		case "miner_seen":
			result := make(chan *minerSeenResult)

			go func() {
				defer close(result)
				_ = indexDb.Query("SELECT side_height,timestamp,(SELECT COUNT(DISTINCT(b.miner)) FROM side_blocks b WHERE b.side_height <= side_blocks.side_height AND b.side_height > (side_blocks.side_height - $2)) as count FROM side_blocks WHERE side_height % $1 = 0 ORDER BY side_height DESC;", func(row index.RowScanInterface) error {
					r := &minerSeenResult{}

					if err := row.Scan(&r.Height, &r.Timestamp, &r.Miners); err != nil {
						return err
					}

					result <- r
					return nil
				}, (3600*24)/consensus.TargetBlockTime, (3600*24*7)/consensus.TargetBlockTime)
			}()

			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusOK)
			_ = httputils.StreamJsonChan(request, writer, result)

		case "miner_seen_window":
			result := make(chan *minerSeenResult)

			go func() {
				defer close(result)
				_ = indexDb.Query("SELECT side_height,timestamp,(SELECT COUNT(DISTINCT(b.miner)) FROM side_blocks b WHERE b.side_height <= side_blocks.side_height AND b.side_height > (side_blocks.side_height - $2)) as count FROM side_blocks WHERE side_height % $1 = 0 ORDER BY side_height DESC;", func(row index.RowScanInterface) error {
					r := &minerSeenResult{}

					if err := row.Scan(&r.Height, &r.Timestamp, &r.Miners); err != nil {
						return err
					}

					result <- r
					return nil
				}, consensus.ChainWindowSize, consensus.ChainWindowSize)
			}()

			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusOK)
			_ = httputils.StreamJsonChan(request, writer, result)
		}
	})

	// p2pool consensus server api
	serveMux.HandleFunc("/api/consensus/peer_list", func(writer http.ResponseWriter, request *http.Request) {

		writer.Header().Set("Content-Type", "text/plain")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(p2api.PeerList())
	})

	serveMux.HandleFunc("/api/consensus/connection_check/{addrPort:.+}", func(writer http.ResponseWriter, request *http.Request) {
		addrPort, err := netip.ParseAddrPort(mux.Vars(request)["addrPort"])
		if err != nil {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusBadRequest)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: err.Error(),
			})
			_, _ = writer.Write(buf)
			return
		}

		addrPort = netip.AddrPortFrom(addrPort.Addr().Unmap(), addrPort.Port())

		if !addrPort.IsValid() || addrPort.Addr().IsLoopback() || addrPort.Addr().IsMulticast() || addrPort.Addr().IsInterfaceLocalMulticast() || addrPort.Addr().IsLinkLocalMulticast() || addrPort.Addr().IsPrivate() || addrPort.Addr().IsUnspecified() || addrPort.Addr().IsLinkLocalUnicast() {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusBadRequest)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "not_valid_ip",
			})
			_, _ = writer.Write(buf)
			return
		}

		if addrPort.Port() != consensus.DefaultPort() && addrPort.Port() < 10000 {
			//do not allow low port numbers to prevent targeting
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusBadRequest)
			buf, _ := utils.MarshalJSON(struct {
				Error string `json:"error"`
			}{
				Error: "not_valid_port",
			})
			_, _ = writer.Write(buf)
			return
		}

		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_ = httputils.EncodeJson(request, writer, p2api.ConnectionCheck(addrPort))
	})

	// p2pool disk / p2pool.io emulation
	serveMux.HandleFunc("/api/network/stats", func(writer http.ResponseWriter, request *http.Request) {
		type networkStats struct {
			Difficulty uint64     `json:"difficulty"`
			Hash       types.Hash `json:"hash"`
			Height     uint64     `json:"height"`
			Reward     uint64     `json:"reward"`
			Timestamp  uint64     `json:"timestamp"`
		}

		mainTip, err := getMainBlockTipRedis(ctx, rdb)
		if err != nil {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		mainTipHash, _ := types.HashFromString(mainTip.Id)

		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_ = httputils.EncodeJson(request, writer, networkStats{
			Difficulty: mainTip.Difficulty,
			Hash:       mainTipHash,
			Height:     mainTip.Height,
			Reward:     mainTip.Reward,
			Timestamp:  mainTip.Timestamp,
		})
	})
	serveMux.HandleFunc("/api/pool/blocks", func(writer http.ResponseWriter, request *http.Request) {
		type poolBlock struct {
			Timestamp   uint64                 `json:"ts"`
			Hash        types.Hash             `json:"hash"`
			Difficulty  uint64                 `json:"diff"`
			TotalHashes uint64                 `json:"totalHashes"`
			Height      uint64                 `json:"height"`
			Valid       bool                   `json:"valid"`
			Unlocked    bool                   `json:"unlocked"`
			PoolType    string                 `json:"pool_type"`
			Value       uint64                 `json:"value"`
			Finder      string                 `json:"finder"`
			TxKey       crypto.PrivateKeyBytes `json:"tx_key,omitempty"`
		}
		params := request.URL.Query()

		var limit uint64 = 200
		if params.Has("limit") {
			if i, err := strconv.Atoi(params.Get("limit")); err == nil {
				limit = uint64(i)
			}
		}

		if limit <= 0 {
			limit = 200
		}
		if limit > 1000 {
			limit = 1000
		}

		blocks := make([]poolBlock, 0, limit)

		result, err := indexDb.GetFoundBlocks("", limit)
		if err != nil {
			panic(err)
		}

		poolInfo := lastPoolInfo.Load()

		index.QueryIterate(result, func(_ int, b *index.FoundBlock) (stop bool) {
			b = fillFoundBlockResult(true, b)
			blocks = append(blocks, poolBlock{
				Height:      b.MainBlock.Height,
				Hash:        b.MainBlock.Id,
				Difficulty:  b.MainBlock.Difficulty,
				TotalHashes: b.CumulativeDifficulty.Lo,
				Timestamp:   b.MainBlock.Timestamp,
				Valid:       !b.Orphan(),
				Unlocked:    !b.Orphan() && poolInfo.MainChain.Height-b.MainBlock.Height > monero.MinerRewardUnlockTime,
				PoolType:    "pplns",
				Value:       b.MainBlock.Reward,
				Finder:      string(b.MinerAddress.ToBase58()),
				TxKey:       b.MainBlock.CoinbasePrivateKey,
			})
			return false
		})

		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_ = httputils.EncodeJson(request, writer, blocks)
	})
	serveMux.HandleFunc("/api/pool/stats", func(writer http.ResponseWriter, request *http.Request) {
		type poolStats struct {
			PoolList       []string `json:"pool_list"`
			PoolStatistics struct {
				HashRate            uint64 `json:"hashRate"`
				Miners              uint64 `json:"miners"`
				TotalHashes         uint64 `json:"totalHashes"`
				LastBlockFoundTime  uint64 `json:"lastBlockFoundTime"`
				LastBlockFound      uint64 `json:"lastBlockFound"`
				TotalBlocksFound    uint64 `json:"totalBlocksFound"`
				PPLNSWindowSize     uint64 `json:"pplnsWindowSize"`
				SidechainDifficulty uint64 `json:"sidechainDifficulty"`
				SidechainHeight     uint64 `json:"sidechainHeight"`
			} `json:"pool_statistics"`
		}

		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusOK)

		poolInfo := lastPoolInfo.Load()

		var lastBlockFound, lastBlockFoundTime uint64
		b := index.QueryFirstResult(indexDb.GetFoundBlocks("", 1))
		if b != nil {
			lastBlockFound = b.MainBlock.Height
			lastBlockFoundTime = b.MainBlock.Timestamp
		}

		_ = httputils.EncodeJson(request, writer, poolStats{
			PoolList: []string{"pplns"},
			PoolStatistics: struct {
				HashRate            uint64 `json:"hashRate"`
				Miners              uint64 `json:"miners"`
				TotalHashes         uint64 `json:"totalHashes"`
				LastBlockFoundTime  uint64 `json:"lastBlockFoundTime"`
				LastBlockFound      uint64 `json:"lastBlockFound"`
				TotalBlocksFound    uint64 `json:"totalBlocksFound"`
				PPLNSWindowSize     uint64 `json:"pplnsWindowSize"`
				SidechainDifficulty uint64 `json:"sidechainDifficulty"`
				SidechainHeight     uint64 `json:"sidechainHeight"`
			}{
				HashRate:            poolInfo.SideChain.LastBlock.Difficulty / consensus.TargetBlockTime,
				Miners:              poolInfo.SideChain.Miners,
				TotalHashes:         poolInfo.SideChain.LastBlock.CumulativeDifficulty.Lo,
				LastBlockFound:      lastBlockFound,
				LastBlockFoundTime:  lastBlockFoundTime,
				TotalBlocksFound:    poolInfo.SideChain.Found,
				PPLNSWindowSize:     uint64(poolInfo.SideChain.Window.Blocks),
				SidechainDifficulty: poolInfo.SideChain.LastBlock.Difficulty,
				SidechainHeight:     poolInfo.SideChain.LastBlock.SideHeight,
			},
		})
	})

	serveMux.HandleFunc("/api/config", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("{\"pplns_fee\":0,\"min_wallet_payout\":300000000,\"dev_donation\":0,\"pool_dev_donation\":0,\"maturity_depth\":60,\"min_denom\":1}"))
	})

	serveMux.HandleFunc("/api/stats_mod", func(writer http.ResponseWriter, request *http.Request) {

		mainTip, err := getMainBlockTipRedis(ctx, rdb)
		if err != nil {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		poolInfo := lastPoolInfo.Load()

		var lastBlockFound, lastBlockFoundTime uint64
		var lastBlockFoundHash types.Hash
		var lastBlockCumulativeDifficulty types.Difficulty
		b := index.QueryFirstResult(indexDb.GetFoundBlocks("", 1))
		if b != nil {
			lastBlockFound = b.MainBlock.Height
			lastBlockFoundTime = b.MainBlock.Timestamp
			lastBlockFoundHash = b.MainBlock.Id
			lastBlockCumulativeDifficulty = b.CumulativeDifficulty
		}

		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(fmt.Sprintf(
			"{\"config\":{\"ports\":[{\"port\":3333,\"tls\":false}],\"fee\":0,\"minPaymentThreshold\":300000000},\"network\":{\"height\":%d},\"pool\":{\"stats\":{\"lastBlockFound\":\"%d\"},\"blocks\":[\"%s...%s:%d\",\"%d\"],\"miners\":%d,\"hashrate\":%d,\"roundHashes\":%d}}",
			mainTip.Height,
			lastBlockFoundTime*1000,
			hex.EncodeToString(lastBlockFoundHash[:2]),
			hex.EncodeToString(lastBlockFoundHash[types.HashSize-2:]),
			lastBlockFoundTime,
			lastBlockFound,
			poolInfo.SideChain.Miners,
			poolInfo.SideChain.LastBlock.Difficulty/consensus.TargetBlockTime,
			poolInfo.SideChain.LastBlock.CumulativeDifficulty.Sub(lastBlockCumulativeDifficulty).Lo,
		)))
	})

	server := &http.Server{
		Addr:        "0.0.0.0:8080",
		ReadTimeout: time.Second * 2,
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != "GET" && request.Method != "HEAD" && request.Method != "POST" {
				writer.WriteHeader(http.StatusForbidden)
				return
			}

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
