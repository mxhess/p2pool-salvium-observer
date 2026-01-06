// Package main provides a clean REST API for the P2Pool Salvium observer.
// It reads all data from Redis (written by the daemon) and serves JSON endpoints.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"git.gammaspectra.live/P2Pool/observer/p2pool"
	"github.com/gorilla/mux"
	"github.com/redis/go-redis/v9"
)

// Salvium address regex: SC1 followed by 90-150 base58 characters
// Standard addresses are ~95 chars after SC1, integrated addresses are longer
// Using wider range to catch edge cases
var salviumAddrRegex = regexp.MustCompile(`\b(SC1[1-9A-HJ-NP-Za-km-z]{90,150})\b`)

// API server
type API struct {
	redis  *redis.Client
	router *mux.Router
}

func main() {
	// Command line flags
	listenAddr := flag.String("listen", ":8080", "HTTP listen address")
	redisAddr := flag.String("redis", "127.0.0.1:6379", "Redis address")
	flag.Parse()

	p2pool.Logf("API", "P2Pool Salvium Observer API (clean)")
	p2pool.Logf("API", "Listen: %s", *listenAddr)
	p2pool.Logf("API", "Redis: %s", *redisAddr)

	// Setup context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

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
		p2pool.Errorf("API", "Failed to connect to Redis: %v", err)
		os.Exit(1)
	}
	p2pool.Logf("API", "Connected to Redis")

	// Start cache cleanup goroutine
	startCacheCleanup(30 * time.Second)

	// Create API
	api := &API{
		redis:  redisClient,
		router: mux.NewRouter(),
	}

	// Register routes
	api.registerRoutes()

	// Create HTTP server
	server := &http.Server{
		Addr:         *listenAddr,
		Handler:      api.router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	// Start server in goroutine
	go func() {
		p2pool.Logf("API", "Starting HTTP server on %s", *listenAddr)
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			p2pool.Errorf("API", "HTTP server error: %v", err)
		}
	}()

	// Wait for shutdown signal
	<-sigChan
	p2pool.Logf("API", "Shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	server.Shutdown(shutdownCtx)

	p2pool.Logf("API", "Shutdown complete")
}

func (a *API) registerRoutes() {
	// CORS middleware
	a.router.Use(corsMiddleware)

	// Health check
	a.router.HandleFunc("/api/health", a.handleHealth).Methods("GET")

	// Pool info (main endpoint)
	a.router.HandleFunc("/api/pool_info", a.handlePoolInfo).Methods("GET")

	// Network stats
	a.router.HandleFunc("/api/network/stats", a.handleNetworkStats).Methods("GET")

	// Pool stats
	a.router.HandleFunc("/api/pool/stats", a.handlePoolStats).Methods("GET")

	// Found blocks
	a.router.HandleFunc("/api/found_blocks", a.handleFoundBlocks).Methods("GET")
	a.router.HandleFunc("/api/pool/blocks", a.handleFoundBlocks).Methods("GET") // Alias

	// PPLNS window
	a.router.HandleFunc("/api/pplns", a.handlePPLNS).Methods("GET")
	a.router.HandleFunc("/api/side_blocks_in_window", a.handlePPLNS).Methods("GET") // Alias

	// Miner info (GET with address in path - may be blocked by ad blockers)
	a.router.HandleFunc("/api/miner_info/{miner}", a.handleMinerInfo).Methods("GET")
	a.router.HandleFunc("/api/miner/{miner}/shares", a.handleMinerShares).Methods("GET")

	// Miner info (POST with address in body - ad blocker safe, generic names)
	a.router.HandleFunc("/api/lookup/info", a.handleMinerInfoPOST).Methods("POST")
	a.router.HandleFunc("/api/lookup/shares", a.handleMinerSharesPOST).Methods("POST")
	a.router.HandleFunc("/api/lookup/history", a.handlePayoutsPOST).Methods("POST")

	// Share/block lookups
	a.router.HandleFunc("/api/share/{id}", a.handleShare).Methods("GET")
	a.router.HandleFunc("/api/block_by_id/{id}", a.handleShare).Methods("GET") // Alias
	a.router.HandleFunc("/api/block_by_height/{height}", a.handleBlockByHeight).Methods("GET")

	// Recent shares (side_blocks)
	a.router.HandleFunc("/api/side_blocks", a.handleSideBlocks).Methods("GET")

	// Current tip
	a.router.HandleFunc("/api/redirect/tip", a.handleTip).Methods("GET")

	// Payouts
	a.router.HandleFunc("/api/payouts/{miner}", a.handlePayouts).Methods("GET")

	// Effort stats
	a.router.HandleFunc("/api/pool/effort", a.handleEffort).Methods("GET")

	// Found block details (with PPLNS snapshot and payouts)
	a.router.HandleFunc("/api/found_block/{height}", a.handleFoundBlockDetails).Methods("GET")

	// Stats endpoint (cryptonote-nodejs-pool compatible format)
	a.router.HandleFunc("/api/stats", a.handleStats).Methods("GET")
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	// Serialize, sanitize addresses, then write
	data, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Write(sanitizeAddresses(data))
}

// sanitizeAddresses truncates all Salvium addresses in JSON data for privacy
// Full addresses are NEVER exposed publicly - only <8 chars>...<8 chars> format
func sanitizeAddresses(data []byte) []byte {
	return salviumAddrRegex.ReplaceAllFunc(data, func(match []byte) []byte {
		addr := string(match)
		if len(addr) > 19 {
			return []byte(addr[:8] + "..." + addr[len(addr)-8:])
		}
		return match
	})
}

// cachedRedisGet retrieves a value from cache or Redis, caching the result
func (a *API) cachedRedisGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	cacheKey := "redis:" + key

	if data, ok := cacheGet(cacheKey); ok {
		return string(data), nil
	}

	result, err := a.redis.Get(ctx, key).Result()
	if err != nil {
		return "", err
	}

	cacheSet(cacheKey, []byte(result), ttl)
	return result, nil
}

func (a *API) writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// GET /api/health
func (a *API) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := a.redis.Ping(ctx).Err(); err != nil {
		a.writeError(w, http.StatusServiceUnavailable, "Redis unavailable")
		return
	}
	a.writeJSON(w, map[string]string{"status": "ok"})
}

// writeSanitizedCache writes cached data with address sanitization
func (a *API) writeSanitizedCache(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache", "HIT")
	w.Write(sanitizeAddresses(data))
}

// GET /api/pool_info - Main pool information endpoint (cached 10s)
func (a *API) handlePoolInfo(w http.ResponseWriter, r *http.Request) {
	cacheKey := "api:pool_info"

	// Check cache first
	if data, ok := cacheGet(cacheKey); ok {
		a.writeSanitizedCache(w, data)
		return
	}

	ctx := r.Context()

	// Gather all stats
	result := make(map[string]interface{})

	// Sidechain info
	result["sidechain_id"] = "default"
	if height, err := a.redis.Get(ctx, "stats:sidechain:height").Uint64(); err == nil {
		result["sidechain_height"] = height
	}
	if diff, err := a.redis.Get(ctx, "stats:sidechain:difficulty").Uint64(); err == nil {
		result["sidechain_difficulty"] = diff
	}

	// Pool stats
	if hashrate, err := a.redis.Get(ctx, "stats:pool:hashrate").Uint64(); err == nil {
		result["pool_hashrate"] = hashrate
	}
	if miners, err := a.redis.Get(ctx, "stats:pool:miners").Int(); err == nil {
		result["miners"] = miners
	}
	if found, err := a.redis.Get(ctx, "stats:pool:blocks_found").Int(); err == nil {
		result["blocks_found"] = found
	}

	// Network stats
	if height, err := a.redis.Get(ctx, "stats:network:height").Uint64(); err == nil {
		result["mainchain_height"] = height
	}
	if diff, err := a.redis.Get(ctx, "stats:network:difficulty").Uint64(); err == nil {
		result["mainchain_difficulty"] = diff
	}
	if reward, err := a.redis.Get(ctx, "stats:network:reward").Uint64(); err == nil {
		result["block_reward"] = reward
	}
	if txPoolSize, err := a.redis.Get(ctx, "stats:network:tx_pool_size").Uint64(); err == nil {
		result["tx_pool_size"] = txPoolSize
	}
	if supply, err := a.redis.Get(ctx, "stats:network:supply").Uint64(); err == nil {
		result["total_supply"] = supply
	}
	if circulating, err := a.redis.Get(ctx, "stats:network:circulating").Uint64(); err == nil {
		result["circulating_supply"] = circulating
	}
	if staked, err := a.redis.Get(ctx, "stats:network:staked").Uint64(); err == nil {
		result["staked_supply"] = staked
	}
	if burned, err := a.redis.Get(ctx, "stats:network:burned").Uint64(); err == nil {
		result["burned_supply"] = burned
	}

	// PPLNS window info
	var pplnsWindow map[string]interface{}
	if data, err := a.redis.Get(ctx, "cache:pplns:window").Result(); err == nil {
		json.Unmarshal([]byte(data), &pplnsWindow)
		if pplnsWindow != nil {
			result["pplns_window"] = pplnsWindow
		}
	}

	// Effort
	var effort map[string]interface{}
	if data, err := a.redis.Get(ctx, "stats:pool:effort").Result(); err == nil {
		json.Unmarshal([]byte(data), &effort)
		if effort != nil {
			result["effort"] = effort
		}
	}

	// Last found
	if lastTime, err := a.redis.Get(ctx, "stats:pool:last_found_time").Int64(); err == nil {
		result["last_block_found_time"] = lastTime
	}
	if lastHeight, err := a.redis.Get(ctx, "stats:pool:last_found_height").Uint64(); err == nil {
		result["last_block_found"] = lastHeight
	}

	// Cache the response
	if jsonData, err := json.Marshal(result); err == nil {
		cacheSet(cacheKey, jsonData, CacheTTLShort)
	}

	w.Header().Set("X-Cache", "MISS")
	a.writeJSON(w, result)
}

// GET /api/network/stats (cached 10s)
func (a *API) handleNetworkStats(w http.ResponseWriter, r *http.Request) {
	cacheKey := "api:network:stats"

	if data, ok := cacheGet(cacheKey); ok {
		a.writeSanitizedCache(w, data)
		return
	}

	ctx := r.Context()

	data, err := a.redis.Get(ctx, "stats:network").Result()
	if err != nil {
		a.writeError(w, http.StatusNotFound, "Network stats not available")
		return
	}

	var stats map[string]interface{}
	if err := json.Unmarshal([]byte(data), &stats); err != nil {
		a.writeError(w, http.StatusInternalServerError, "Invalid stats data")
		return
	}

	if jsonData, err := json.Marshal(stats); err == nil {
		cacheSet(cacheKey, jsonData, CacheTTLShort)
	}

	w.Header().Set("X-Cache", "MISS")
	a.writeJSON(w, stats)
}

// GET /api/pool/stats (cached 10s)
func (a *API) handlePoolStats(w http.ResponseWriter, r *http.Request) {
	cacheKey := "api:pool:stats"

	if data, ok := cacheGet(cacheKey); ok {
		a.writeSanitizedCache(w, data)
		return
	}

	ctx := r.Context()

	data, err := a.redis.Get(ctx, "stats:pool").Result()
	if err != nil {
		a.writeError(w, http.StatusNotFound, "Pool stats not available")
		return
	}

	var stats map[string]interface{}
	if err := json.Unmarshal([]byte(data), &stats); err != nil {
		a.writeError(w, http.StatusInternalServerError, "Invalid stats data")
		return
	}

	// Add hashrate and effort
	if hashrate, err := a.redis.Get(ctx, "stats:pool:hashrate").Uint64(); err == nil {
		stats["hashrate"] = hashrate
	}
	if effortData, err := a.redis.Get(ctx, "stats:pool:effort").Result(); err == nil {
		var effort map[string]interface{}
		json.Unmarshal([]byte(effortData), &effort)
		stats["effort"] = effort
	}

	if jsonData, err := json.Marshal(stats); err == nil {
		cacheSet(cacheKey, jsonData, CacheTTLShort)
	}

	w.Header().Set("X-Cache", "MISS")
	a.writeJSON(w, stats)
}

// GET /api/found_blocks (cached 10s, or 60s for limit>=100)
func (a *API) handleFoundBlocks(w http.ResponseWriter, r *http.Request) {
	// Determine cache TTL based on limit parameter
	limit := r.URL.Query().Get("limit")
	ttl := CacheTTLShort // 10s default
	if limit != "" {
		if n, err := strconv.Atoi(limit); err == nil && n >= 100 {
			ttl = CacheTTLMedium // 60s for larger requests
		}
	}

	cacheKey := "api:found_blocks:" + limit

	if data, ok := cacheGet(cacheKey); ok {
		a.writeSanitizedCache(w, data)
		return
	}

	ctx := r.Context()

	data, err := a.redis.Get(ctx, "cache:found_blocks").Result()
	if err != nil {
		a.writeJSON(w, []interface{}{}) // Empty array
		return
	}

	var blocks []interface{}
	if err := json.Unmarshal([]byte(data), &blocks); err != nil {
		a.writeError(w, http.StatusInternalServerError, "Invalid found blocks data")
		return
	}

	// Apply limit if specified - take the LAST n blocks (newest first)
	if limit != "" {
		if n, err := strconv.Atoi(limit); err == nil && n > 0 && n < len(blocks) {
			blocks = blocks[len(blocks)-n:]
		}
	}

	if jsonData, err := json.Marshal(blocks); err == nil {
		cacheSet(cacheKey, jsonData, ttl)
	}

	w.Header().Set("X-Cache", "MISS")
	a.writeJSON(w, blocks)
}

// GET /api/pplns (cached 120s - PPLNS window data)
func (a *API) handlePPLNS(w http.ResponseWriter, r *http.Request) {
	cacheKey := "api:pplns"

	if data, ok := cacheGet(cacheKey); ok {
		a.writeSanitizedCache(w, data)
		return
	}

	ctx := r.Context()

	data, err := a.redis.Get(ctx, "cache:pplns:full").Result()
	if err != nil {
		a.writeError(w, http.StatusNotFound, "PPLNS data not available")
		return
	}

	var pplns map[string]interface{}
	if err := json.Unmarshal([]byte(data), &pplns); err != nil {
		a.writeError(w, http.StatusInternalServerError, "Invalid PPLNS data")
		return
	}

	cacheSet(cacheKey, []byte(data), CacheTTLLong)

	w.Header().Set("X-Cache", "MISS")
	a.writeJSON(w, pplns)
}

// GET /api/miner_info/{miner} (cached 10s)
func (a *API) handleMinerInfo(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	minerAddr := vars["miner"]

	if minerAddr == "" {
		a.writeError(w, http.StatusBadRequest, "Miner address required")
		return
	}

	cacheKey := "api:miner_info:" + minerAddr

	if data, ok := cacheGet(cacheKey); ok {
		a.writeSanitizedCache(w, data)
		return
	}

	ctx := r.Context()

	result := make(map[string]interface{})
	result["address"] = p2pool.TruncateAddress(minerAddr)

	// Get miner's PPLNS data
	key := fmt.Sprintf("cache:pplns:miner:%s", minerAddr)
	if data, err := a.redis.Get(ctx, key).Result(); err == nil {
		var minerData map[string]interface{}
		if json.Unmarshal([]byte(data), &minerData) == nil {
			result["pplns"] = minerData
		}
	}

	// Get miner's shares
	sharesKey := fmt.Sprintf("miner:%s:shares", minerAddr)
	if data, err := a.redis.Get(ctx, sharesKey).Result(); err == nil {
		var shares []string
		if json.Unmarshal([]byte(data), &shares) == nil {
			result["shares_count"] = len(shares)
			result["recent_shares"] = shares
		}
	}

	// Get alias if set
	aliasKey := fmt.Sprintf("miner:%s:alias", minerAddr)
	if alias, err := a.redis.Get(ctx, aliasKey).Result(); err == nil {
		result["alias"] = alias
	}

	if jsonData, err := json.Marshal(result); err == nil {
		cacheSet(cacheKey, jsonData, CacheTTLShort)
	}

	w.Header().Set("X-Cache", "MISS")
	a.writeJSON(w, result)
}

// GET /api/miner/{miner}/shares (cached 10s)
func (a *API) handleMinerShares(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	minerAddr := vars["miner"]

	cacheKey := "api:miner_shares:" + minerAddr

	if data, ok := cacheGet(cacheKey); ok {
		a.writeSanitizedCache(w, data)
		return
	}

	ctx := r.Context()

	key := fmt.Sprintf("miner:%s:shares", minerAddr)
	data, err := a.redis.Get(ctx, key).Result()
	if err != nil {
		a.writeJSON(w, []string{})
		return
	}

	var shares []string
	if err := json.Unmarshal([]byte(data), &shares); err != nil {
		a.writeError(w, http.StatusInternalServerError, "Invalid shares data")
		return
	}

	cacheSet(cacheKey, []byte(data), CacheTTLShort)

	w.Header().Set("X-Cache", "MISS")
	a.writeJSON(w, shares)
}

// MinerRequest is the JSON body for POST miner endpoints
type MinerRequest struct {
	Address string `json:"address"`
}

// POST /api/miner_info - Miner info with address in body (ad blocker safe)
func (a *API) handleMinerInfoPOST(w http.ResponseWriter, r *http.Request) {
	var req MinerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		a.writeError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Address == "" {
		a.writeError(w, http.StatusBadRequest, "Address required")
		return
	}

	// Reuse the same logic as the GET handler
	minerAddr := req.Address
	cacheKey := "api:miner_info:" + minerAddr

	if data, ok := cacheGet(cacheKey); ok {
		a.writeSanitizedCache(w, data)
		return
	}

	ctx := r.Context()

	result := make(map[string]interface{})
	result["address"] = p2pool.TruncateAddress(minerAddr)

	// Get miner's PPLNS data
	key := fmt.Sprintf("cache:pplns:miner:%s", minerAddr)
	if data, err := a.redis.Get(ctx, key).Result(); err == nil {
		var minerData map[string]interface{}
		if json.Unmarshal([]byte(data), &minerData) == nil {
			result["pplns"] = minerData
		}
	}

	// Get miner's shares
	sharesKey := fmt.Sprintf("miner:%s:shares", minerAddr)
	if data, err := a.redis.Get(ctx, sharesKey).Result(); err == nil {
		var shares []string
		if json.Unmarshal([]byte(data), &shares) == nil {
			result["shares_count"] = len(shares)
			result["recent_shares"] = shares
		}
	}

	// Get alias if set
	aliasKey := fmt.Sprintf("miner:%s:alias", minerAddr)
	if alias, err := a.redis.Get(ctx, aliasKey).Result(); err == nil {
		result["alias"] = alias
	}

	if jsonData, err := json.Marshal(result); err == nil {
		cacheSet(cacheKey, jsonData, CacheTTLShort)
	}

	w.Header().Set("X-Cache", "MISS")
	a.writeJSON(w, result)
}

// POST /api/miner_shares - Miner shares with address in body (ad blocker safe)
func (a *API) handleMinerSharesPOST(w http.ResponseWriter, r *http.Request) {
	var req MinerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		a.writeError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Address == "" {
		a.writeError(w, http.StatusBadRequest, "Address required")
		return
	}

	minerAddr := req.Address
	cacheKey := "api:miner_shares:" + minerAddr

	if data, ok := cacheGet(cacheKey); ok {
		a.writeSanitizedCache(w, data)
		return
	}

	ctx := r.Context()

	key := fmt.Sprintf("miner:%s:shares", minerAddr)
	data, err := a.redis.Get(ctx, key).Result()
	if err != nil {
		a.writeJSON(w, []string{})
		return
	}

	var shares []string
	if err := json.Unmarshal([]byte(data), &shares); err != nil {
		a.writeError(w, http.StatusInternalServerError, "Invalid shares data")
		return
	}

	cacheSet(cacheKey, []byte(data), CacheTTLShort)

	w.Header().Set("X-Cache", "MISS")
	a.writeJSON(w, shares)
}

// POST /api/miner_payouts - Miner payouts with address in body (ad blocker safe)
func (a *API) handlePayoutsPOST(w http.ResponseWriter, r *http.Request) {
	var req MinerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		a.writeError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Address == "" {
		a.writeError(w, http.StatusBadRequest, "Address required")
		return
	}

	miner := req.Address
	cacheKey := "api:payouts:" + miner

	if data, ok := cacheGet(cacheKey); ok {
		a.writeSanitizedCache(w, data)
		return
	}

	ctx := r.Context()

	// Try multiple key formats for backwards compatibility
	keysToTry := []string{
		fmt.Sprintf("miner:%s:payouts", miner),                         // Full address
		fmt.Sprintf("miner:%s:payouts", p2pool.TruncateAddress(miner)), // New truncated (8...8)
	}

	var data string
	var err error
	for _, key := range keysToTry {
		data, err = a.redis.Get(ctx, key).Result()
		if err == nil {
			break
		}
	}

	if err != nil {
		// Return empty array if not found
		a.writeJSON(w, []interface{}{})
		return
	}

	cacheSet(cacheKey, []byte(data), CacheTTLShort)

	w.Header().Set("X-Cache", "MISS")
	w.Header().Set("Content-Type", "application/json")
	w.Write(sanitizeAddresses([]byte(data)))
}

// GET /api/share/{id} (cached 10s)
func (a *API) handleShare(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	shareId := vars["id"]

	// Normalize ID (lowercase, no 0x prefix)
	shareId = strings.ToLower(strings.TrimPrefix(shareId, "0x"))

	cacheKey := "api:share:" + shareId

	if data, ok := cacheGet(cacheKey); ok {
		a.writeSanitizedCache(w, data)
		return
	}

	ctx := r.Context()

	key := fmt.Sprintf("share:data:%s", shareId)
	data, err := a.redis.Get(ctx, key).Result()
	if err != nil {
		a.writeError(w, http.StatusNotFound, "Share not found")
		return
	}

	var share map[string]interface{}
	if err := json.Unmarshal([]byte(data), &share); err != nil {
		a.writeError(w, http.StatusInternalServerError, "Invalid share data")
		return
	}

	cacheSet(cacheKey, []byte(data), CacheTTLShort)

	w.Header().Set("X-Cache", "MISS")
	a.writeJSON(w, share)
}

// GET /api/block_by_height/{height} (cached 10s)
func (a *API) handleBlockByHeight(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	heightStr := vars["height"]

	cacheKey := "api:block_height:" + heightStr

	if data, ok := cacheGet(cacheKey); ok {
		a.writeSanitizedCache(w, data)
		return
	}

	ctx := r.Context()

	height, err := strconv.ParseUint(heightStr, 10, 64)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, "Invalid height")
		return
	}

	// Get template ID for this height
	heightKey := fmt.Sprintf("sideblock:height:%d", height)
	templateId, err := a.redis.Get(ctx, heightKey).Result()
	if err != nil {
		a.writeError(w, http.StatusNotFound, "Block not found at height")
		return
	}

	// Get share data
	shareKey := fmt.Sprintf("share:data:%s", templateId)
	data, err := a.redis.Get(ctx, shareKey).Result()
	if err != nil {
		a.writeError(w, http.StatusNotFound, "Share data not found")
		return
	}

	var share map[string]interface{}
	if err := json.Unmarshal([]byte(data), &share); err != nil {
		a.writeError(w, http.StatusInternalServerError, "Invalid share data")
		return
	}

	cacheSet(cacheKey, []byte(data), CacheTTLShort)

	w.Header().Set("X-Cache", "MISS")
	a.writeJSON(w, share)
}

// GET /api/pool/effort (cached 10s)
func (a *API) handleEffort(w http.ResponseWriter, r *http.Request) {
	cacheKey := "api:pool:effort"

	if data, ok := cacheGet(cacheKey); ok {
		a.writeSanitizedCache(w, data)
		return
	}

	ctx := r.Context()

	data, err := a.redis.Get(ctx, "stats:pool:effort").Result()
	if err != nil {
		a.writeError(w, http.StatusNotFound, "Effort data not available")
		return
	}

	var effort map[string]interface{}
	if err := json.Unmarshal([]byte(data), &effort); err != nil {
		a.writeError(w, http.StatusInternalServerError, "Invalid effort data")
		return
	}

	cacheSet(cacheKey, []byte(data), CacheTTLShort)

	w.Header().Set("X-Cache", "MISS")
	a.writeJSON(w, effort)
}

// GET /api/side_blocks - Recent shares list (cached 10s)
func (a *API) handleSideBlocks(w http.ResponseWriter, r *http.Request) {
	limit := r.URL.Query().Get("limit")
	if limit == "" {
		limit = "50"
	}

	cacheKey := "api:side_blocks:" + limit

	if data, ok := cacheGet(cacheKey); ok {
		a.writeSanitizedCache(w, data)
		return
	}

	ctx := r.Context()

	// Get recent shares from cache
	data, err := a.redis.Get(ctx, "cache:recent_shares").Result()
	if err != nil {
		// Fallback: return empty array
		a.writeJSON(w, []interface{}{})
		return
	}

	var shares []interface{}
	if err := json.Unmarshal([]byte(data), &shares); err != nil {
		a.writeError(w, http.StatusInternalServerError, "Invalid shares data")
		return
	}

	// Apply limit
	if n, err := strconv.Atoi(limit); err == nil && n > 0 && n < len(shares) {
		shares = shares[:n]
	}

	if jsonData, err := json.Marshal(shares); err == nil {
		cacheSet(cacheKey, jsonData, CacheTTLShort)
	}

	w.Header().Set("X-Cache", "MISS")
	a.writeJSON(w, shares)
}

// GET /api/redirect/tip - Current sidechain tip (cached 10s)
func (a *API) handleTip(w http.ResponseWriter, r *http.Request) {
	cacheKey := "api:tip"

	if data, ok := cacheGet(cacheKey); ok {
		a.writeSanitizedCache(w, data)
		return
	}

	ctx := r.Context()

	// Get tip share data
	data, err := a.redis.Get(ctx, "cache:tip").Result()
	if err != nil {
		a.writeError(w, http.StatusNotFound, "Tip data not available")
		return
	}

	var tip map[string]interface{}
	if err := json.Unmarshal([]byte(data), &tip); err != nil {
		a.writeError(w, http.StatusInternalServerError, "Invalid tip data")
		return
	}

	cacheSet(cacheKey, []byte(data), CacheTTLShort)

	w.Header().Set("X-Cache", "MISS")
	a.writeJSON(w, tip)
}

// GET /api/payouts/{miner} - Miner payout history (cached 10s)
func (a *API) handlePayouts(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	miner := vars["miner"]

	cacheKey := "api:payouts:" + miner

	if data, ok := cacheGet(cacheKey); ok {
		a.writeSanitizedCache(w, data)
		return
	}

	ctx := r.Context()

	// Try multiple key formats for backwards compatibility
	keysToTry := []string{
		fmt.Sprintf("miner:%s:payouts", miner), // Full address
		fmt.Sprintf("miner:%s:payouts", p2pool.TruncateAddress(miner)), // New truncated (8...8)
	}


	var data string
	var err error
	for _, key := range keysToTry {
		data, err = a.redis.Get(ctx, key).Result()
		if err == nil {
			break
		}
	}

	if err != nil {
		// Return empty array if not found
		a.writeJSON(w, []interface{}{})
		return
	}

	cacheSet(cacheKey, []byte(data), CacheTTLShort)

	w.Header().Set("X-Cache", "MISS")
	w.Header().Set("Content-Type", "application/json")
	w.Write(sanitizeAddresses([]byte(data)))
}

// GET /api/found_block/{height} - Found block details with PPLNS snapshot (cached 60s)
func (a *API) handleFoundBlockDetails(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	heightStr := vars["height"]

	height, err := strconv.ParseUint(heightStr, 10, 64)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, "Invalid block height")
		return
	}

	cacheKey := fmt.Sprintf("api:found_block:%d", height)

	if data, ok := cacheGet(cacheKey); ok {
		a.writeSanitizedCache(w, data)
		return
	}

	ctx := r.Context()

	// Get all found blocks
	blocksData, err := a.redis.Get(ctx, "cache:found_blocks").Result()
	if err != nil {
		a.writeError(w, http.StatusNotFound, "Found blocks not available")
		return
	}

	var blocks []map[string]interface{}
	if err := json.Unmarshal([]byte(blocksData), &blocks); err != nil {
		a.writeError(w, http.StatusInternalServerError, "Invalid found blocks data")
		return
	}

	// Find the specific block
	var foundBlock map[string]interface{}
	var blockIndex int = -1
	for i, block := range blocks {
		if h, ok := block["height"].(float64); ok && uint64(h) == height {
			foundBlock = block
			blockIndex = i
			break
		}
	}

	if foundBlock == nil {
		a.writeError(w, http.StatusNotFound, "Block not found")
		return
	}

	// Calculate effort for this block
	if blockIndex > 0 {
		prevBlock := blocks[blockIndex-1]
		prevHashes, _ := strconv.ParseFloat(fmt.Sprintf("%v", prevBlock["total_hashes"]), 64)
		currHashes, _ := strconv.ParseFloat(fmt.Sprintf("%v", foundBlock["total_hashes"]), 64)
		diff, _ := strconv.ParseFloat(fmt.Sprintf("%v", foundBlock["difficulty"]), 64)
		if diff > 0 {
			roundHashes := currHashes - prevHashes
			effort := (roundHashes / diff) * 100
			foundBlock["effort"] = effort
		}
	}

	// Build response
	result := map[string]interface{}{
		"block": foundBlock,
	}

	// Get PPLNS snapshot for this block
	snapshotsData, err := a.redis.Get(ctx, "cache:pplns_snapshots").Result()
	if err == nil {
		var snapshots map[string][]string
		if json.Unmarshal([]byte(snapshotsData), &snapshots) == nil {
			if minerAddrs, ok := snapshots[heightStr]; ok {
				result["pplns_snapshot"] = minerAddrs
			}
		}
	}

	// Get payouts for this block from miner payouts
	// Build a map of payouts for this specific block height
	payouts := make([]map[string]interface{}, 0)

	// Get all miner payout keys
	keys, err := a.redis.Keys(ctx, "miner:*:payouts").Result()
	if err == nil {
		for _, key := range keys {
			// Extract miner address from key (miner:{addr}:payouts)
			parts := strings.Split(key, ":")
			if len(parts) < 2 {
				continue
			}
			minerAddr := parts[1]

			data, err := a.redis.Get(ctx, key).Result()
			if err != nil {
				continue
			}
			var minerPayouts []map[string]interface{}
			if json.Unmarshal([]byte(data), &minerPayouts) != nil {
				continue
			}
			for _, payout := range minerPayouts {
				if h, ok := payout["block_height"].(float64); ok && uint64(h) == height {
					// Add miner address to the payout object
					payout["address"] = minerAddr
					payouts = append(payouts, payout)
					break // Only one payout per miner per block
				}
			}
		}
	}

	// Sort payouts by amount descending
	if len(payouts) > 0 {
		// Simple bubble sort for small arrays
		for i := 0; i < len(payouts); i++ {
			for j := i + 1; j < len(payouts); j++ {
				amtI, _ := payouts[i]["amount"].(float64)
				amtJ, _ := payouts[j]["amount"].(float64)
				if amtJ > amtI {
					payouts[i], payouts[j] = payouts[j], payouts[i]
				}
			}
		}
		result["payouts"] = payouts
	}

	// Cache the response
	if jsonData, err := json.Marshal(result); err == nil {
		cacheSet(cacheKey, jsonData, CacheTTLMedium) // 60s cache
	}

	w.Header().Set("X-Cache", "MISS")
	a.writeJSON(w, result)
}

// GET /api/stats - cryptonote-nodejs-pool compatible stats endpoint (cached 10s)
func (a *API) handleStats(w http.ResponseWriter, r *http.Request) {
	cacheKey := "api:stats"

	if data, ok := cacheGet(cacheKey); ok {
		a.writeSanitizedCache(w, data)
		return
	}

	ctx := r.Context()

	// Build the response structure
	result := make(map[string]interface{})

	// Config section (static P2Pool-specific values)
	config := map[string]interface{}{
		"poolHost":             "www.whiskymine.io",
		"ports":                []interface{}{}, // P2Pool has no ports - miners connect to p2pool directly
		"cnAlgorithm":          "randomx",
		"coin":                 "salvium",
		"coinUnits":            100000000,
		"coinDecimalPlaces":    8,
		"coinDifficultyTarget": 120,
		"symbol":               "SAL",
		"version":              "p2pool-salvium-observer",
	}
	result["config"] = config

	// Pool section
	pool := make(map[string]interface{})

	if miners, err := a.redis.Get(ctx, "stats:pool:miners").Int(); err == nil {
		pool["miners"] = miners
	}
	if hashrate, err := a.redis.Get(ctx, "stats:pool:hashrate").Uint64(); err == nil {
		pool["hashrate"] = hashrate
	}
	if blocksFound, err := a.redis.Get(ctx, "stats:pool:blocks_found").Int(); err == nil {
		pool["totalBlocks"] = blocksFound
	}
	if lastFoundTime, err := a.redis.Get(ctx, "stats:pool:last_found_time").Int64(); err == nil {
		pool["lastBlockFound"] = lastFoundTime * 1000 // Convert to milliseconds
	}

	// PPLNS window info
	if pplnsData, err := a.redis.Get(ctx, "cache:pplns:full").Result(); err == nil {
		var pplns map[string]interface{}
		if json.Unmarshal([]byte(pplnsData), &pplns) == nil {
			if blocks, ok := pplns["blocks_included"]; ok {
				pool["pplnsWindowBlocks"] = blocks
			}
			if duration, ok := pplns["window_duration"]; ok {
				pool["pplnsWindowDuration"] = duration
			}
		}
	}

	// Get recent found blocks (limit to 30)
	if blocksData, err := a.redis.Get(ctx, "cache:found_blocks").Result(); err == nil {
		var allBlocks []map[string]interface{}
		if json.Unmarshal([]byte(blocksData), &allBlocks) == nil {
			// Take the last 30 blocks (most recent)
			blockLimit := 30
			startIdx := 0
			if len(allBlocks) > blockLimit {
				startIdx = len(allBlocks) - blockLimit
			}
			recentBlocks := allBlocks[startIdx:]

			// Reverse to show newest first
			formattedBlocks := make([]map[string]interface{}, 0, len(recentBlocks))
			for i := len(recentBlocks) - 1; i >= 0; i-- {
				block := recentBlocks[i]
				formatted := map[string]interface{}{
					"height":     block["height"],
					"hash":       block["hash"],
					"time":       block["timestamp"],
					"difficulty": block["difficulty"],
					"reward":     block["reward"],
					"miner":      block["finder"],
				}
				formattedBlocks = append(formattedBlocks, formatted)
			}
			pool["blocks"] = formattedBlocks
		}
	}

	result["pool"] = pool

	// Network section
	network := make(map[string]interface{})

	if height, err := a.redis.Get(ctx, "stats:network:height").Uint64(); err == nil {
		network["height"] = height
	}
	if diff, err := a.redis.Get(ctx, "stats:network:difficulty").Uint64(); err == nil {
		network["difficulty"] = diff
		// Calculate network hashrate from difficulty (difficulty / block_time)
		network["hashrate"] = diff / 120
	}

	result["network"] = network

	// Last block section
	lastblock := make(map[string]interface{})

	// Get the most recent found block for lastblock info
	if blocksData, err := a.redis.Get(ctx, "cache:found_blocks").Result(); err == nil {
		var allBlocks []map[string]interface{}
		if json.Unmarshal([]byte(blocksData), &allBlocks) == nil && len(allBlocks) > 0 {
			lastFound := allBlocks[len(allBlocks)-1]
			lastblock["height"] = lastFound["height"]
			lastblock["hash"] = lastFound["hash"]
			lastblock["timestamp"] = lastFound["timestamp"]
			lastblock["difficulty"] = lastFound["difficulty"]
			lastblock["reward"] = lastFound["reward"]
		}
	}

	result["lastblock"] = lastblock

	// Cache the response
	if jsonData, err := json.Marshal(result); err == nil {
		cacheSet(cacheKey, jsonData, CacheTTLShort)
	}

	w.Header().Set("X-Cache", "MISS")
	a.writeJSON(w, result)
}
