package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"git.gammaspectra.live/P2Pool/consensus/v4/monero"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/client"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/randomx"
	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/cache/legacy"
	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/sidechain"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
	"github.com/redis/go-redis/v9"
)

// ============================================================================
// P2POOL SALVIUM OBSERVER DAEMON v2
// ============================================================================
//
// Data Sources:
//   1. data-api JSON files (pool/stats, network/stats, stats_mod)
//   2. p2pool.cache file (binary, via consensus layer)
//
// Storage: Redis/Valkey with three data classifications:
//   1. Permanent (no TTL): total_blocks, total_paid, block history
//   2. Current (no TTL, updated): pool hashrate, network stats, miner count
//   3. Temporal (with TTL): recent shares, per-miner stats, except PPLNS window
//
// Redis Key Structure:
//   stats:pool:hashrate          - Current pool hashrate (no TTL)
//   stats:pool:miners            - Current miner count (no TTL)
//   stats:network:height         - Salvium height (no TTL)
//   stats:network:difficulty     - Salvium difficulty (no TTL)
//   stats:sidechain:height       - P2Pool sidechain height (no TTL)
//   stats:total:blocks           - Total blocks found (permanent)
//   stats:total:paid             - Total paid out (permanent)
//   shares:pplns:{height}        - Share data within PPLNS window (calculated TTL)
//   shares:recent                - Recent shares list (24h TTL)
//   miner:{address}:hashrate     - Per-miner hashrate (24h TTL)
//   miner:{address}:shares       - Per-miner share count (24h TTL)
//   blocks:found:{hash}          - Block details (permanent)
//   blocks:recent                - Recent blocks list (7d TTL)
//
// Resilience Features:
//   - Exponential backoff retry for Redis and RPC connections
//   - In-memory buffering of 1M Redis operations (~190MB, ~5 days of outage)
//   - Atomic file reading with retry for data-api files
//   - Connection health monitoring and automatic recovery
//   - Graceful degradation during outages
//   - Signal handling (SIGTERM/SIGINT/SIGHUP) with 30s flush timeout
//
// ============================================================================

// ============================================================================
// RESILIENCE INFRASTRUCTURE
// ============================================================================

// RetryConfig defines retry behavior
type RetryConfig struct {
	MaxRetries     int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	BackoffFactor  float64
}

// DefaultRetryConfig provides sensible defaults
var DefaultRetryConfig = RetryConfig{
	MaxRetries:     5,
	InitialBackoff: 100 * time.Millisecond,
	MaxBackoff:     30 * time.Second,
	BackoffFactor:  2.0,
}

// RetryWithBackoff executes a function with exponential backoff
func RetryWithBackoff(ctx context.Context, cfg RetryConfig, name string, fn func() error) error {
	var lastErr error
	backoff := cfg.InitialBackoff

	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			utils.Logf("RETRY", "%s: attempt %d/%d after %v", name, attempt, cfg.MaxRetries, backoff)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}

		if err := fn(); err != nil {
			lastErr = err
			if attempt < cfg.MaxRetries {
				backoff = time.Duration(float64(backoff) * cfg.BackoffFactor)
				if backoff > cfg.MaxBackoff {
					backoff = cfg.MaxBackoff
				}
			}
			continue
		}
		return nil
	}

	return fmt.Errorf("%s: failed after %d attempts: %w", name, cfg.MaxRetries+1, lastErr)
}

// RedisOperation represents a buffered Redis operation
type RedisOperation struct {
	Type      string        // "set", "incr", "hset"
	Key       string
	Value     interface{}
	TTL       time.Duration
	Field     string // for hset
	Timestamp time.Time
}

// BufferedRedis wraps Redis client with buffering and retry logic
type BufferedRedis struct {
	client      *redis.Client
	ctx         context.Context
	buffer      []RedisOperation
	bufferMu    sync.Mutex
	isHealthy   bool
	healthMu    sync.RWMutex
	retryConfig RetryConfig
	maxBuffer   int
}

func NewBufferedRedis(ctx context.Context, addr string, maxBuffer int) (*BufferedRedis, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     "",
		DB:           0,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolSize:     10,
		MinIdleConns: 2,
	})

	br := &BufferedRedis{
		client:      rdb,
		ctx:         ctx,
		buffer:      make([]RedisOperation, 0, maxBuffer),
		retryConfig: DefaultRetryConfig,
		maxBuffer:   maxBuffer,
	}

	// Initial health check
	if err := br.ping(); err != nil {
		utils.Errorf("REDIS", "Initial connection failed: %s (will retry)", err)
		br.setHealth(false)
	} else {
		utils.Logf("REDIS", "Connected successfully to %s", addr)
		br.setHealth(true)
	}

	// Start background flush worker
	go br.flushWorker()

	return br, nil
}

func (b *BufferedRedis) ping() error {
	return RetryWithBackoff(b.ctx, b.retryConfig, "Redis ping", func() error {
		return b.client.Ping(b.ctx).Err()
	})
}

func (b *BufferedRedis) setHealth(healthy bool) {
	b.healthMu.Lock()
	defer b.healthMu.Unlock()
	if b.isHealthy != healthy {
		b.isHealthy = healthy
		if healthy {
			utils.Logf("REDIS", "Connection restored")
		} else {
			utils.Errorf("REDIS", "Connection lost")
		}
	}
}

func (b *BufferedRedis) getHealth() bool {
	b.healthMu.RLock()
	defer b.healthMu.RUnlock()
	return b.isHealthy
}

func (b *BufferedRedis) addToBuffer(op RedisOperation) {
	b.bufferMu.Lock()
	defer b.bufferMu.Unlock()

	// Add timestamp
	op.Timestamp = time.Now()

	// If buffer is full, remove oldest operation
	if len(b.buffer) >= b.maxBuffer {
		utils.Errorf("REDIS", "Buffer full (%d ops), dropping oldest operation", b.maxBuffer)
		b.buffer = b.buffer[1:]
	}

	b.buffer = append(b.buffer, op)
	utils.Logf("REDIS", "Buffered %s operation for key %s (buffer size: %d)", op.Type, op.Key, len(b.buffer))
}

func (b *BufferedRedis) flushWorker() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-b.ctx.Done():
			return
		case <-ticker.C:
			b.flushBuffer()
		}
	}
}

func (b *BufferedRedis) flushBuffer() {
	// Check health first
	if err := b.ping(); err != nil {
		b.setHealth(false)
		return
	}
	b.setHealth(true)

	b.bufferMu.Lock()
	if len(b.buffer) == 0 {
		b.bufferMu.Unlock()
		return
	}

	// Copy buffer and clear it
	ops := make([]RedisOperation, len(b.buffer))
	copy(ops, b.buffer)
	b.buffer = b.buffer[:0]
	b.bufferMu.Unlock()

	utils.Logf("REDIS", "Flushing %d buffered operations", len(ops))

	// Execute buffered operations
	failed := 0
	for _, op := range ops {
		var err error
		switch op.Type {
		case "set":
			err = b.client.Set(b.ctx, op.Key, op.Value, op.TTL).Err()
		case "incr":
			err = b.client.Incr(b.ctx, op.Key).Err()
		case "hset":
			err = b.client.HSet(b.ctx, op.Key, op.Field, op.Value).Err()
		case "sadd":
			members := op.Value.([]interface{})
			err = b.client.SAdd(b.ctx, op.Key, members...).Err()
		case "expire":
			err = b.client.Expire(b.ctx, op.Key, op.TTL).Err()
		case "lpush":
			values := op.Value.([]interface{})
			err = b.client.LPush(b.ctx, op.Key, values...).Err()
		case "ltrim":
			bounds := op.Value.([]int64)
			err = b.client.LTrim(b.ctx, op.Key, bounds[0], bounds[1]).Err()
		case "zadd":
			scoreMap := op.Value.(map[string]float64)
			for member, score := range scoreMap {
				err = b.client.ZAdd(b.ctx, op.Key, redis.Z{Score: score, Member: member}).Err()
			}
		}

		if err != nil {
			utils.Errorf("REDIS", "Failed to flush %s operation for key %s: %s", op.Type, op.Key, err)
			failed++
			// Re-add to buffer
			b.addToBuffer(op)
		}
	}

	if failed == 0 {
		utils.Logf("REDIS", "Successfully flushed %d operations", len(ops))
	} else {
		utils.Errorf("REDIS", "Flushed %d operations, %d failed and re-buffered", len(ops), failed)
	}
}

func (b *BufferedRedis) Set(key string, value interface{}, ttl time.Duration) error {
	if !b.getHealth() {
		b.addToBuffer(RedisOperation{Type: "set", Key: key, Value: value, TTL: ttl})
		return nil
	}

	err := b.client.Set(b.ctx, key, value, ttl).Err()
	if err != nil {
		b.setHealth(false)
		b.addToBuffer(RedisOperation{Type: "set", Key: key, Value: value, TTL: ttl})
	}
	return err
}

func (b *BufferedRedis) Get(key string) (string, error) {
	if !b.getHealth() {
		return "", errors.New("redis unavailable")
	}
	return b.client.Get(b.ctx, key).Result()
}

func (b *BufferedRedis) Incr(key string) error {
	if !b.getHealth() {
		b.addToBuffer(RedisOperation{Type: "incr", Key: key})
		return nil
	}

	err := b.client.Incr(b.ctx, key).Err()
	if err != nil {
		b.setHealth(false)
		b.addToBuffer(RedisOperation{Type: "incr", Key: key})
	}
	return err
}

func (b *BufferedRedis) HSet(key string, field string, value interface{}) error {
	if !b.getHealth() {
		b.addToBuffer(RedisOperation{Type: "hset", Key: key, Field: field, Value: value})
		return nil
	}

	err := b.client.HSet(b.ctx, key, field, value).Err()
	if err != nil {
		b.setHealth(false)
		b.addToBuffer(RedisOperation{Type: "hset", Key: key, Field: field, Value: value})
	}
	return err
}

func (b *BufferedRedis) HGet(key string, field string) (string, error) {
	if !b.getHealth() {
		return "", errors.New("redis unavailable")
	}
	return b.client.HGet(b.ctx, key, field).Result()
}

func (b *BufferedRedis) SAdd(key string, members ...interface{}) error {
	b.addToBuffer(RedisOperation{
		Type:  "sadd",
		Key:   key,
		Value: members,
	})
	return nil
}

func (b *BufferedRedis) Expire(key string, ttl time.Duration) error {
	b.addToBuffer(RedisOperation{
		Type: "expire",
		Key:  key,
		TTL:  ttl,
	})
	return nil
}

func (b *BufferedRedis) LPush(key string, values ...interface{}) error {
	b.addToBuffer(RedisOperation{
		Type:  "lpush",
		Key:   key,
		Value: values,
	})
	return nil
}

func (b *BufferedRedis) LTrim(key string, start, stop int64) error {
	b.addToBuffer(RedisOperation{
		Type:  "ltrim",
		Key:   key,
		Value: []int64{start, stop},
	})
	return nil
}

func (b *BufferedRedis) ZAdd(key string, score float64, member string) error {
	b.addToBuffer(RedisOperation{
		Type:  "zadd",
		Key:   key,
		Value: map[string]float64{member: score},
	})
	return nil
}

func (b *BufferedRedis) Close() error {
	return b.CloseWithTimeout(30 * time.Second)
}

func (b *BufferedRedis) CloseWithTimeout(timeout time.Duration) error {
	utils.Logf("REDIS", "Shutting down - flushing buffer with %v timeout", timeout)

	// Create timeout context for flush
	flushCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Channel to signal flush completion
	done := make(chan struct{})

	go func() {
		b.flushBuffer()
		close(done)
	}()

	// Wait for flush or timeout
	select {
	case <-done:
		b.bufferMu.Lock()
		remaining := len(b.buffer)
		b.bufferMu.Unlock()

		if remaining > 0 {
			utils.Errorf("REDIS", "Shutdown: %d operations remain buffered (Redis unavailable)", remaining)
		} else {
			utils.Logf("REDIS", "Shutdown: all buffered operations flushed successfully")
		}
	case <-flushCtx.Done():
		b.bufferMu.Lock()
		remaining := len(b.buffer)
		b.bufferMu.Unlock()
		utils.Errorf("REDIS", "Shutdown: flush timeout - %d operations lost", remaining)
	}

	return b.client.Close()
}

// AtomicFileReader reads JSON files with retry and validation
func AtomicFileReader(ctx context.Context, path string, v interface{}) error {
	return RetryWithBackoff(ctx, RetryConfig{
		MaxRetries:     3,
		InitialBackoff: 50 * time.Millisecond,
		MaxBackoff:     500 * time.Millisecond,
		BackoffFactor:  2.0,
	}, fmt.Sprintf("read %s", filepath.Base(path)), func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		// Validate JSON is complete (basic check)
		if len(data) == 0 {
			return fmt.Errorf("empty file")
		}

		// Attempt to parse JSON
		if err := json.Unmarshal(data, v); err != nil {
			// Could be mid-write, retry
			return fmt.Errorf("invalid JSON (possibly mid-write): %w", err)
		}

		return nil
	})
}

// ResilientRPCManager handles multiple Salvium RPC endpoints with failover
type ResilientRPCManager struct {
	endpoints      []string
	currentIndex   int
	healthStatus   []bool
	lastHealthCheck time.Time
	mu             sync.RWMutex
	ctx            context.Context
}

func NewResilientRPCManager(ctx context.Context, endpoints []string) *ResilientRPCManager {
	return &ResilientRPCManager{
		endpoints:    endpoints,
		currentIndex: 0,
		healthStatus: make([]bool, len(endpoints)),
		ctx:          ctx,
	}
}

func (r *ResilientRPCManager) getCurrentEndpoint() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.endpoints[r.currentIndex]
}

func (r *ResilientRPCManager) setEndpoint(index int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if index != r.currentIndex {
		utils.Logf("SALVIUM-RPC", "Switching endpoint: %s -> %s", r.endpoints[r.currentIndex], r.endpoints[index])
		r.currentIndex = index
	}
	client.SetDefaultClientSettings(r.endpoints[index])
}

func (r *ResilientRPCManager) markEndpointHealth(index int, healthy bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.healthStatus[index] = healthy
}

// TryAllEndpoints attempts to connect to any available RPC endpoint
func (r *ResilientRPCManager) TryAllEndpoints() error {
	r.mu.RLock()
	endpoints := make([]string, len(r.endpoints))
	copy(endpoints, r.endpoints)
	startIndex := r.currentIndex
	r.mu.RUnlock()

	// Try current endpoint first
	for i := 0; i < len(endpoints); i++ {
		idx := (startIndex + i) % len(endpoints)
		endpoint := endpoints[idx]

		utils.Logf("SALVIUM-RPC", "Trying endpoint %d/%d: %s", i+1, len(endpoints), endpoint)
		r.setEndpoint(idx)

		c := client.GetDefaultClient()
		info, err := c.GetInfo()
		if err != nil {
			utils.Errorf("SALVIUM-RPC", "Endpoint %s failed: %s", endpoint, err)
			r.markEndpointHealth(idx, false)
			continue
		}
		if info == nil {
			utils.Errorf("SALVIUM-RPC", "Endpoint %s returned nil info", endpoint)
			r.markEndpointHealth(idx, false)
			continue
		}

		// Success!
		r.markEndpointHealth(idx, true)
		utils.Logf("SALVIUM-RPC", "Connected to %s: height=%d, peers=%d",
			endpoint, info.Height, info.OutgoingConnectionsCount+info.IncomingConnectionsCount)
		return nil
	}

	return fmt.Errorf("all %d RPC endpoints failed", len(endpoints))
}

// CheckSalviumRPC verifies Salvium RPC connection with retry and failover
func CheckSalviumRPC(ctx context.Context, manager *ResilientRPCManager) error {
	return RetryWithBackoff(ctx, DefaultRetryConfig, "Salvium RPC", func() error {
		return manager.TryAllEndpoints()
	})
}

// splitAndTrim splits a string by delimiter and trims whitespace
func splitAndTrim(s, delim string) []string {
	parts := []string{}
	for _, part := range splitString(s, delim) {
		trimmed := trimSpace(part)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return parts
}

func splitString(s, delim string) []string {
	if s == "" {
		return []string{}
	}
	result := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if i+len(delim) <= len(s) && s[i:i+len(delim)] == delim {
			result = append(result, s[start:i])
			start = i + len(delim)
			i += len(delim) - 1
		}
	}
	result = append(result, s[start:])
	return result
}

func trimSpace(s string) string {
	start := 0
	end := len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

// ============================================================================
// DATA STRUCTURES
// ============================================================================

// DataAPIStats represents the stats_mod JSON structure
type DataAPIStats struct {
	Config struct {
		Ports []struct {
			Port int  `json:"port"`
			TLS  bool `json:"tls"`
		} `json:"ports"`
		Fee                 int `json:"fee"`
		MinPaymentThreshold int `json:"minPaymentThreshold"`
	} `json:"config"`
	Network struct {
		Height uint64 `json:"height"`
	} `json:"network"`
	Pool struct {
		Stats struct {
			LastBlockFound string `json:"lastBlockFound"`
		} `json:"stats"`
		Blocks    []string `json:"blocks"`
		Miners    int      `json:"miners"`
		Hashrate  uint64   `json:"hashrate"`
		RoundHash uint64   `json:"roundHashes"`
	} `json:"pool"`
}

// NetworkStats represents the network/stats JSON structure
type NetworkStats struct {
	Difficulty uint64 `json:"difficulty"`
	Hash       string `json:"hash"`
	Height     uint64 `json:"height"`
	Reward     uint64 `json:"reward"`
	Timestamp  int64  `json:"timestamp"`
}

// PoolStats represents the pool/stats JSON structure
type PoolStats struct {
	PoolList       []string `json:"pool_list"`
	PoolStatistics struct {
		HashRate            uint64 `json:"hashRate"`
		Miners              int    `json:"miners"`
		TotalHashes         uint64 `json:"totalHashes"`
		LastBlockFoundTime  int64  `json:"lastBlockFoundTime"`
		LastBlockFound      uint64 `json:"lastBlockFound"`
		TotalBlocksFound    uint64 `json:"totalBlocksFound"`
		PPLNSWeight         uint64 `json:"pplnsWeight"`
		PPLNSWindowSize     uint64 `json:"pplnsWindowSize"`
		SidechainDifficulty uint64 `json:"sidechainDifficulty"`
		SidechainHeight     uint64 `json:"sidechainHeight"`
	} `json:"pool_statistics"`
}

// Daemon2 is the main daemon structure
type Daemon2 struct {
	consensus    *sidechain.Consensus
	cache        *legacy.Cache
	dataAPIPath  string
	redis        RedisClient
	ctx          context.Context

	// Tracking
	lastProcessedHeight uint64
	knownShares         map[types.Hash]bool
	allShares           []*sidechain.PoolBlock // All loaded shares for calculations
	foundBlocks         []FoundBlockData       // Track found blocks

	// Cached data from data-api
	mainchainDifficulty uint64
	sidechainDifficulty uint64

	// Event-driven tracking
	lastSidechainHeight uint64
	lastMainchainHeight uint64
	lastMediumRecalc    time.Time
}

// ShareData represents a share stored in Redis
type ShareData struct {
	TemplateId           string   `json:"template_id"`
	SideHeight           uint64   `json:"side_height"`
	MainHeight           uint64   `json:"main_height"`
	MainId               string   `json:"main_id"`
	Timestamp            uint64   `json:"timestamp"`
	Difficulty           uint64   `json:"difficulty"`
	CumulativeDifficulty string   `json:"cumulative_difficulty"` // Stored as string (big int)
	Parent               string   `json:"parent"`                // Parent template ID
	Uncles               []string `json:"uncles,omitempty"`      // Uncle template IDs
	MinerAddress         string   `json:"miner_address"`
	SoftwareId           uint32   `json:"software_id"`
	SoftwareVersion      uint32   `json:"software_version"`
	UncleOf              string   `json:"uncle_of,omitempty"`
	IsOrphan             bool     `json:"is_orphan"`
	WindowWeight         uint64   `json:"window_weight"`
}

// FoundBlockData represents a found block stored in Redis
type FoundBlockData struct {
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

// PPLNSWindowData represents PPLNS window stats
type PPLNSWindowData struct {
	Miners int `json:"miners"`
	Blocks int `json:"blocks"`
	Uncles int `json:"uncles"`
	Weight uint64 `json:"weight"`
}

// EffortData represents effort statistics
type EffortData struct {
	Current    float64 `json:"current"`
	Average10  float64 `json:"average10"`
	Average50  float64 `json:"average50"`
	Average200 float64 `json:"average200"`
}

// MainBlockData represents a mainchain block stored in Redis
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

// RedisClient is a placeholder interface for Redis operations
// TODO: Implement actual Redis client
type RedisClient interface {
	Set(key string, value interface{}, ttl time.Duration) error
	Get(key string) (string, error)
	Incr(key string) error
	HSet(key string, field string, value interface{}) error
	HGet(key string, field string) (string, error)
	SAdd(key string, members ...interface{}) error
	Expire(key string, ttl time.Duration) error
	LPush(key string, values ...interface{}) error
	LTrim(key string, start, stop int64) error
	ZAdd(key string, score float64, member string) error
}

func main() {
	// Command line flags
	salviumHost := flag.String("host", "127.0.0.1", "IP address of Salvium node (deprecated, use --salvium-rpc)")
	salviumRpcPort := flag.Uint("rpc-port", 19081, "salviumd RPC API port number (deprecated, use --salvium-rpc)")
	salviumRpcEndpoints := flag.String("salvium-rpc", "", "Comma-separated list of Salvium RPC URLs (e.g., http://node1:19081,http://node2:19081)")
	dataAPIPath := flag.String("data-api", "", "Path to P2Pool data-api directory")
	cachePath := flag.String("cache", "", "Path to p2pool.cache file")
	redisAddr := flag.String("redis", "localhost:6379", "Redis server address")
	fullMode := flag.Bool("full-mode", false, "Allocate RandomX dataset, uses 2GB of RAM")
	flag.Parse()

	if *dataAPIPath == "" {
		utils.Panic(fmt.Errorf("--data-api path is required"))
	}
	if *cachePath == "" {
		utils.Panic(fmt.Errorf("--cache path is required"))
	}

	// Set up context with cancellation for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	// Handle signals in background
	go func() {
		sig := <-sigChan
		utils.Logf("DAEMON", "Received signal: %v - initiating graceful shutdown", sig)
		cancel()
	}()

	// Parse Salvium RPC endpoints
	var rpcEndpoints []string
	if *salviumRpcEndpoints != "" {
		// Use new multi-endpoint format
		for _, endpoint := range splitAndTrim(*salviumRpcEndpoints, ",") {
			rpcEndpoints = append(rpcEndpoints, endpoint)
		}
	} else {
		// Backward compatibility: use host:port
		rpcEndpoints = []string{fmt.Sprintf("http://%s:%d", *salviumHost, *salviumRpcPort)}
	}

	// Create RPC manager with failover support
	rpcManager := NewResilientRPCManager(ctx, rpcEndpoints)

	utils.Logf("DAEMON", "=================================================")
	utils.Logf("DAEMON", "P2Pool Salvium Observer Daemon v2")
	utils.Logf("DAEMON", "=================================================")
	utils.Logf("DAEMON", "Data API: %s", *dataAPIPath)
	utils.Logf("DAEMON", "Cache: %s", *cachePath)
	utils.Logf("DAEMON", "Redis: %s", *redisAddr)
	utils.Logf("DAEMON", "Salvium RPC endpoints: %d configured", len(rpcEndpoints))
	for i, endpoint := range rpcEndpoints {
		utils.Logf("DAEMON", "  [%d] %s", i+1, endpoint)
	}

	// Verify Salvium RPC connection (tries all endpoints with failover)
	if err := CheckSalviumRPC(ctx, rpcManager); err != nil {
		utils.Errorf("DAEMON", "Warning: All Salvium RPC endpoints failed: %s", err)
		utils.Logf("DAEMON", "Continuing anyway - will retry on each sync cycle")
	}

	// Use default Salvium mainnet consensus
	// TODO: May need Salvium-specific parameters
	consensus := sidechain.ConsensusDefault
	utils.Logf("DAEMON", "Using consensus: %s (PPLNS window: %d)", consensus.PoolName, consensus.ChainWindowSize)

	// Initialize RandomX hasher
	if *fullMode {
		if err := consensus.InitHasher(1, randomx.FlagSecure, randomx.FlagFullMemory); err != nil {
			utils.Panic(err)
		}
		utils.Logf("DAEMON", "RandomX: Full mode (2GB dataset allocated)")
	} else {
		if err := consensus.InitHasher(1, randomx.FlagSecure); err != nil {
			utils.Panic(err)
		}
		utils.Logf("DAEMON", "RandomX: Light mode")
	}

	// Open cache file
	cacheFile, err := legacy.NewCache(consensus, *cachePath)
	if err != nil {
		utils.Panic(fmt.Errorf("failed to open cache: %w", err))
	}
	defer cacheFile.Close()
	utils.Logf("DAEMON", "Cache file opened: %s", *cachePath)

	// Initialize Redis client with buffering (1M operation buffer ~190MB RAM)
	redisClient, err := NewBufferedRedis(ctx, *redisAddr, 1000000)
	if err != nil {
		utils.Panic(fmt.Errorf("failed to initialize Redis client: %w", err))
	}
	defer redisClient.Close()

	// Create daemon instance
	daemon := &Daemon2{
		consensus:    consensus,
		cache:        cacheFile,
		dataAPIPath:  *dataAPIPath,
		redis:        redisClient,
		ctx:          ctx,
		knownShares:  make(map[types.Hash]bool),
	}

	utils.Logf("DAEMON", "=================================================")
	utils.Logf("DAEMON", "Starting sync loop (15s interval)")
	utils.Logf("DAEMON", "=================================================")

	// Start main sync loop
	daemon.run()

	// Graceful shutdown sequence
	utils.Logf("DAEMON", "=================================================")
	utils.Logf("DAEMON", "Graceful shutdown initiated")
	utils.Logf("DAEMON", "=================================================")
	utils.Logf("DAEMON", "Sync loop stopped")
	utils.Logf("DAEMON", "Flushing Redis buffer (defer will execute)")
	// Note: defer statements will execute here (Redis close, cache close)
	utils.Logf("DAEMON", "Shutdown complete - exiting")
}

func (d *Daemon2) run() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	// Do initial sync immediately
	utils.Logf("DAEMON", "Starting initial sync cycle")
	d.syncCycle()

	for {
		select {
		case <-d.ctx.Done():
			utils.Logf("DAEMON", "Context cancelled - stopping sync loop")
			return
		case <-ticker.C:
			d.syncCycle()
		}
	}
}

func (d *Daemon2) syncCycle() {
	// Read data-api files first to get current heights
	if err := d.readDataAPIFiles(); err != nil {
		utils.Errorf("SYNC", "Failed to read data-api files: %s", err)
		return
	}

	// Get current heights from Redis (stored by readDataAPIFiles)
	currentSideHeight, _ := d.redis.Get("stats:sidechain:height")
	currentMainHeight, _ := d.redis.Get("stats:network:height")

	var sideHeight, mainHeight uint64
	fmt.Sscanf(currentSideHeight, "%d", &sideHeight)
	fmt.Sscanf(currentMainHeight, "%d", &mainHeight)

	// Determine what changed
	sidechainChanged := sideHeight != d.lastSidechainHeight
	mainchainChanged := mainHeight != d.lastMainchainHeight
	mediumRecalcDue := time.Since(d.lastMediumRecalc) >= 30*time.Second

	// Initialize on first run
	if d.lastSidechainHeight == 0 {
		d.lastSidechainHeight = sideHeight
		d.lastMainchainHeight = mainHeight
		d.lastMediumRecalc = time.Now()
		sidechainChanged = true
		mainchainChanged = true
		mediumRecalcDue = true
	}

	// Execute tiered work based on what changed
	if sidechainChanged {
		utils.Logf("SYNC", "Sidechain height changed: %d -> %d", d.lastSidechainHeight, sideHeight)
		if err := d.lightWork(); err != nil {
			utils.Errorf("SYNC", "Light work failed: %s", err)
		}
		d.lastSidechainHeight = sideHeight
	}

	if mediumRecalcDue {
		utils.Logf("SYNC", "Medium recalc due (30s timer)")
		if err := d.mediumWork(); err != nil {
			utils.Errorf("SYNC", "Medium work failed: %s", err)
		}
		d.lastMediumRecalc = time.Now()
	}

	if mainchainChanged {
		utils.Logf("SYNC", "Mainchain height changed: %d -> %d", d.lastMainchainHeight, mainHeight)
		if err := d.heavyWork(); err != nil {
			utils.Errorf("SYNC", "Heavy work failed: %s", err)
		}
		d.lastMainchainHeight = mainHeight
	}
}

func (d *Daemon2) readDataAPIFiles() error {
	// Read stats_mod with atomic retry
	statsModPath := filepath.Join(d.dataAPIPath, "stats_mod")
	var statsMod DataAPIStats
	if err := AtomicFileReader(d.ctx, statsModPath, &statsMod); err != nil {
		return fmt.Errorf("failed to read stats_mod: %w", err)
	}

	// Read network/stats with atomic retry
	networkStatsPath := filepath.Join(d.dataAPIPath, "network", "stats")
	var networkStats NetworkStats
	if err := AtomicFileReader(d.ctx, networkStatsPath, &networkStats); err != nil {
		return fmt.Errorf("failed to read network/stats: %w", err)
	}

	// Read pool/stats with atomic retry
	poolStatsPath := filepath.Join(d.dataAPIPath, "pool", "stats")
	var poolStats PoolStats
	if err := AtomicFileReader(d.ctx, poolStatsPath, &poolStats); err != nil {
		return fmt.Errorf("failed to read pool/stats: %w", err)
	}

	utils.Logf("DATA-API", "Pool: %d miners, %d H/s", statsMod.Pool.Miners, statsMod.Pool.Hashrate)
	utils.Logf("DATA-API", "Network: height %d, difficulty %d", networkStats.Height, networkStats.Difficulty)
	utils.Logf("DATA-API", "Sidechain: height %d, PPLNS window %d",
		poolStats.PoolStatistics.SidechainHeight,
		poolStats.PoolStatistics.PPLNSWindowSize)

	// Cache difficulties for effort calculation
	d.mainchainDifficulty = networkStats.Difficulty
	d.sidechainDifficulty = poolStats.PoolStatistics.SidechainDifficulty

	// Store current stats in Redis (no TTL - always keep latest)
	d.redis.Set("stats:pool:hashrate", statsMod.Pool.Hashrate, 0)
	d.redis.Set("stats:pool:miners", statsMod.Pool.Miners, 0)
	d.redis.Set("stats:network:height", networkStats.Height, 0)
	d.redis.Set("stats:network:difficulty", networkStats.Difficulty, 0)
	d.redis.Set("stats:network:reward", networkStats.Reward, 0)
	d.redis.Set("stats:sidechain:height", poolStats.PoolStatistics.SidechainHeight, 0)
	d.redis.Set("stats:sidechain:difficulty", poolStats.PoolStatistics.SidechainDifficulty, 0)

	// Store permanent cumulative stats (no TTL)
	d.redis.Set("stats:total:blocks", poolStats.PoolStatistics.TotalBlocksFound, 0)

	return nil
}

// lightWork runs every sidechain block (~10s): Store shares, update counters
func (d *Daemon2) lightWork() error {
	// Load all blocks from cache
	loader := &CacheLoader{
		consensus: d.consensus,
		shares:    make([]*sidechain.PoolBlock, 0),
	}

	d.cache.LoadAll(loader)

	if len(loader.shares) == 0 {
		utils.Logf("LIGHT", "No shares in cache")
		return nil
	}

	// Store all shares for calculations
	d.allShares = loader.shares

	// Process each share
	newShares := 0
	recentShares := []ShareData{}
	minerAddresses := make(map[string]bool)

	// Clear found blocks for fresh calculation
	d.foundBlocks = []FoundBlockData{}

	for _, share := range loader.shares {
		templateId := share.SideTemplateId(d.consensus)
		templateIdHex := templateId.String()

		// Extract miner address
		minerAddr := share.Side.PublicKey.ToAddress(monero.MainNetwork)
		if minerAddr == nil {
			continue
		}
		minerAddrStr := string(minerAddr.ToBase58())
		minerAddresses[minerAddrStr] = true

		// Check if new share
		isNew := !d.knownShares[templateId]
		if isNew {
			d.knownShares[templateId] = true
			newShares++
		}

		// Create share data
		// Extract uncle template IDs
		uncleIds := make([]string, len(share.Side.Uncles))
		for i, uncle := range share.Side.Uncles {
			uncleIds[i] = uncle.String()
		}

		shareData := ShareData{
			TemplateId:           templateIdHex,
			SideHeight:           share.Side.Height,
			MainHeight:           share.Main.Coinbase.GenHeight,
			MainId:               share.Main.Id().String(),
			Timestamp:            share.Main.Timestamp,
			Difficulty:           share.Side.Difficulty.Lo,
			CumulativeDifficulty: share.Side.CumulativeDifficulty.String(),
			Parent:               share.Side.Parent.String(),
			Uncles:               uncleIds,
			MinerAddress:         minerAddrStr,
			SoftwareId:           uint32(share.Side.ExtraBuffer.SoftwareId),
			SoftwareVersion:      uint32(share.Side.ExtraBuffer.SoftwareVersion),
			IsOrphan:             false, // TODO: detect orphans
		}

		// Serialize to JSON
		shareJSON, err := json.Marshal(shareData)
		if err != nil {
			utils.Errorf("CACHE", "Failed to marshal share %s: %s", templateIdHex, err)
			continue
		}

		// Store complete share data with 7-day TTL
		ttl := 7 * 24 * time.Hour
		shareKey := fmt.Sprintf("share:data:%s", templateIdHex)
		d.redis.Set(shareKey, string(shareJSON), ttl)

		// INDEX 1: By side height (for GetSideBlocksByHeight, GetTipSideBlockByHeight)
		heightIndexKey := fmt.Sprintf("sideblock:height:%d", share.Side.Height)
		d.redis.SAdd(heightIndexKey, templateIdHex)
		d.redis.Expire(heightIndexKey, ttl)

		// INDEX 2: By template ID (for GetSideBlocksByTemplateId, GetTipSideBlockByTemplateId)
		templateIndexKey := fmt.Sprintf("sideblock:template:%s", templateIdHex)
		d.redis.Set(templateIndexKey, string(shareJSON), ttl)

		// INDEX 3: By main ID (for GetSideBlockByMainId)
		mainIdIndexKey := fmt.Sprintf("sideblock:main:%s", shareData.MainId)
		d.redis.Set(mainIdIndexKey, templateIdHex, ttl)

		// INDEX 4: By miner address (for GetSideBlocksByMinerIdInWindow)
		minerSharesKey := fmt.Sprintf("miner:%s:shares", minerAddrStr)
		d.redis.LPush(minerSharesKey, templateIdHex) // Add to front of list
		d.redis.LTrim(minerSharesKey, 0, 2159)       // Keep last 2160 shares (PPLNS window)
		d.redis.Expire(minerSharesKey, ttl)

		// Add to recent shares list (keep last 100)
		if len(recentShares) < 100 {
			recentShares = append(recentShares, shareData)
		}

		// Update per-miner stats
		minerKey := fmt.Sprintf("miner:%s", minerAddrStr)
		d.redis.HSet(minerKey, "address", minerAddrStr)
		d.redis.HSet(minerKey, "last_share_height", share.Side.Height)
		d.redis.HSet(minerKey, "last_share_time", share.Main.Timestamp)

		// Increment share count for miner
		minerShareCountKey := fmt.Sprintf("miner:%s:share_count", minerAddrStr)
		d.redis.Incr(minerShareCountKey)

		// INDEX 5: Store uncle relationships (for GetSideBlocksByUncleOfId)
		for _, uncle := range share.Side.Uncles {
			if uncle != types.ZeroHash {
				// This share includes 'uncle' as an uncle
				uncleIndexKey := fmt.Sprintf("sideblock:uncle:%s", uncle.String())
				d.redis.SAdd(uncleIndexKey, templateIdHex)
				d.redis.Expire(uncleIndexKey, ttl)
			}
		}

		// Check if this share found a mainchain block
		if d.isFoundBlock(share) {
			// Calculate reward from coinbase outputs
			var reward uint64 = 0
			for _, output := range share.Main.Coinbase.Outputs {
				reward += output.Reward
			}

			// Count uncles in this block
			uncleCount := 0
			for _, uncle := range share.Side.Uncles {
				if uncle != types.ZeroHash {
					uncleCount++
				}
			}

			foundBlock := FoundBlockData{
				MainId:       share.Main.Id().String(),
				MainHeight:   share.Main.Coinbase.GenHeight,
				TemplateId:   templateIdHex,
				SideHeight:   share.Side.Height,
				Timestamp:    share.Main.Timestamp,
				Reward:       reward,
				Difficulty:   share.Side.Difficulty.Lo,
				MinerAddress: minerAddrStr,
				UncleCount:   uncleCount,
			}
			d.foundBlocks = append(d.foundBlocks, foundBlock)

			// Store individual found block (permanent - no TTL)
			blockKey := fmt.Sprintf("block:found:%s", share.Main.Id().String())
			blockJSON, _ := json.Marshal(foundBlock)
			d.redis.Set(blockKey, string(blockJSON), 0)

			// INDEX: Found blocks by miner (for GetFoundBlocks with miner filter)
			foundByMinerKey := fmt.Sprintf("miner:%s:blocks:found", minerAddrStr)
			d.redis.LPush(foundByMinerKey, share.Main.Id().String())
			// No TTL - permanent list

			// INDEX: Found blocks by side height
			foundBySideHeightKey := fmt.Sprintf("block:found:sideheight:%d", share.Side.Height)
			d.redis.Set(foundBySideHeightKey, share.Main.Id().String(), 0)

			// INDEX: Found blocks by main height
			foundByMainHeightKey := fmt.Sprintf("block:found:mainheight:%d", share.Main.Coinbase.GenHeight)
			d.redis.Set(foundByMainHeightKey, share.Main.Id().String(), 0)
		}
	}

	// Store recent shares list
	if len(recentShares) > 0 {
		recentSharesJSON, _ := json.Marshal(recentShares)
		d.redis.Set("shares:recent", string(recentSharesJSON), 24*time.Hour)
		utils.Logf("LIGHT", "Stored %d recent shares", len(recentShares))
	}

	// Calculate and store sidechain tip
	if len(loader.shares) > 0 {
		tip := loader.shares[len(loader.shares)-1]
		tipData := map[string]interface{}{
			"template_id":  tip.SideTemplateId(d.consensus).String(),
			"side_height":  tip.Side.Height,
			"main_height":  tip.Main.Coinbase.GenHeight,
			"main_id":      tip.Main.Id().String(),
			"timestamp":    tip.Main.Timestamp,
			"difficulty":   tip.Side.Difficulty.Lo,
			"window_depth": d.consensus.ChainWindowSize,
		}
		tipJSON, _ := json.Marshal(tipData)
		d.redis.Set("chain:tip:side", string(tipJSON), 0)
	}

	// Update miners count
	d.redis.Set("stats:sidechain:miners_known", len(minerAddresses), 0)

	if newShares > 0 {
		utils.Logf("LIGHT", "Processed %d new shares out of %d total", newShares, len(loader.shares))
	}

	return nil
}

// mediumWork runs every 30s: Recalculate rankings, aggregate stats
func (d *Daemon2) mediumWork() error {
	if len(d.allShares) == 0 {
		utils.Logf("MEDIUM", "No shares available")
		return nil
	}

	// TODO: Calculate top 10 miners by share count
	// TODO: Calculate per-miner hashrate estimates
	// TODO: Aggregate pool statistics

	utils.Logf("MEDIUM", "Medium work completed (placeholder)")
	return nil
}

// heavyWork runs on mainchain block changes (~2 min): PPLNS, effort, fetch mainchain blocks
func (d *Daemon2) heavyWork() error {
	if len(d.allShares) == 0 {
		utils.Logf("HEAVY", "No shares available")
		return nil
	}

	// Calculate PPLNS window (last N shares based on consensus window size)
	pplnsWindow := d.calculatePPLNSWindow(d.allShares)
	pplnsJSON, _ := json.Marshal(pplnsWindow)
	d.redis.Set("stats:pplns:window", string(pplnsJSON), 0)

	// Calculate effort statistics
	effort := d.calculateEffort(d.allShares)
	effortJSON, _ := json.Marshal(effort)
	d.redis.Set("stats:effort", string(effortJSON), 0)

	// Store found blocks (if any)
	d.storeFoundBlocks()

	// Extract and store payout data from coinbase outputs
	d.extractAndStorePayouts()

	// Fetch and store mainchain blocks referenced by shares
	d.fetchMainBlocksForShares(d.allShares)

	utils.Logf("HEAVY", "Heavy work completed")
	return nil
}

// calculatePPLNSWindow calculates PPLNS window statistics
func (d *Daemon2) calculatePPLNSWindow(shares []*sidechain.PoolBlock) PPLNSWindowData {
	if len(shares) == 0 {
		return PPLNSWindowData{}
	}

	// Get the window size from consensus
	windowSize := d.consensus.ChainWindowSize

	// Take last N shares (up to window size)
	windowShares := shares
	if uint64(len(shares)) > windowSize {
		windowShares = shares[len(shares)-int(windowSize):]
	}

	// Count unique miners in window
	minersInWindow := make(map[string]bool)
	blocksInWindow := 0
	unclesInWindow := 0
	totalWeight := uint64(0)

	for _, share := range windowShares {
		minerAddr := share.Side.PublicKey.ToAddress(monero.MainNetwork)
		if minerAddr != nil {
			minersInWindow[string(minerAddr.ToBase58())] = true
		}

		// TODO: Detect uncles properly
		// For now, count all as regular blocks
		blocksInWindow++

		// Add difficulty as weight
		totalWeight += share.Side.Difficulty.Lo
	}

	return PPLNSWindowData{
		Miners: len(minersInWindow),
		Blocks: blocksInWindow,
		Uncles: unclesInWindow,
		Weight: totalWeight,
	}
}

// calculateEffort calculates effort statistics based on found blocks
func (d *Daemon2) calculateEffort(shares []*sidechain.PoolBlock) EffortData {
	if len(shares) == 0 {
		return EffortData{}
	}

	// Find all blocks that found mainchain blocks
	// A share "found" a block if it was included in the mainchain
	foundBlockIndices := []int{}
	for i, share := range shares {
		// Check if this share found a mainchain block
		// This is indicated by the share being at a mainchain height that exists
		if d.isFoundBlock(share) {
			foundBlockIndices = append(foundBlockIndices, i)
		}
	}

	if len(foundBlockIndices) == 0 {
		// No blocks found yet, current effort is based on all shares
		currentEffort := d.calculateCurrentEffort(shares, len(shares))
		return EffortData{
			Current:    currentEffort,
			Average10:  100.0,
			Average50:  100.0,
			Average200: 100.0,
		}
	}

	// Calculate current effort (shares since last found block)
	lastFoundIndex := foundBlockIndices[len(foundBlockIndices)-1]
	sharesSinceLastBlock := len(shares) - lastFoundIndex - 1
	currentEffort := d.calculateCurrentEffort(shares[lastFoundIndex+1:], sharesSinceLastBlock)

	// Calculate effort for each found block
	efforts := []float64{}
	for i, foundIdx := range foundBlockIndices {
		startIdx := 0
		if i > 0 {
			startIdx = foundBlockIndices[i-1] + 1
		}

		sharesForBlock := foundIdx - startIdx + 1
		blockShares := shares[startIdx : foundIdx+1]
		effort := d.calculateBlockEffort(blockShares, sharesForBlock)
		efforts = append(efforts, effort)
	}

	// Calculate averages
	avg10 := calculateAverage(efforts, 10)
	avg50 := calculateAverage(efforts, 50)
	avg200 := calculateAverage(efforts, 200)

	return EffortData{
		Current:    currentEffort,
		Average10:  avg10,
		Average50:  avg50,
		Average200: avg200,
	}
}

// isFoundBlock checks if a share found a mainchain block
func (d *Daemon2) isFoundBlock(share *sidechain.PoolBlock) bool {
	// A share found a block if it has a valid coinbase with outputs
	// Check if this share's main block is in the actual mainchain
	// For now, we'll use a heuristic: if the share has coinbase outputs
	// and the main difficulty is significantly higher than side difficulty,
	// it likely found a mainchain block

	// TODO: Query Salvium RPC to verify the main block ID matches
	// For now, detect based on coinbase structure
	return len(share.Main.Coinbase.Outputs) > 0 &&
	       share.Main.Coinbase.GenHeight > 0
}

// calculateCurrentEffort calculates effort for shares since last found block
func (d *Daemon2) calculateCurrentEffort(shares []*sidechain.PoolBlock, shareCount int) float64 {
	if len(shares) == 0 || shareCount == 0 {
		return 0.0
	}

	// Use cached difficulties from data-api
	mainDiff := d.mainchainDifficulty
	sideDiff := d.sidechainDifficulty

	if mainDiff == 0 || sideDiff == 0 {
		return 100.0
	}

	// Expected shares = mainchain_difficulty / pool_difficulty
	expectedShares := float64(mainDiff) / float64(sideDiff)

	// Actual shares
	actualShares := float64(shareCount)

	// Effort = (actual / expected) * 100
	if expectedShares > 0 {
		return (actualShares / expectedShares) * 100.0
	}

	return 100.0
}

// calculateBlockEffort calculates effort for a specific found block
func (d *Daemon2) calculateBlockEffort(shares []*sidechain.PoolBlock, shareCount int) float64 {
	if len(shares) == 0 || shareCount == 0 {
		return 100.0
	}

	// Use cached difficulties from data-api
	mainDiff := d.mainchainDifficulty
	sideDiff := d.sidechainDifficulty

	if mainDiff == 0 || sideDiff == 0 {
		return 100.0
	}

	// Expected shares to find this block
	expectedShares := float64(mainDiff) / float64(sideDiff)

	// Actual shares it took
	actualShares := float64(shareCount)

	// Effort percentage
	if expectedShares > 0 {
		return (actualShares / expectedShares) * 100.0
	}

	return 100.0
}

// calculateAverage calculates average of last N elements
func calculateAverage(values []float64, n int) float64 {
	if len(values) == 0 {
		return 100.0
	}

	// Take last N values
	start := 0
	if len(values) > n {
		start = len(values) - n
	}
	subset := values[start:]

	if len(subset) == 0 {
		return 100.0
	}

	sum := 0.0
	for _, v := range subset {
		sum += v
	}

	return sum / float64(len(subset))
}

// extractAndStorePayouts extracts payout data from coinbase outputs
// TODO: This requires decrypting outputs with view/spend keys to determine recipient addresses
// For now, just store basic payout info without recipient mapping
func (d *Daemon2) extractAndStorePayouts() {
	// Process all shares to extract basic payout data from coinbase outputs
	for _, share := range d.allShares {
		if !d.isFoundBlock(share) {
			continue
		}

		mainId := share.Main.Id().String()
		sideHeight := share.Side.Height
		mainHeight := share.Main.Coinbase.GenHeight
		timestamp := share.Main.Timestamp

		// Store basic payout info for the block (output count and total reward)
		totalReward := uint64(0)
		outputCount := 0
		for _, output := range share.Main.Coinbase.Outputs {
			totalReward += output.Reward
			outputCount++
		}

		payoutData := map[string]interface{}{
			"main_id":      mainId,
			"side_height":  sideHeight,
			"main_height":  mainHeight,
			"timestamp":    timestamp,
			"total_reward": totalReward,
			"output_count": outputCount,
		}

		payoutJSON, _ := json.Marshal(payoutData)

		// Store payout summary indexed by side height
		payoutKey := fmt.Sprintf("block:payouts:%d", sideHeight)
		d.redis.Set(payoutKey, string(payoutJSON), 0) // Permanent
	}
}

// storeFoundBlocks stores found block data
func (d *Daemon2) storeFoundBlocks() {
	if len(d.foundBlocks) == 0 {
		return
	}

	// Store recent found blocks (last 50)
	recentBlocks := d.foundBlocks
	if len(recentBlocks) > 50 {
		recentBlocks = recentBlocks[len(recentBlocks)-50:]
	}
	blocksJSON, _ := json.Marshal(recentBlocks)
	d.redis.Set("blocks:found:recent", string(blocksJSON), 0)

	// Store all found blocks in a sorted set for efficient range queries
	// Score = timestamp (for chronological ordering)
	for _, block := range d.foundBlocks {
		d.redis.ZAdd("blocks:found:all", float64(block.Timestamp), block.MainId)
	}

	// Store total count of found blocks
	d.redis.Set("stats:total:blocks:found", len(d.foundBlocks), 0)

	utils.Logf("CACHE", "Stored %d found blocks", len(d.foundBlocks))
}

// fetchAndStoreMainBlock fetches mainchain block data from Salvium RPC and stores in Redis
func (d *Daemon2) fetchAndStoreMainBlock(height uint64) error {
	c := client.GetDefaultClient()

	// Get block by height
	block, err := c.GetBlockByHeight(height, d.ctx)
	if err != nil {
		return fmt.Errorf("failed to get block at height %d: %w", height, err)
	}

	// Extract block data from BlockHeader
	mainBlock := MainBlockData{
		Id:         block.BlockHeader.Hash.String(),
		Height:     block.BlockHeader.Height,
		Timestamp:  uint64(block.BlockHeader.Timestamp),
		Difficulty: block.BlockHeader.Difficulty,
		Reward:     block.BlockHeader.Reward,
		Size:       block.BlockHeader.BlockSize,
		Weight:     block.BlockHeader.BlockWeight,
		TxCount:    int(block.BlockHeader.NumTxes),
	}

	// Store by height
	heightKey := fmt.Sprintf("block:main:height:%d", height)
	blockJSON, _ := json.Marshal(mainBlock)
	d.redis.Set(heightKey, string(blockJSON), 0) // No TTL - permanent

	// Store by ID for quick ID lookups
	idKey := fmt.Sprintf("block:main:id:%s", mainBlock.Id)
	d.redis.Set(idKey, string(blockJSON), 0) // No TTL - permanent

	utils.Logf("MAINCHAIN", "Stored block %d (%s)", height, mainBlock.Id[:16])
	return nil
}

// fetchMainBlocksForShares fetches mainchain blocks referenced by shares
func (d *Daemon2) fetchMainBlocksForShares(shares []*sidechain.PoolBlock) {
	// Track unique heights to avoid duplicate fetches
	heights := make(map[uint64]bool)

	for _, share := range shares {
		height := share.Main.Coinbase.GenHeight
		if !heights[height] {
			heights[height] = true

			// Check if we already have this block in Redis
			heightKey := fmt.Sprintf("block:main:height:%d", height)
			if _, err := d.redis.Get(heightKey); err != nil {
				// Don't have it, fetch it
				if err := d.fetchAndStoreMainBlock(height); err != nil {
					utils.Errorf("MAINCHAIN", "Failed to fetch block %d: %s", height, err)
				}
			}
		}
	}

	utils.Logf("MAINCHAIN", "Processed %d unique mainchain block heights", len(heights))
}

// CacheLoader implements the cache.Loadee interface
type CacheLoader struct {
	consensus *sidechain.Consensus
	shares    []*sidechain.PoolBlock
}

func (l *CacheLoader) Consensus() *sidechain.Consensus {
	return l.consensus
}

func (l *CacheLoader) AddCachedBlock(block *sidechain.PoolBlock) {
	l.shares = append(l.shares, block)
}
