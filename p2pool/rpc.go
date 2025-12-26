// Package p2pool provides types for parsing p2pool-salvium block data.
package p2pool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// SalviumClient is a minimal RPC client for Salvium daemon.
// Supports multiple endpoints with automatic fallback.
type SalviumClient struct {
	endpoints    []*url.URL
	currentIndex int
	mu           sync.RWMutex
	http         *http.Client
}

// NewSalviumClient creates a new Salvium RPC client.
// addresses can be a single URL or comma-separated list for fallback.
// e.g., "http://127.0.0.1:19081" or "http://node1:19081,http://node2:19081"
func NewSalviumClient(addresses string) (*SalviumClient, error) {
	parts := strings.Split(addresses, ",")
	endpoints := make([]*url.URL, 0, len(parts))

	for _, addr := range parts {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		parsed, err := url.Parse(addr)
		if err != nil {
			return nil, fmt.Errorf("parse address %q: %w", addr, err)
		}
		endpoints = append(endpoints, parsed)
	}

	if len(endpoints) == 0 {
		return nil, fmt.Errorf("no valid endpoints provided")
	}

	return &SalviumClient{
		endpoints:    endpoints,
		currentIndex: 0,
		http: &http.Client{
			Timeout: 10 * time.Second, // Shorter timeout for faster failover
		},
	}, nil
}

// currentEndpoint returns the current active endpoint.
func (c *SalviumClient) currentEndpoint() *url.URL {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.endpoints[c.currentIndex]
}

// failover switches to the next endpoint in the list.
// Returns true if there are more endpoints to try.
func (c *SalviumClient) failover() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.currentIndex = (c.currentIndex + 1) % len(c.endpoints)
	return true // Always return true since we cycle
}

// EndpointCount returns the number of configured endpoints.
func (c *SalviumClient) EndpointCount() int {
	return len(c.endpoints)
}

// CurrentEndpoint returns the currently active endpoint URL as string.
func (c *SalviumClient) CurrentEndpoint() string {
	return c.currentEndpoint().String()
}

// JSON-RPC envelope types
type rpcRequest struct {
	ID      string      `json:"id"`
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type rpcResponse struct {
	ID      string          `json:"id"`
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// jsonrpc makes a JSON-RPC call to the Salvium daemon with automatic failover.
func (c *SalviumClient) jsonrpc(ctx context.Context, method string, params interface{}, result interface{}) error {
	reqBody, err := json.Marshal(&rpcRequest{
		ID:      "0",
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	// Try each endpoint until one works
	var lastErr error
	for attempt := 0; attempt < len(c.endpoints); attempt++ {
		endpoint := c.currentEndpoint()
		address := *endpoint
		address.Path = "/json_rpc"

		req, err := http.NewRequestWithContext(ctx, "POST", address.String(), bytes.NewReader(reqBody))
		if err != nil {
			lastErr = fmt.Errorf("create request: %w", err)
			c.failover()
			continue
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", endpoint.Host, err)
			Logf("RPC", "Endpoint %s failed: %v, trying next...", endpoint.Host, err)
			c.failover()
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			resp.Body.Close()
			lastErr = fmt.Errorf("%s: non-2xx status: %d", endpoint.Host, resp.StatusCode)
			Logf("RPC", "Endpoint %s returned status %d, trying next...", endpoint.Host, resp.StatusCode)
			c.failover()
			continue
		}

		var rpcResp rpcResponse
		if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
			resp.Body.Close()
			lastErr = fmt.Errorf("%s: decode response: %w", endpoint.Host, err)
			c.failover()
			continue
		}
		resp.Body.Close()

		if rpcResp.Error != nil && (rpcResp.Error.Code != 0 || rpcResp.Error.Message != "") {
			return fmt.Errorf("rpc error: code=%d message=%s", rpcResp.Error.Code, rpcResp.Error.Message)
		}

		if result != nil && len(rpcResp.Result) > 0 {
			if err := json.Unmarshal(rpcResp.Result, result); err != nil {
				return fmt.Errorf("unmarshal result: %w", err)
			}
		}

		return nil
	}

	return fmt.Errorf("all endpoints failed, last error: %w", lastErr)
}

// GetInfoResult contains the response from get_info RPC call.
// Only includes fields we actually need.
type GetInfoResult struct {
	Height          uint64 `json:"height"`
	Difficulty      uint64 `json:"difficulty"`
	DifficultyTop64 uint64 `json:"difficulty_top64"`
	WideDifficulty  string `json:"wide_difficulty"` // 128-bit as hex string

	TopBlockHash string `json:"top_block_hash"`
	TxCount      uint64 `json:"tx_count"`
	TxPoolSize   uint64 `json:"tx_pool_size"`
	Version      string `json:"version"`
	Synchronized bool   `json:"synchronized"`

	Status string `json:"status"`
}

// GetInfo retrieves general information about the Salvium node and network.
// Most importantly, this gives us the current mainchain difficulty.
func (c *SalviumClient) GetInfo(ctx context.Context) (*GetInfoResult, error) {
	result := &GetInfoResult{}
	if err := c.jsonrpc(ctx, "get_info", nil, result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetDifficulty is a convenience method that returns the mainchain difficulty as our Difficulty type.
func (c *SalviumClient) GetDifficulty(ctx context.Context) (Difficulty, error) {
	info, err := c.GetInfo(ctx)
	if err != nil {
		return Difficulty{}, err
	}
	return Difficulty{Lo: info.Difficulty, Hi: info.DifficultyTop64}, nil
}

// BlockHeader contains block header information.
type BlockHeader struct {
	Height     uint64 `json:"height"`
	Timestamp  int64  `json:"timestamp"`
	Difficulty uint64 `json:"difficulty"`
	Hash       string `json:"hash"`
	PrevHash   string `json:"prev_hash"`
	Reward     uint64 `json:"reward"`
	BlockSize  uint64 `json:"block_size"`
	NumTxes    uint   `json:"num_txes"`
}

// GetLastBlockHeaderResult contains the response from get_last_block_header RPC call.
type GetLastBlockHeaderResult struct {
	BlockHeader BlockHeader `json:"block_header"`
	Status      string      `json:"status"`
}

// GetLastBlockHeader retrieves the header of the most recent block.
func (c *SalviumClient) GetLastBlockHeader(ctx context.Context) (*GetLastBlockHeaderResult, error) {
	result := &GetLastBlockHeaderResult{}
	if err := c.jsonrpc(ctx, "get_last_block_header", nil, result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetBlockHeaderByHeightResult contains the response from get_block_header_by_height RPC call.
type GetBlockHeaderByHeightResult struct {
	BlockHeader BlockHeader `json:"block_header"`
	Status      string      `json:"status"`
}

// GetBlockHeaderByHeight retrieves the header of a block at a specific height.
func (c *SalviumClient) GetBlockHeaderByHeight(ctx context.Context, height uint64) (*GetBlockHeaderByHeightResult, error) {
	result := &GetBlockHeaderByHeightResult{}
	params := map[string]uint64{"height": height}
	if err := c.jsonrpc(ctx, "get_block_header_by_height", params, result); err != nil {
		return nil, err
	}
	return result, nil
}

// CoinbaseOutput represents a single output in a coinbase transaction
type CoinbaseOutput struct {
	Amount uint64 `json:"amount"`
}

// GetBlockResult contains the response from get_block RPC call.
type GetBlockResult struct {
	BlockHeader BlockHeader `json:"block_header"`
	Json        string      `json:"json"` // Block JSON as string
	Status      string      `json:"status"`
}

// BlockJson is the parsed block JSON structure
type BlockJson struct {
	MinerTx struct {
		Vout []struct {
			Amount uint64 `json:"amount"`
		} `json:"vout"`
	} `json:"miner_tx"`
}

// GetBlock retrieves a full block by height including coinbase outputs.
func (c *SalviumClient) GetBlock(ctx context.Context, height uint64) (*GetBlockResult, error) {
	result := &GetBlockResult{}
	params := map[string]uint64{"height": height}
	if err := c.jsonrpc(ctx, "get_block", params, result); err != nil {
		return nil, err
	}
	return result, nil
}

// CoinbaseTxSumResult contains the response from get_coinbase_tx_sum RPC call.
type CoinbaseTxSumResult struct {
	EmissionAmount      uint64 `json:"emission_amount"`
	EmissionAmountTop64 uint64 `json:"emission_amount_top64"`
	FeeAmount           uint64 `json:"fee_amount"`
	FeeAmountTop64      uint64 `json:"fee_amount_top64"`
	Status              string `json:"status"`
}

// GetCoinbaseTxSum retrieves the total emission (supply) and fees for blocks from height 0 to the given height.
func (c *SalviumClient) GetCoinbaseTxSum(ctx context.Context, height uint64) (*CoinbaseTxSumResult, error) {
	result := &CoinbaseTxSumResult{}
	params := map[string]uint64{
		"height": 0,
		"count":  height, // Sum blocks 0 through height-1 (count must not exceed chain length)
	}
	if err := c.jsonrpc(ctx, "get_coinbase_tx_sum", params, result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetTotalSupply returns the total circulating supply in atomic units.
func (c *SalviumClient) GetTotalSupply(ctx context.Context, height uint64) (uint64, error) {
	sum, err := c.GetCoinbaseTxSum(ctx, height)
	if err != nil {
		return 0, err
	}
	// For now, just use the lower 64 bits - Salvium supply fits in uint64
	// Full 128-bit would be: (sum.EmissionAmountTop64 << 64) | sum.EmissionAmount
	return sum.EmissionAmount, nil
}

// SupplyEntry represents a single supply category from get_supply_info
type SupplyEntry struct {
	Amount        string `json:"amount"`
	CurrencyLabel string `json:"currency_label"`
}

// SupplyInfoResult contains the response from get_supply_info RPC call.
type SupplyInfoResult struct {
	Height      uint64        `json:"height"`
	SupplyTally []SupplyEntry `json:"supply_tally"`
	Status      string        `json:"status"`
}

// SupplyInfo contains parsed supply information
type SupplyInfo struct {
	Height      uint64 // Chain height
	Circulating uint64 // SAL1 - liquid, spendable coins
	Staked      uint64 // STAKE - locked in staking
	Burned      uint64 // BURN - permanently removed
}

// GetSupplyInfo retrieves detailed supply breakdown (circulating, staked, burned).
func (c *SalviumClient) GetSupplyInfo(ctx context.Context) (*SupplyInfo, error) {
	result := &SupplyInfoResult{}
	if err := c.jsonrpc(ctx, "get_supply_info", nil, result); err != nil {
		return nil, err
	}

	info := &SupplyInfo{Height: result.Height}
	for _, entry := range result.SupplyTally {
		amount, _ := parseUint64(entry.Amount)
		switch entry.CurrencyLabel {
		case "SAL1":
			info.Circulating = amount
		case "STAKE":
			info.Staked = amount
		case "BURN":
			info.Burned = amount
		}
	}
	return info, nil
}

// parseUint64 parses a string to uint64, returns 0 on error
func parseUint64(s string) (uint64, error) {
	var v uint64
	_, err := fmt.Sscanf(s, "%d", &v)
	return v, err
}

// GetCoinbaseOutputs retrieves the coinbase output amounts for a block at the given height.
func (c *SalviumClient) GetCoinbaseOutputs(ctx context.Context, height uint64) ([]uint64, error) {
	block, err := c.GetBlock(ctx, height)
	if err != nil {
		return nil, err
	}

	var blockJson BlockJson
	if err := json.Unmarshal([]byte(block.Json), &blockJson); err != nil {
		return nil, fmt.Errorf("parse block json: %w", err)
	}

	amounts := make([]uint64, len(blockJson.MinerTx.Vout))
	for i, out := range blockJson.MinerTx.Vout {
		amounts[i] = out.Amount
	}

	return amounts, nil
}
