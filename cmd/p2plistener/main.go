package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	mainblock "git.gammaspectra.live/P2Pool/consensus/v4/monero/block"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/client"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/client/zmq"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/randomx"
	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/mainchain"
	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/mempool"
	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/p2p"
	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/sidechain"
	p2pooltypes "git.gammaspectra.live/P2Pool/consensus/v4/p2pool/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
	"github.com/redis/go-redis/v9"
)

const Version = "P2Pool P2PListener v1.0"

func main() {
	// Command-line flags
	peers := flag.String("peers", "", "Comma-separated list of P2Pool peers (host:port) - optional, will auto-discover from Salvium daemon")
	redisAddr := flag.String("redis", "localhost:6379", "Redis address")
	listenAddr := flag.String("listen", ":38889", "P2P listen address")
	externalIP := flag.String("p2p-external-ip", "", "External IP address to advertise to P2P peers (auto-detect if empty)")
	dataDir := flag.String("data-dir", ".", "Data directory for peer cache and other persistent data")
	sidechainConfig := flag.String("sidechain-config", "", "Path to sidechain_config.json (required)")
	salviumHost := flag.String("salvium-host", "127.0.0.1", "Salvium daemon host")
	salviumPort := flag.Uint("salvium-port", 19081, "Salvium daemon RPC port")

	flag.Parse()

	if *sidechainConfig == "" {
		fmt.Println("Error: --sidechain-config is required")
		fmt.Println("Example: --sidechain-config=/path/to/sidechain_config.json")
		os.Exit(1)
	}

	utils.Logf("", "=================================================")
	utils.Logf("", Version)
	utils.Logf("", "=================================================")

	// Load sidechain configuration
	consensus, err := loadSidechainConfig(*sidechainConfig)
	if err != nil {
		utils.Logf("", "ERROR: Failed to load sidechain config: %v", err)
		os.Exit(1)
	}

	// Initialize RandomX hasher (required for block validation)
	if err := consensus.InitHasher(1, randomx.FlagSecure); err != nil {
		utils.Logf("", "ERROR: Failed to initialize RandomX hasher: %v", err)
		os.Exit(1)
	}
	utils.Logf("", "RandomX: Light mode initialized")

	utils.Logf("", "Network: %s", consensus.PoolName)
	utils.Logf("", "Sidechain: block_time=%ds, min_diff=%d, pplns_window=%d",
		consensus.TargetBlockTime, consensus.MinimumDifficulty, consensus.ChainWindowSize)
	utils.Logf("", "Salvium RPC: %s:%d", *salviumHost, *salviumPort)
	utils.Logf("", "Redis: %s", *redisAddr)
	utils.Logf("", "Listen: %s", *listenAddr)

	// Initialize Salvium RPC client for peer discovery
	salviumClient, err := client.NewClient(fmt.Sprintf("http://%s:%d", *salviumHost, *salviumPort))
	if err != nil {
		utils.Logf("", "ERROR: Failed to create Salvium RPC client: %v", err)
		os.Exit(1)
	}

	// Discover P2Pool peers
	var seedPeers []netip.AddrPort
	peerCacheFile := fmt.Sprintf("%s/p2pool_peers.txt", *dataDir)

	// Use manual peers if provided
	if *peers != "" {
		utils.Logf("", "Using manually specified peers: %s", *peers)
		for _, peer := range strings.Split(*peers, ",") {
			peer = strings.TrimSpace(peer)
			if peer == "" {
				continue
			}
			addrPort, err := netip.ParseAddrPort(peer)
			if err != nil {
				utils.Logf("", "WARNING: Invalid peer address %s: %v", peer, err)
				continue
			}
			seedPeers = append(seedPeers, addrPort)
		}
	} else {
		// Try to load cached peers first
		cachedPeers, err := loadPeerCache(peerCacheFile)
		if err == nil && len(cachedPeers) > 0 {
			seedPeers = cachedPeers
			utils.Logf("", "Loaded %d P2Pool peers from cache", len(seedPeers))
		} else {
			// Auto-discover from Salvium daemon peer list
			utils.Logf("", "Auto-discovering P2Pool peers from Salvium daemon...")
			ctx := context.Background()
			discovered, err := discoverP2PoolPeers(ctx, salviumClient, *listenAddr)
			if err != nil {
				utils.Logf("", "WARNING: Peer discovery failed: %v", err)
			} else {
				seedPeers = discovered
				utils.Logf("", "Discovered %d P2Pool peers", len(seedPeers))
				// Save to cache for next time
				if err := savePeerCache(peerCacheFile, seedPeers); err != nil {
					utils.Logf("", "WARNING: Failed to save peer cache: %v", err)
				}
			}
		}
	}

	if len(seedPeers) == 0 {
		utils.Logf("", "ERROR: No P2Pool peers available")
		utils.Logf("", "Either specify --peers manually or ensure Salvium daemon has peer connections")
		os.Exit(1)
	}

	// Initialize Redis
	ctx := context.Background()
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

	if err := rdb.Ping(ctx).Err(); err != nil {
		utils.Logf("", "ERROR: Redis connection failed: %v", err)
		os.Exit(1)
	}
	utils.Logf("", "Connected to Redis")

	// Auto-detect external IP if not provided
	finalExternalIP := *externalIP
	if finalExternalIP == "" {
		detectedIP, err := detectExternalIP()
		if err != nil {
			utils.Logf("", "WARNING: Could not auto-detect external IP: %v", err)
			utils.Logf("", "You may need to specify --p2p-external-ip manually")
		} else {
			finalExternalIP = detectedIP
			utils.Logf("", "Auto-detected external IP: %s", finalExternalIP)
		}
	}

	// Create wiretap instance
	wiretap, err := NewWiretap(ctx, rdb, salviumClient, consensus, *listenAddr, finalExternalIP, peerCacheFile, seedPeers)
	if err != nil {
		utils.Logf("", "ERROR: Failed to create p2plistener: %v", err)
		os.Exit(1)
	}

	// Fetch initial miner data from Salvium daemon to initialize mainchain
	if result, err := salviumClient.GetMinerData(); err == nil && result != nil {
		// Convert daemon result to P2Pool miner data type
		minerData := &p2pooltypes.MinerData{
			MajorVersion: uint8(result.MajorVersion),
			Height:       result.Height,
			PrevId:       result.PrevId,
			SeedHash:     result.SeedHash,
			Difficulty:   result.Difficulty,
		}
		utils.Logf("", "Fetched initial miner data from Salvium: height=%d", minerData.Height)
		wiretap.mainChain.HandleMinerData(minerData)
	} else {
		utils.Logf("", "WARNING: Could not fetch initial miner data: %v", err)
		utils.Logf("", "Mainchain may not be fully initialized")
	}

	// Start wiretap
	if err := wiretap.Start(); err != nil {
		utils.Logf("", "ERROR: Failed to start p2plistener: %v", err)
		os.Exit(1)
	}

	utils.Logf("", "=================================================")
	utils.Logf("", "P2PListener started - capturing P2Pool blocks")
	utils.Logf("", "=================================================")

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	utils.Logf("", "Shutting down...")
	wiretap.Stop()
	rdb.Close()
}

// P2PListener captures P2Pool blocks from the network and stores them in Redis
type P2PListener struct {
	ctx           context.Context
	cancel        context.CancelFunc
	redis         *redis.Client
	salviumClient *client.Client
	consensus     *sidechain.Consensus
	sideChain     *sidechain.SideChain
	mainChain     *mainchain.MainChain
	p2pServer     *p2p.Server

	// Peer cache file path
	peerCacheFile string

	// Cached mainchain data (updated from P2P peers)
	minerDataTip    *p2pooltypes.MinerData
	minerDataMutex  sync.RWMutex
	mainchainTip    *sidechain.ChainMain
	mainchainMutex  sync.RWMutex

	// Stats
	blocksReceived uint64
	blocksMutex    sync.RWMutex
}

func NewWiretap(ctx context.Context, rdb *redis.Client, salviumClient *client.Client, consensus *sidechain.Consensus, listenAddr, externalIP, peerCacheFile string, seedPeers []netip.AddrPort) (*P2PListener, error) {
	wtCtx, cancel := context.WithCancel(ctx)

	w := &P2PListener{
		ctx:           wtCtx,
		cancel:        cancel,
		redis:         rdb,
		salviumClient: salviumClient,
		consensus:     consensus,
		peerCacheFile: peerCacheFile,
		// Initialize with dummy data - will be updated from P2P peers
		minerDataTip: &p2pooltypes.MinerData{Height: 0},
		mainchainTip: &sidechain.ChainMain{},
	}

	// Create real sidechain with wiretap as the P2PoolInterface
	// The sidechain will manage chain state and call our Store() method
	w.sideChain = sidechain.NewSideChain(w)

	// Use thin prune mode to reduce memory usage
	if err := w.sideChain.SetPruneMode(sidechain.PruneModeThin); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to set prune mode: %w", err)
	}

	// Create mainchain (signature: NewMainChain(s *SideChain, p2pool P2PoolInterface))
	w.mainChain = mainchain.NewMainChain(w.sideChain, w)

	// Construct full listen address with external IP if provided
	fullListenAddr := listenAddr
	if externalIP != "" {
		// Extract port from listenAddr
		_, port, err := net.SplitHostPort(listenAddr)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("invalid listen address: %w", err)
		}
		fullListenAddr = net.JoinHostPort(externalIP, port)
	}

	// Create P2P server with wiretap as the "P2Pool" interface
	// Signature: NewServer(p2pool, listenAddress string, externalPort, maxOut, maxIn, useIPv4, useIPv6, ctx)
	p2pServer, err := p2p.NewServer(w, fullListenAddr, 0, 8, 0, true, true, wtCtx)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create P2P server: %w", err)
	}

	w.p2pServer = p2pServer

	// Add seed peers
	for _, peer := range seedPeers {
		p2pServer.AddToPeerList(peer)
	}

	return w, nil
}

func (w *P2PListener) Start() error {
	// Start P2P server - Listen() has no parameters
	if err := w.p2pServer.Listen(); err != nil {
		return fmt.Errorf("failed to start P2P server: %w", err)
	}

	// Start stats reporter
	go w.statsReporter()

	// TODO: Add peer cache saver once we figure out the p2p.Client API
	// The cache is already saved on initial discovery, which is the most important part

	return nil
}

func (w *P2PListener) Stop() {
	w.cancel()
	if w.p2pServer != nil {
		w.p2pServer.Close()
	}
}

func (w *P2PListener) statsReporter() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
			w.blocksMutex.RLock()
			blocks := w.blocksReceived
			w.blocksMutex.RUnlock()

			utils.Logf("STATS", "Blocks captured: %d, Peers: %d",
				blocks, len(w.p2pServer.Clients()))
		}
	}
}

// TODO: Implement periodic peer cache saver
// Need to figure out how to extract addresses from []*p2p.Client
/*
func (w *P2PListener) peerCacheSaver() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
			// Get current peer list from P2P server
			clients := w.p2pServer.Clients()
			// TODO: Extract netip.AddrPort from each client
		}
	}
}
*/

// P2PoolInterface implementation - minimal version for wiretap

func (w *P2PListener) Consensus() *sidechain.Consensus {
	return w.consensus
}

func (w *P2PListener) SideChain() *sidechain.SideChain {
	return w.sideChain
}

func (w *P2PListener) MainChain() *mainchain.MainChain {
	return w.mainChain
}

func (w *P2PListener) Context() context.Context {
	return w.ctx
}

func (w *P2PListener) Started() bool {
	// We consider ourselves started once the P2P server is listening
	return w.p2pServer != nil
}

func (w *P2PListener) ClientRPC() *client.Client {
	// Return Salvium RPC client for blockchain queries
	return w.salviumClient
}

func (w *P2PListener) ClientZMQ() *zmq.Client {
	// P2PListener doesn't use ZMQ
	return nil
}

func (w *P2PListener) GetChainMainTip() *sidechain.ChainMain {
	return w.mainChain.GetChainMainTip()
}

func (w *P2PListener) GetChainMainByHeight(height uint64) *sidechain.ChainMain {
	return w.mainChain.GetChainMainByHeight(height)
}

func (w *P2PListener) GetChainMainByHash(hash types.Hash) *sidechain.ChainMain {
	return w.mainChain.GetChainMainByHash(hash)
}

func (w *P2PListener) GetMinimalBlockHeaderByHeight(height uint64) *mainblock.Header {
	// Try mainchain cache first
	if cm := w.mainChain.GetChainMainByHeight(height); cm != nil {
		return &mainblock.Header{
			Height:     cm.Height,
			Timestamp:  cm.Timestamp,
			Difficulty: cm.Difficulty,
			Reward:     cm.Reward,
			Id:         cm.Id,
		}
	}

	// Return empty header if not found - this prevents nil pointer crashes
	return &mainblock.Header{}
}

func (w *P2PListener) GetMinimalBlockHeaderByHash(hash types.Hash) *mainblock.Header {
	// Try mainchain cache first
	if cm := w.mainChain.GetChainMainByHash(hash); cm != nil {
		return &mainblock.Header{
			Height:     cm.Height,
			Timestamp:  cm.Timestamp,
			Difficulty: cm.Difficulty,
			Reward:     cm.Reward,
			Id:         cm.Id,
		}
	}

	// Return empty header if not found - this prevents nil pointer crashes
	return &mainblock.Header{}
}

func (w *P2PListener) GetDifficultyByHeight(height uint64) types.Difficulty {
	if cm := w.mainChain.GetChainMainByHeight(height); cm != nil {
		return cm.Difficulty
	}
	return types.ZeroDifficulty
}

func (w *P2PListener) GetMinerDataTip() *p2pooltypes.MinerData {
	// Return cached miner data (updated from P2P peers)
	w.minerDataMutex.RLock()
	defer w.minerDataMutex.RUnlock()
	return w.minerDataTip
}

// Cache interface implementation (used by sidechain for key derivation caching)
func (w *P2PListener) GetBlob(key []byte) (blob []byte, err error) {
	// Not implemented - we don't persist derivation cache
	return nil, nil
}

func (w *P2PListener) SetBlob(key, blob []byte) (err error) {
	// Not implemented - we don't persist derivation cache
	return nil
}

func (w *P2PListener) RemoveBlob(key []byte) (err error) {
	// Not implemented - we don't persist derivation cache
	return nil
}

// Block processing stubs (we don't mine or broadcast)
func (w *P2PListener) UpdateTip(tip *sidechain.PoolBlock) {
	// Passive listener - we don't update tip
}

func (w *P2PListener) BroadcastMoneroBlock(block *mainblock.Block) {
	// Passive listener - we don't broadcast
}

func (w *P2PListener) Broadcast(block *sidechain.PoolBlock) {
	// Passive listener - we don't broadcast
}

func (w *P2PListener) UpdateBlockFound(data *sidechain.ChainMain, block *sidechain.PoolBlock) {
	// Passive listener - we don't track found blocks
}

func (w *P2PListener) UpdateMainData(data *sidechain.ChainMain) {
	// Update cached mainchain tip from P2P peers
	w.mainchainMutex.Lock()
	w.mainchainTip = data
	w.mainchainMutex.Unlock()
}

func (w *P2PListener) UpdateMempoolData(mempool mempool.Mempool) {
	// Passive listener - we don't track mempool
}

func (w *P2PListener) UpdateMinerData(data *p2pooltypes.MinerData) {
	// Update cached miner data from P2P peers
	w.minerDataMutex.Lock()
	w.minerDataTip = data
	w.minerDataMutex.Unlock()

	// Note: We don't call HandleMinerData here to avoid infinite loops
	// The mainchain will get updates through other P2P mechanisms
}

func (w *P2PListener) SubmitBlock(block *mainblock.Block) {
	// Passive listener - we don't submit blocks
}

func (w *P2PListener) Store(block *sidechain.PoolBlock) {
	// This is called by the sidechain when a block is verified and accepted
	if err := w.storeBlockToRedis(block); err != nil {
		utils.Logf("P2PLISTENER", "Failed to store block to Redis: %v", err)
		return
	}

	// Update stats
	w.blocksMutex.Lock()
	w.blocksReceived++
	w.blocksMutex.Unlock()

	templateId := block.SideTemplateId(w.consensus)
	utils.Logf("P2PLISTENER", "Block stored: height=%d id=%s miner=%s",
		block.Side.Height,
		templateId.String()[:8],
		string(block.GetAddress().ToBase58(w.consensus.NetworkType.AddressNetwork())))
}

func (w *P2PListener) ClearCachedBlocks() {
	// Not needed - we manage our own cache
}

// storeBlockToRedis stores a P2Pool block to Redis for the daemon to consume
func (w *P2PListener) storeBlockToRedis(block *sidechain.PoolBlock) error {
	templateId := block.SideTemplateId(w.consensus)

	// Encode block to compact format
	compactData, err := block.MarshalBinaryFlags(false, true)
	if err != nil {
		return err
	}

	// Create cached block structure
	cached := CachedBlock{
		TemplateId:  templateId.String(),
		Height:      block.Side.Height,
		Timestamp:   block.Main.Timestamp,
		MinerAddr:   string(block.GetAddress().ToBase58(w.consensus.NetworkType.AddressNetwork())),
		Difficulty:  block.Side.Difficulty.String(),
		CompactData: compactData,
		CapturedAt:  time.Now().Unix(),
	}

	data, err := json.Marshal(cached)
	if err != nil {
		return err
	}

	// Store in Redis with 7 day TTL
	key := "p2plistener:block:" + templateId.String()
	if err := w.redis.Set(w.ctx, key, data, 7*24*time.Hour).Err(); err != nil {
		return err
	}

	// Add to height index
	heightKey := fmt.Sprintf("p2plistener:height:%d", block.Side.Height)
	w.redis.SAdd(w.ctx, heightKey, templateId.String())
	w.redis.Expire(w.ctx, heightKey, 7*24*time.Hour)

	// Update latest height
	w.redis.Set(w.ctx, "p2plistener:latest:height", block.Side.Height, 0)
	w.redis.Set(w.ctx, "p2plistener:latest:id", templateId.String(), 0)

	return nil
}

// CachedBlock is the structure we store in Redis
type CachedBlock struct {
	TemplateId  string `json:"template_id"`
	Height      uint64 `json:"height"`
	Timestamp   uint64 `json:"timestamp"`
	MinerAddr   string `json:"miner"`
	Difficulty  string `json:"difficulty"`
	CompactData []byte `json:"compact_data"`
	CapturedAt  int64  `json:"captured_at"`
}

// SidechainConfig matches the JSON structure of sidechain_config.json
type SidechainConfig struct {
	Name         string `json:"name"`
	Password     string `json:"password"`
	BlockTime    uint64 `json:"block_time"`
	MinDiff      uint64 `json:"min_diff"`
	PPLNSWindow  int    `json:"pplns_window"`
	UnclePenalty int    `json:"uncle_penalty"`
}

// discoverP2PoolPeers discovers P2Pool nodes by querying Salvium daemon for peer list
// and checking each peer IP for P2Pool on the default port
// detectExternalIP tries to determine the system's external IP address
func detectExternalIP() (string, error) {
	// Try to get the outbound IP by creating a dummy UDP connection
	// This doesn't actually send anything, just determines which interface would be used
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		// Fallback: enumerate network interfaces
		return detectIPFromInterfaces()
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String(), nil
}

// detectIPFromInterfaces gets the first non-loopback IPv4 address from network interfaces
func detectIPFromInterfaces() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}

	for _, iface := range ifaces {
		// Skip loopback and down interfaces
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}

			// Return first non-loopback IPv4 address
			if ip != nil && ip.To4() != nil && !ip.IsLoopback() {
				return ip.String(), nil
			}
		}
	}

	return "", fmt.Errorf("no suitable IP address found")
}

func discoverP2PoolPeers(ctx context.Context, salviumClient *client.Client, listenAddr string) ([]netip.AddrPort, error) {
	// Parse the P2Pool port from listen address
	_, portStr, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid listen address: %w", err)
	}
	p2poolPort, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("invalid port in listen address: %w", err)
	}

	// Get peer list from Salvium daemon
	peerList, err := salviumClient.GetPeerList()
	if err != nil {
		return nil, fmt.Errorf("failed to get peer list from Salvium daemon: %w", err)
	}

	// Combine white and gray peers
	allPeers := append(peerList.WhiteList, peerList.GrayList...)
	if len(allPeers) == 0 {
		return nil, fmt.Errorf("Salvium daemon has no peers")
	}

	utils.Logf("", "Found %d Salvium peers, scanning for P2Pool nodes...", len(allPeers))

	const targetBootstrapPeers = 3 // Stop after finding this many P2Pool nodes
	var discovered []netip.AddrPort

	for _, peer := range allPeers {
		// Stop scanning if we have enough bootstrap peers
		if len(discovered) >= targetBootstrapPeers {
			utils.Logf("", "Found %d P2Pool nodes, stopping scan (will discover more via P2P)", len(discovered))
			break
		}

		// Parse host:port from peer address
		host, _, err := net.SplitHostPort(peer.Host)
		if err != nil {
			// Try just using it as a host
			host = peer.Host
		}

		addr, err := netip.ParseAddr(host)
		if err != nil {
			continue
		}

		// Try to connect to P2Pool port on this host
		p2poolAddr := netip.AddrPortFrom(addr, uint16(p2poolPort))

		// Quick TCP connection test (just check if port is open)
		// Don't do full P2P handshake here - just see if something is listening
		testConn, err := net.DialTimeout("tcp", p2poolAddr.String(), 2*time.Second)
		if err == nil {
			testConn.Close()
			discovered = append(discovered, p2poolAddr)
			utils.Logf("", "Found P2Pool node: %s", p2poolAddr.String())
		}
	}

	if len(discovered) == 0 {
		return nil, fmt.Errorf("no P2Pool nodes found on Salvium peer IPs")
	}

	return discovered, nil
}

// loadSidechainConfig reads sidechain_config.json and creates a Consensus
func loadSidechainConfig(path string) (*sidechain.Consensus, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Strip C-style comments (// ...) from JSON
	cleaned := stripJSONComments(string(data))

	var config SidechainConfig
	if err := json.Unmarshal([]byte(cleaned), &config); err != nil {
		return nil, fmt.Errorf("failed to parse config JSON: %w", err)
	}

	// Use default network type (mainnet)
	networkType := sidechain.ConsensusDefault.NetworkType

	// Create consensus from config using the proper constructor
	consensus := sidechain.NewConsensus(
		networkType,
		config.Name,
		config.Password,
		"", // extra
		config.BlockTime,
		config.MinDiff,
		uint64(config.PPLNSWindow),
		uint64(config.UnclePenalty),
	)

	utils.Logf("", "Loaded sidechain config: %s", config.Name)

	return consensus, nil
}

// stripJSONComments removes C-style // comments from JSON
func stripJSONComments(jsonStr string) string {
	lines := strings.Split(jsonStr, "\n")
	var cleaned []string

	for _, line := range lines {
		// Find // comment marker
		if idx := strings.Index(line, "//"); idx >= 0 {
			// Keep everything before the comment
			line = line[:idx]
		}
		// Keep the line if it's not empty after trimming
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			cleaned = append(cleaned, line)
		}
	}

	return strings.Join(cleaned, "\n")
}

// loadPeerCache loads cached peer addresses from a file
func loadPeerCache(filePath string) ([]netip.AddrPort, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var peers []netip.AddrPort
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		addr, err := netip.ParseAddrPort(line)
		if err != nil {
			continue // Skip invalid entries
		}
		peers = append(peers, addr)
	}

	return peers, nil
}

// savePeerCache saves peer addresses to a file
func savePeerCache(filePath string, peers []netip.AddrPort) error {
	var lines []string
	for _, peer := range peers {
		lines = append(lines, peer.String())
	}

	data := strings.Join(lines, "\n") + "\n"
	return os.WriteFile(filePath, []byte(data), 0644)
}
