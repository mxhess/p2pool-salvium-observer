// daemon2 is the clean P2Pool Salvium observer daemon.
// It uses only the p2pool package, without the old buggy consensus dependency.
//
// This daemon:
// 1. Reads shares from the C++ Redis cache (p2pool:cache)
// 2. Calculates PPLNS using the correct C++ algorithm
// 3. Fetches mainchain difficulty from Salvium RPC
// 4. Stores results in Redis for the API
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"git.gammaspectra.live/P2Pool/observer/p2pool"
	"github.com/redis/go-redis/v9"
)

// PPLNSMinerData represents per-miner PPLNS data for Redis storage
type PPLNSMinerData struct {
	Address         string  `json:"address"`
	Weight          string  `json:"weight"`           // As string for 128-bit values
	Percentage      float64 `json:"percentage"`       // 0-100
	EstimatedPayout uint64  `json:"estimated_payout"` // In atomic units
	ShareCount      int     `json:"share_count"`      // Number of main shares in window
	UncleCount      int     `json:"uncle_count"`      // Number of uncle shares in window
	Hashrate        float64 `json:"hashrate"`         // Estimated hashrate in H/s
}

// PPLNSFullData represents complete PPLNS data for Redis storage
type PPLNSFullData struct {
	// Window statistics
	TipHeight      uint64 `json:"tip_height"`
	BottomHeight   uint64 `json:"bottom_height"`
	BlocksIncluded int    `json:"blocks_included"`
	UnclesIncluded int    `json:"uncles_included"`
	TotalWeight    string `json:"total_weight"`    // As string for 128-bit
	WindowDuration int64  `json:"window_duration"` // Window duration in seconds

	// Per-miner data (sorted by weight descending)
	Miners []PPLNSMinerData `json:"miners"`

	// Calculation metadata
	MainchainDifficulty string `json:"mainchain_difficulty"`
	BlockReward         uint64 `json:"block_reward"` // Current block reward estimate
	CalculatedAt        int64  `json:"calculated_at"`
}

// PoolStats represents basic pool statistics for Redis
type PoolStats struct {
	SidechainHeight uint64 `json:"sidechain_height"`
	SharesInCache   int    `json:"shares_in_cache"`
	MinersInWindow  int    `json:"miners_in_window"`
	BlocksInWindow  int    `json:"blocks_in_window"`
	UnclesInWindow  int    `json:"uncles_in_window"`
	UpdatedAt       int64  `json:"updated_at"`
}

// NetworkStats represents mainchain network statistics
type NetworkStats struct {
	Height     uint64 `json:"height"`
	Difficulty string `json:"difficulty"` // 128-bit as string
	Reward     uint64 `json:"reward"`     // Block reward in atomic units
	UpdatedAt  int64  `json:"updated_at"`
}

// FoundBlock represents a found mainchain block
// Format from C++: "timestamp height hash difficulty total_hashes"
type FoundBlock struct {
	Timestamp   int64  `json:"timestamp"`
	Height      uint64 `json:"height"`
	Hash        string `json:"hash"`
	Difficulty  string `json:"difficulty"`
	TotalHashes string `json:"total_hashes"` // Cumulative hashes at time of find
}

// EffortStats represents current pool effort statistics
type EffortStats struct {
	CurrentEffort  float64 `json:"current_effort"`  // % toward next block
	RoundHashes    uint64  `json:"round_hashes"`    // Hashes since last found block
	LastFoundTime  int64   `json:"last_found_time"` // Unix timestamp of last found
	LastFoundBlock uint64  `json:"last_found_block"`
	TotalFound     int     `json:"total_found"`
	UpdatedAt      int64   `json:"updated_at"`
}

// ShareData represents individual share data for Redis storage
type ShareData struct {
	TemplateId           string   `json:"template_id"`
	SideHeight           uint64   `json:"side_height"`
	MainHeight           uint64   `json:"main_height"`
	Timestamp            uint64   `json:"timestamp"`
	Difficulty           string   `json:"difficulty"`
	CumulativeDifficulty string   `json:"cumulative_difficulty"`
	MinerAddress         string   `json:"miner_address"`
	Parent               string   `json:"parent"`
	Uncles               []string `json:"uncles,omitempty"`
}

// Payout represents a single payout to a miner from a found block
type Payout struct {
	BlockHeight uint64  `json:"block_height"` // Mainchain height
	BlockHash   string  `json:"block_hash"`
	Amount      uint64  `json:"amount"`     // In atomic units
	Percentage  float64 `json:"percentage"` // Miner's share %
	Timestamp   int64   `json:"timestamp"`  // When block was found
}

// Daemon is the main observer daemon
type Daemon struct {
	ctx    context.Context
	cancel context.CancelFunc

	// Redis clients
	redis       *redis.Client // For storing observer data
	p2poolCache *p2pool.CacheReader

	// Salvium RPC client
	salvium *p2pool.SalviumClient

	// Cached state
	shares            map[p2pool.Hash]*p2pool.Share
	mainchainDiff     p2pool.Difficulty
	lastBlockReward   uint64
	lastSyncTime      time.Time
	foundBlocks       []FoundBlock
	processedPayouts  map[uint64]bool          // Block heights we've calculated payouts for
	minerPayouts      map[string][]Payout      // Address -> list of payouts
}

func main() {
	// Command line flags
	redisAddr := flag.String("redis", "127.0.0.1:6379", "Redis address for storage")
	salviumRPC := flag.String("salvium-rpc", "http://127.0.0.1:19081", "Salvium RPC URL(s), comma-separated for failover")
	syncInterval := flag.Duration("interval", 10*time.Second, "Sync interval")
	rebuildPayouts := flag.Bool("rebuild-payouts", false, "Rebuild all payouts from on-chain data and exit")
	flag.Parse()

	p2pool.Logf("DAEMON", "P2Pool Salvium Observer Daemon v2 (clean)")
	p2pool.Logf("DAEMON", "Redis: %s", *redisAddr)
	p2pool.Logf("DAEMON", "Salvium RPC: %s", *salviumRPC)
	if *rebuildPayouts {
		p2pool.Logf("DAEMON", "Mode: REBUILD PAYOUTS")
	} else {
		p2pool.Logf("DAEMON", "Sync interval: %s", *syncInterval)
	}

	// Setup context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		p2pool.Logf("DAEMON", "Received signal %s, shutting down...", sig)
		cancel()
	}()

	// Initialize Redis client
	redisClient := redis.NewClient(&redis.Options{
		Addr:         *redisAddr,
		Password:     "",
		DB:           0,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	})
	defer redisClient.Close()

	// Test Redis connection
	if err := redisClient.Ping(ctx).Err(); err != nil {
		p2pool.Errorf("DAEMON", "Failed to connect to Redis: %v", err)
		os.Exit(1)
	}
	p2pool.Logf("DAEMON", "Connected to Redis")

	// Initialize Salvium RPC client
	salviumClient, err := p2pool.NewSalviumClient(*salviumRPC)
	if err != nil {
		p2pool.Errorf("DAEMON", "Failed to create Salvium client: %v", err)
		os.Exit(1)
	}

	// Test Salvium connection
	info, err := salviumClient.GetInfo(ctx)
	if err != nil {
		p2pool.Errorf("DAEMON", "Failed to connect to Salvium: %v", err)
		os.Exit(1)
	}
	p2pool.Logf("DAEMON", "Connected to Salvium via %s (height=%d, version=%s)",
		salviumClient.CurrentEndpoint(), info.Height, info.Version)
	if salviumClient.EndpointCount() > 1 {
		p2pool.Logf("DAEMON", "RPC failover: %d endpoints configured", salviumClient.EndpointCount())
	}

	// Create daemon
	daemon := &Daemon{
		ctx:              ctx,
		cancel:           cancel,
		redis:            redisClient,
		p2poolCache:      p2pool.NewCacheReader(redisClient, "p2pool:cache"),
		salvium:          salviumClient,
		shares:           make(map[p2pool.Hash]*p2pool.Share),
		processedPayouts: make(map[uint64]bool),
		minerPayouts:     make(map[string][]Payout),
	}

	// Handle rebuild-payouts mode
	if *rebuildPayouts {
		p2pool.Logf("DAEMON", "Loading data for payout rebuild...")

		// Load shares from cache
		shares, err := daemon.p2poolCache.LoadAllShares(ctx)
		if err != nil {
			p2pool.Errorf("DAEMON", "Failed to load shares: %v", err)
			os.Exit(1)
		}
		daemon.shares = shares
		p2pool.Logf("DAEMON", "Loaded %d shares", len(shares))

		// Get mainchain difficulty
		diff, err := daemon.salvium.GetDifficulty(ctx)
		if err != nil {
			p2pool.Errorf("DAEMON", "Failed to get mainchain difficulty: %v", err)
			os.Exit(1)
		}
		daemon.mainchainDiff = diff

		// Load found blocks
		daemon.loadFoundBlocks()
		p2pool.Logf("DAEMON", "Loaded %d found blocks", len(daemon.foundBlocks))

		// Rebuild payouts
		if err := daemon.RebuildAllPayouts(); err != nil {
			p2pool.Errorf("DAEMON", "Rebuild failed: %v", err)
			os.Exit(1)
		}

		p2pool.Logf("DAEMON", "Payout rebuild complete!")
		return
	}

	// Run main loop
	daemon.run(*syncInterval)
}

func (d *Daemon) run(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial sync
	p2pool.Logf("DAEMON", "Starting initial sync...")
	d.syncCycle()

	for {
		select {
		case <-d.ctx.Done():
			p2pool.Logf("DAEMON", "Shutdown complete")
			return
		case <-ticker.C:
			d.syncCycle()
		}
	}
}

func (d *Daemon) syncCycle() {
	start := time.Now()

	// 1. Load shares from C++ cache
	shares, err := d.p2poolCache.LoadAllShares(d.ctx)
	if err != nil {
		p2pool.Errorf("SYNC", "Failed to load shares: %v", err)
		return
	}
	d.shares = shares
	p2pool.Logf("SYNC", "Loaded %d shares from cache", len(shares))

	if len(shares) == 0 {
		p2pool.Logf("SYNC", "No shares in cache, skipping PPLNS")
		return
	}

	// 2. Find the tip (highest sidechain height)
	tip := d.findTip()
	if tip == nil {
		p2pool.Errorf("SYNC", "No tip found")
		return
	}
	p2pool.Logf("SYNC", "Tip: height=%d", tip.SidechainHeight)

	// 3. Get mainchain difficulty from Salvium
	diff, err := d.salvium.GetDifficulty(d.ctx)
	if err != nil {
		p2pool.Errorf("SYNC", "Failed to get mainchain difficulty: %v", err)
		// Use cached value if available
		if d.mainchainDiff.Lo == 0 {
			d.mainchainDiff = p2pool.Difficulty{Lo: 5_000_000_000} // Default fallback
		}
	} else {
		d.mainchainDiff = diff
	}
	p2pool.Logf("SYNC", "Mainchain difficulty: %s", d.mainchainDiff.String())

	// 4. Get current block reward (from last block header)
	header, err := d.salvium.GetLastBlockHeader(d.ctx)
	if err == nil {
		d.lastBlockReward = header.BlockHeader.Reward
	}

	// 5. Calculate PPLNS
	pplns := p2pool.CalculatePPLNS(shares, tip, d.mainchainDiff)
	p2pool.Logf("SYNC", "PPLNS: %d blocks, %d uncles, %d miners",
		pplns.BlocksIncluded, pplns.UnclesIncluded, len(pplns.Shares))

	// 6. Store results to Redis
	d.storePPLNS(pplns)
	d.storeStats(tip, pplns)
	d.storeNetworkStats()
	d.storeShares(tip, pplns)
	d.storeHashrate(pplns)

	// 7. Load and process found blocks
	d.loadFoundBlocks()
	d.storeEffort(tip)

	// 8. Calculate payouts for found blocks
	d.calculatePayouts(pplns)

	elapsed := time.Since(start)
	p2pool.Logf("SYNC", "Sync completed in %v", elapsed)
	d.lastSyncTime = time.Now()
}

func (d *Daemon) findTip() *p2pool.Share {
	var tip *p2pool.Share
	for _, share := range d.shares {
		if tip == nil || share.SidechainHeight > tip.SidechainHeight {
			tip = share
		}
	}
	return tip
}

func (d *Daemon) storePPLNS(pplns *p2pool.PPLNSResult) {
	ctx := d.ctx

	// Build full PPLNS data
	fullData := PPLNSFullData{
		TipHeight:           pplns.TipHeight,
		BottomHeight:        pplns.BottomHeight,
		BlocksIncluded:      pplns.BlocksIncluded,
		UnclesIncluded:      pplns.UnclesIncluded,
		TotalWeight:         pplns.TotalWeight.String(),
		WindowDuration:      pplns.WindowDuration,
		MainchainDifficulty: d.mainchainDiff.String(),
		BlockReward:         d.lastBlockReward,
		CalculatedAt:        time.Now().Unix(),
		Miners:              make([]PPLNSMinerData, 0, len(pplns.Shares)),
	}

	// Calculate estimated payouts
	// Salvium uses 1e8 atomic units (100 million per SAL)
	blockReward := d.lastBlockReward
	if blockReward == 0 {
		blockReward = 8_500_000_000 // ~85 SAL default (1e8 atomic units)
	}
	payouts := pplns.EstimatedPayout(blockReward)

	// Get all miners sorted by weight
	topMiners := pplns.TopMiners(len(pplns.Shares))
	for _, mw := range topMiners {
		// Calculate hashrate: weight / window_duration
		// Weight is accumulated difficulty, hashrate = difficulty / time
		var hashrate float64
		if pplns.WindowDuration > 0 {
			// Convert weight to float64 for hashrate calculation
			weightFloat := float64(mw.Weight.Hi)*float64(1<<64) + float64(mw.Weight.Lo)
			hashrate = weightFloat / float64(pplns.WindowDuration)
		}

		minerData := PPLNSMinerData{
			Address:         mw.Address.ToBase58(),
			Weight:          mw.Weight.String(),
			Percentage:      pplns.MinerPercentage(mw.Address),
			EstimatedPayout: payouts[mw.Address],
			ShareCount:      mw.ShareCount,
			UncleCount:      mw.UncleCount,
			Hashrate:        hashrate,
		}
		fullData.Miners = append(fullData.Miners, minerData)
	}

	// Store complete PPLNS data
	fullDataJSON, _ := json.Marshal(fullData)
	d.redis.Set(ctx, "cache:pplns:full", string(fullDataJSON), 0)

	// Store per-miner data for quick lookups
	for _, minerData := range fullData.Miners {
		minerKey := fmt.Sprintf("cache:pplns:miner:%s", minerData.Address)
		minerJSON, _ := json.Marshal(minerData)
		d.redis.Set(ctx, minerKey, string(minerJSON), 24*time.Hour)
	}

	// Store legacy format for API compatibility
	legacyData := map[string]interface{}{
		"miners":          len(fullData.Miners),
		"blocks":          pplns.BlocksIncluded,
		"uncles":          pplns.UnclesIncluded,
		"weight":          pplns.TotalWeight.Lo,
		"bottom_height":   pplns.BottomHeight,
		"window_size":     p2pool.PPLNSWindow,
		"window_duration": pplns.WindowDuration,
		"calculated_at":   fullData.CalculatedAt,
	}
	legacyJSON, _ := json.Marshal(legacyData)
	d.redis.Set(ctx, "cache:pplns:window", string(legacyJSON), 0)

	p2pool.Logf("REDIS", "Stored PPLNS data (%d miners)", len(fullData.Miners))
}

func (d *Daemon) storeStats(tip *p2pool.Share, pplns *p2pool.PPLNSResult) {
	ctx := d.ctx

	stats := PoolStats{
		SidechainHeight: tip.SidechainHeight,
		SharesInCache:   len(d.shares),
		MinersInWindow:  len(pplns.Shares),
		BlocksInWindow:  pplns.BlocksIncluded,
		UnclesInWindow:  pplns.UnclesIncluded,
		UpdatedAt:       time.Now().Unix(),
	}

	statsJSON, _ := json.Marshal(stats)
	d.redis.Set(ctx, "stats:pool", string(statsJSON), 0)

	// Also set individual keys for compatibility
	d.redis.Set(ctx, "stats:sidechain:height", tip.SidechainHeight, 0)
	d.redis.Set(ctx, "stats:pool:miners", len(pplns.Shares), 0)
}

func (d *Daemon) storeNetworkStats() {
	ctx := d.ctx

	info, err := d.salvium.GetInfo(ctx)
	if err != nil {
		return
	}

	stats := NetworkStats{
		Height:     info.Height,
		Difficulty: d.mainchainDiff.String(),
		Reward:     d.lastBlockReward,
		UpdatedAt:  time.Now().Unix(),
	}

	statsJSON, _ := json.Marshal(stats)
	d.redis.Set(ctx, "stats:network", string(statsJSON), 0)

	// Also set individual keys for compatibility
	d.redis.Set(ctx, "stats:network:height", info.Height, 0)
	d.redis.Set(ctx, "stats:network:difficulty", d.mainchainDiff.Lo, 0)
	d.redis.Set(ctx, "stats:network:reward", d.lastBlockReward, 0)
}

// storeShares stores individual share data and miner share lists
func (d *Daemon) storeShares(tip *p2pool.Share, pplns *p2pool.PPLNSResult) {
	ctx := d.ctx

	// Track shares per miner for this cycle
	minerShares := make(map[string][]string) // address -> list of template IDs

	// Store recent shares list (for /api/side_blocks)
	recentShares := make([]ShareData, 0, 100)

	// Walk the PPLNS window and store share data
	current := tip
	count := 0
	for current != nil && count < p2pool.PPLNSWindow {
		templateId := current.SidechainId.String()
		minerAddr := current.MinerWallet.ToBase58()

		// Build uncle list
		uncles := make([]string, len(current.Uncles))
		for i, u := range current.Uncles {
			uncles[i] = u.String()
		}

		// Create share data
		shareData := ShareData{
			TemplateId:           templateId,
			SideHeight:           current.SidechainHeight,
			MainHeight:           current.MainchainHeight,
			Timestamp:            current.Timestamp,
			Difficulty:           current.Difficulty.String(),
			CumulativeDifficulty: current.CumulativeDifficulty.String(),
			MinerAddress:         minerAddr,
			Parent:               current.Parent.String(),
			Uncles:               uncles,
		}

		// Store share data by template ID
		shareJSON, _ := json.Marshal(shareData)
		shareKey := fmt.Sprintf("share:data:%s", templateId)
		d.redis.Set(ctx, shareKey, string(shareJSON), 24*time.Hour)

		// Store template ID by sidechain height
		heightKey := fmt.Sprintf("sideblock:height:%d", current.SidechainHeight)
		d.redis.Set(ctx, heightKey, templateId, 24*time.Hour)

		// Track for miner shares list
		minerShares[minerAddr] = append(minerShares[minerAddr], templateId)

		// Add to recent shares (limit to 100)
		if len(recentShares) < 100 {
			recentShares = append(recentShares, shareData)
		}

		// Move to parent
		count++
		if current.Parent.IsZero() {
			break
		}
		parent, ok := d.shares[current.Parent]
		if !ok {
			break
		}
		current = parent
	}

	// Store miner share lists (most recent first)
	for addr, templateIds := range minerShares {
		minerKey := fmt.Sprintf("miner:%s:shares", addr)
		// Store as JSON array
		idsJSON, _ := json.Marshal(templateIds)
		d.redis.Set(ctx, minerKey, string(idsJSON), 24*time.Hour)
	}

	// Store recent shares list for API
	if len(recentShares) > 0 {
		recentJSON, _ := json.Marshal(recentShares)
		d.redis.Set(ctx, "cache:recent_shares", string(recentJSON), 0)
	}

	// Store tip share data for /api/redirect/tip
	if tip != nil {
		tipData := ShareData{
			TemplateId:           tip.SidechainId.String(),
			SideHeight:           tip.SidechainHeight,
			MainHeight:           tip.MainchainHeight,
			Timestamp:            tip.Timestamp,
			Difficulty:           tip.Difficulty.String(),
			CumulativeDifficulty: tip.CumulativeDifficulty.String(),
			MinerAddress:         tip.MinerWallet.ToBase58(),
			Parent:               tip.Parent.String(),
		}
		tipJSON, _ := json.Marshal(tipData)
		d.redis.Set(ctx, "cache:tip", string(tipJSON), 0)
	}

	p2pool.Logf("REDIS", "Stored %d shares, %d miner lists, %d recent", count, len(minerShares), len(recentShares))
}

// storeHashrate calculates and stores pool hashrate
func (d *Daemon) storeHashrate(pplns *p2pool.PPLNSResult) {
	ctx := d.ctx

	// Calculate hashrate from PPLNS window
	// Hashrate = total difficulty / time span
	// PPLNS window is ~2160 blocks at ~10 second average = ~6 hours
	// We use actual timestamps from bottom to tip for accuracy

	if pplns.BlocksIncluded < 2 {
		return
	}

	// Find bottom and tip shares to get time span
	var bottomShare, tipShare *p2pool.Share
	for _, share := range d.shares {
		if share.SidechainHeight == pplns.BottomHeight {
			bottomShare = share
		}
		if share.SidechainHeight == pplns.TipHeight {
			tipShare = share
		}
	}

	if bottomShare == nil || tipShare == nil {
		return
	}

	// Calculate time span in seconds
	timeSpan := tipShare.Timestamp - bottomShare.Timestamp
	if timeSpan == 0 {
		timeSpan = 1 // Avoid division by zero
	}

	// Hashrate = difficulty / time (in H/s)
	// TotalWeight is the sum of all difficulties in window
	hashrate := pplns.TotalWeight.Lo / timeSpan

	d.redis.Set(ctx, "stats:pool:hashrate", hashrate, 0)

	p2pool.Logf("REDIS", "Pool hashrate: %d H/s (from %d blocks over %d seconds)",
		hashrate, pplns.BlocksIncluded, timeSpan)
}

// loadFoundBlocks loads found blocks from p2pool:found_blocks Redis list
// Format from C++: "timestamp height hash difficulty total_hashes"
func (d *Daemon) loadFoundBlocks() {
	ctx := d.ctx

	// Read all found blocks from Redis list
	entries, err := d.redis.LRange(ctx, "p2pool:found_blocks", 0, -1).Result()
	if err != nil {
		p2pool.Errorf("FOUND", "Failed to read found_blocks: %v", err)
		return
	}

	if len(entries) == 0 {
		p2pool.Logf("FOUND", "No found blocks in Redis")
		return
	}

	// Parse entries
	d.foundBlocks = make([]FoundBlock, 0, len(entries))
	for _, entry := range entries {
		var fb FoundBlock
		// Parse space-separated format: timestamp height hash difficulty total_hashes
		n, err := fmt.Sscanf(entry, "%d %d %s %s %s",
			&fb.Timestamp, &fb.Height, &fb.Hash, &fb.Difficulty, &fb.TotalHashes)
		if err != nil || n < 3 {
			// Try parsing without total_hashes for older entries
			n, err = fmt.Sscanf(entry, "%d %d %s %s",
				&fb.Timestamp, &fb.Height, &fb.Hash, &fb.Difficulty)
			if err != nil || n < 3 {
				p2pool.Errorf("FOUND", "Failed to parse found block: %q", entry)
				continue
			}
		}
		d.foundBlocks = append(d.foundBlocks, fb)
	}

	// Store found blocks as JSON for API
	if len(d.foundBlocks) > 0 {
		foundJSON, _ := json.Marshal(d.foundBlocks)
		d.redis.Set(ctx, "cache:found_blocks", string(foundJSON), 0)
		d.redis.Set(ctx, "stats:pool:blocks_found", len(d.foundBlocks), 0)

		// Store last found block info
		lastFound := d.foundBlocks[len(d.foundBlocks)-1]
		d.redis.Set(ctx, "stats:pool:last_found_time", lastFound.Timestamp, 0)
		d.redis.Set(ctx, "stats:pool:last_found_height", lastFound.Height, 0)
	}

	p2pool.Logf("FOUND", "Loaded %d found blocks", len(d.foundBlocks))
}

// storeEffort calculates and stores pool effort statistics
// Effort = (hashes_since_last_found_block * 100) / mainchain_difficulty
func (d *Daemon) storeEffort(tip *p2pool.Share) {
	ctx := d.ctx

	if len(d.foundBlocks) == 0 || tip == nil {
		return
	}

	lastFound := d.foundBlocks[len(d.foundBlocks)-1]

	// Calculate round hashes (hashes since last found block)
	// We use cumulative difficulty as a proxy for total hashes
	// roundHashes = current_cumulative_diff - last_found_cumulative_diff
	var roundHashes uint64
	if lastFound.TotalHashes != "" {
		// Parse total_hashes from last found block
		var lastTotalHashes uint64
		fmt.Sscanf(lastFound.TotalHashes, "%d", &lastTotalHashes)
		if tip.CumulativeDifficulty.Lo > lastTotalHashes {
			roundHashes = tip.CumulativeDifficulty.Lo - lastTotalHashes
		}
	} else {
		// Fallback: estimate based on time since last found
		// roundHashes = hashrate * time_since_last_found
		hashrate := d.redis.Get(ctx, "stats:pool:hashrate").Val()
		var hr uint64
		fmt.Sscanf(hashrate, "%d", &hr)
		timeSince := time.Now().Unix() - lastFound.Timestamp
		if timeSince > 0 && hr > 0 {
			roundHashes = hr * uint64(timeSince)
		}
	}

	// Calculate effort percentage
	// effort = (roundHashes * 100) / mainchain_difficulty
	var currentEffort float64
	if d.mainchainDiff.Lo > 0 {
		currentEffort = float64(roundHashes) * 100.0 / float64(d.mainchainDiff.Lo)
	}

	effort := EffortStats{
		CurrentEffort:  currentEffort,
		RoundHashes:    roundHashes,
		LastFoundTime:  lastFound.Timestamp,
		LastFoundBlock: lastFound.Height,
		TotalFound:     len(d.foundBlocks),
		UpdatedAt:      time.Now().Unix(),
	}

	effortJSON, _ := json.Marshal(effort)
	d.redis.Set(ctx, "stats:pool:effort", string(effortJSON), 0)

	p2pool.Logf("REDIS", "Pool effort: %.2f%% (round hashes: %d, last found: height %d)",
		currentEffort, roundHashes, lastFound.Height)
}

// calculatePayouts calculates and stores miner payouts for found blocks.
// Uses actual on-chain coinbase outputs for precision.
// IMPORTANT: Only calculates payouts for blocks within the current PPLNS window.
// Blocks outside the window cannot have accurate payouts calculated because the
// PPLNS state at the time they were found is no longer available.
func (d *Daemon) calculatePayouts(pplns *p2pool.PPLNSResult) {
	ctx := d.ctx

	if len(d.foundBlocks) == 0 || pplns == nil {
		return
	}

	// Load existing processed payouts from Redis on first run
	if len(d.processedPayouts) == 0 {
		d.loadProcessedPayouts()
	}

	// Load existing miner payouts from Redis on first run
	if len(d.minerPayouts) == 0 {
		d.loadMinerPayouts()
	}

	newPayouts := 0

	// Process each found block
	for _, fb := range d.foundBlocks {
		// Skip if already processed
		if d.processedPayouts[fb.Height] {
			continue
		}

		// Only calculate payouts for blocks found within the current PPLNS window.
		// The PPLNS window typically covers the last ~2160 sidechain blocks.
		// If a found block is older than the bottom of the current window,
		// we cannot accurately calculate payouts because the PPLNS has changed.
		// Mark these as processed but don't calculate payouts (avoids incorrect data).
		if fb.Timestamp > 0 && pplns.BottomHeight > 0 {
			// Estimate: PPLNS window covers roughly 6 hours (2160 blocks * 10s)
			// If block is older than ~6 hours, skip payout calculation
			windowDuration := int64(6 * 60 * 60) // 6 hours in seconds
			currentTime := time.Now().Unix()
			if fb.Timestamp < currentTime-windowDuration {
				p2pool.Logf("PAYOUT", "Block %d is outside PPLNS window (age: %d hours), skipping payout calculation",
					fb.Height, (currentTime-fb.Timestamp)/3600)
				d.processedPayouts[fb.Height] = true
				continue
			}
		}

		// Fetch actual coinbase outputs from Salvium for precision
		outputs, err := d.salvium.GetCoinbaseOutputs(ctx, fb.Height)
		if err != nil {
			p2pool.Errorf("PAYOUT", "Block %d: failed to get coinbase outputs: %v", fb.Height, err)
			// Mark as processed to avoid retrying every cycle
			d.processedPayouts[fb.Height] = true
			continue
		}

		if len(outputs) == 0 {
			p2pool.Errorf("PAYOUT", "Block %d: no coinbase outputs found", fb.Height)
			d.processedPayouts[fb.Height] = true
			continue
		}

		// Get ordered list of PPLNS miners (same order as coinbase outputs)
		miners := pplns.GetOrderedMiners()

		// Calculate total reward for percentage calculation
		var totalReward uint64
		for _, out := range outputs {
			totalReward += out
		}

		// Match outputs to miners
		minCount := len(miners)
		if len(outputs) < minCount {
			minCount = len(outputs)
		}

		for i := 0; i < minCount; i++ {
			amount := outputs[i]
			percentage := 0.0
			if totalReward > 0 {
				percentage = float64(amount) / float64(totalReward) * 100.0
			}

			payout := Payout{
				BlockHeight: fb.Height,
				BlockHash:   fb.Hash,
				Amount:      amount,
				Percentage:  percentage,
				Timestamp:   fb.Timestamp,
			}

			// Add to miner's payout list
			addrStr := miners[i].ToBase58()
			d.minerPayouts[addrStr] = append(d.minerPayouts[addrStr], payout)
		}

		p2pool.Logf("PAYOUT", "Block %d: %d payouts from on-chain data (total: %.4f SAL)",
			fb.Height, minCount, float64(totalReward)/1e8)
		d.processedPayouts[fb.Height] = true
		newPayouts++
	}

	if newPayouts > 0 {
		// Store miner payouts to Redis
		for addr, payouts := range d.minerPayouts {
			key := fmt.Sprintf("miner:%s:payouts", addr)
			payoutsJSON, _ := json.Marshal(payouts)
			d.redis.Set(ctx, key, string(payoutsJSON), 0)
		}

		// Store processed block heights
		processedJSON, _ := json.Marshal(d.processedPayouts)
		d.redis.Set(ctx, "cache:processed_payouts", string(processedJSON), 0)

		p2pool.Logf("REDIS", "Calculated payouts for %d new blocks, %d miners", newPayouts, len(d.minerPayouts))
	}
}

// loadProcessedPayouts loads the set of already-processed payout blocks from Redis
func (d *Daemon) loadProcessedPayouts() {
	ctx := d.ctx

	data, err := d.redis.Get(ctx, "cache:processed_payouts").Result()
	if err != nil {
		return
	}

	json.Unmarshal([]byte(data), &d.processedPayouts)
}

// loadMinerPayouts loads existing miner payouts from Redis
func (d *Daemon) loadMinerPayouts() {
	ctx := d.ctx

	// Get all miner payout keys
	keys, err := d.redis.Keys(ctx, "miner:*:payouts").Result()
	if err != nil {
		return
	}

	for _, key := range keys {
		data, err := d.redis.Get(ctx, key).Result()
		if err != nil {
			continue
		}

		// Extract address from key (miner:{addr}:payouts)
		parts := make([]string, 0)
		for _, p := range []byte(key) {
			if p == ':' {
				parts = append(parts, "")
			} else if len(parts) > 0 {
				parts[len(parts)-1] += string(p)
			}
		}
		if len(parts) < 2 {
			continue
		}
		addr := parts[0]

		var payouts []Payout
		if json.Unmarshal([]byte(data), &payouts) == nil {
			d.minerPayouts[addr] = payouts
		}
	}

	p2pool.Logf("REDIS", "Loaded payouts for %d miners", len(d.minerPayouts))
}

// rebuildPayoutsFromChain rebuilds payout data for a found block using on-chain data.
// This fetches actual coinbase outputs from Salvium and matches them to PPLNS miners.
func (d *Daemon) rebuildPayoutsFromChain(fb FoundBlock) ([]Payout, error) {
	// 1. Find the sidechain block that was the tip at this mainchain height
	tipShare := d.findShareByMainHeight(fb.Height)
	if tipShare == nil {
		return nil, fmt.Errorf("no sidechain block found for main height %d", fb.Height)
	}

	// 2. Rebuild PPLNS from that sidechain block
	pplns := p2pool.CalculatePPLNS(d.shares, tipShare, d.mainchainDiff)
	if pplns == nil || len(pplns.Shares) == 0 {
		return nil, fmt.Errorf("failed to calculate PPLNS for block %d", fb.Height)
	}

	// 3. Fetch actual coinbase outputs from Salvium RPC
	outputs, err := d.salvium.GetCoinbaseOutputs(d.ctx, fb.Height)
	if err != nil {
		return nil, fmt.Errorf("failed to get coinbase outputs: %w", err)
	}

	if len(outputs) == 0 {
		return nil, fmt.Errorf("no coinbase outputs found for block %d", fb.Height)
	}

	// 4. Get ordered list of PPLNS miners (same order as coinbase outputs)
	miners := pplns.GetOrderedMiners()
	if len(miners) != len(outputs) {
		p2pool.Logf("PAYOUT", "Warning: miner count %d != output count %d for block %d",
			len(miners), len(outputs), fb.Height)
		// Use minimum of both to avoid index out of range
		if len(miners) > len(outputs) {
			miners = miners[:len(outputs)]
		}
	}

	// 5. Match outputs to miners
	payouts := make([]Payout, 0, len(miners))
	var totalReward uint64
	for _, out := range outputs {
		totalReward += out
	}

	for i, miner := range miners {
		if i >= len(outputs) {
			break
		}
		amount := outputs[i]
		percentage := 0.0
		if totalReward > 0 {
			percentage = float64(amount) / float64(totalReward) * 100.0
		}

		payouts = append(payouts, Payout{
			BlockHeight: fb.Height,
			BlockHash:   fb.Hash,
			Amount:      amount,
			Percentage:  percentage,
			Timestamp:   fb.Timestamp,
		})

		// Store the payout for this miner
		addrStr := miner.ToBase58()
		d.minerPayouts[addrStr] = append(d.minerPayouts[addrStr], payouts[len(payouts)-1])
	}

	return payouts, nil
}

// findShareByMainHeight finds the sidechain block that was the tip at the given mainchain height.
// It looks for the share with the matching MainchainHeight and highest SidechainHeight.
func (d *Daemon) findShareByMainHeight(mainHeight uint64) *p2pool.Share {
	var best *p2pool.Share

	for _, share := range d.shares {
		// Find shares at or before this mainchain height
		if share.MainchainHeight == mainHeight {
			if best == nil || share.SidechainHeight > best.SidechainHeight {
				best = share
			}
		}
	}

	// If exact match not found, find closest lower mainchain height
	if best == nil {
		var closestHeight uint64
		for _, share := range d.shares {
			if share.MainchainHeight < mainHeight && share.MainchainHeight > closestHeight {
				closestHeight = share.MainchainHeight
				best = share
			} else if share.MainchainHeight == closestHeight && share.SidechainHeight > best.SidechainHeight {
				best = share
			}
		}
	}

	return best
}

// RebuildAllPayouts rebuilds payouts for all found blocks using on-chain data.
// This is useful for data recovery after downtime or cache issues.
func (d *Daemon) RebuildAllPayouts() error {
	p2pool.Logf("REBUILD", "Starting payout rebuild for %d found blocks", len(d.foundBlocks))

	// Clear existing payout data
	d.minerPayouts = make(map[string][]Payout)
	d.processedPayouts = make(map[uint64]bool)

	successCount := 0
	errorCount := 0

	for _, fb := range d.foundBlocks {
		payouts, err := d.rebuildPayoutsFromChain(fb)
		if err != nil {
			p2pool.Errorf("REBUILD", "Block %d: %v", fb.Height, err)
			errorCount++
			continue
		}

		d.processedPayouts[fb.Height] = true
		successCount++
		p2pool.Logf("REBUILD", "Block %d: rebuilt %d payouts", fb.Height, len(payouts))
	}

	// Store rebuilt payouts to Redis
	ctx := d.ctx
	for addr, payouts := range d.minerPayouts {
		key := fmt.Sprintf("miner:%s:payouts", addr)
		payoutsJSON, _ := json.Marshal(payouts)
		d.redis.Set(ctx, key, string(payoutsJSON), 0)
	}

	processedJSON, _ := json.Marshal(d.processedPayouts)
	d.redis.Set(ctx, "cache:processed_payouts", string(processedJSON), 0)

	p2pool.Logf("REBUILD", "Complete: %d successful, %d errors, %d miners",
		successCount, errorCount, len(d.minerPayouts))

	return nil
}
