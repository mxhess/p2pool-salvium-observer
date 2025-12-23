// Package p2pool provides types for parsing p2pool-salvium block data.
// Based directly on ~/p2pool-salvium/src/ C++ implementation.
package p2pool

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
)

// CacheReader reads P2Pool shares from the Redis cache.
// The cache is populated by p2pool-salvium C++ using block_cache.cpp.
//
// Redis format (from block_cache.cpp):
// - Hash key: "cache"
// - Fields: "0", "1", "2", ... (circular buffer indices)
// - Values: sidechain_id (32 bytes) + size (4 bytes) + block_data
type CacheReader struct {
	client   *redis.Client
	cacheKey string // Redis hash key, default "cache"
}

// NewCacheReader creates a new cache reader.
// cacheKey is the Redis hash key used by block_cache.cpp (default "p2pool:cache").
func NewCacheReader(client *redis.Client, cacheKey string) *CacheReader {
	if cacheKey == "" {
		cacheKey = "p2pool:cache"
	}
	return &CacheReader{
		client:   client,
		cacheKey: cacheKey,
	}
}

// GetShareByIndex retrieves a share by its cache index (0-4607).
// This is the raw access method matching block_cache.cpp's circular buffer.
func (c *CacheReader) GetShareByIndex(ctx context.Context, index int) (*Share, error) {
	field := fmt.Sprintf("%d", index)
	data, err := c.client.HGet(ctx, c.cacheKey, field).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, fmt.Errorf("cache slot %d empty", index)
		}
		return nil, fmt.Errorf("redis hget: %w", err)
	}

	return ParseCacheEntry(data)
}

// LoadAllShares loads all shares from the cache hash into a map keyed by sidechain ID.
// This is the primary method for building the share map for parent chain walking.
// The C++ cache uses a circular buffer of NUM_BLOCKS=4608 entries.
func (c *CacheReader) LoadAllShares(ctx context.Context) (map[Hash]*Share, error) {
	shares := make(map[Hash]*Share)

	// Use HGETALL to fetch all entries at once
	result, err := c.client.HGetAll(ctx, c.cacheKey).Result()
	if err != nil {
		return nil, fmt.Errorf("redis hgetall: %w", err)
	}

	for _, value := range result {
		data := []byte(value)
		share, err := ParseCacheEntry(data)
		if err != nil {
			// Skip entries that fail to parse
			continue
		}
		shares[share.SidechainId] = share
	}

	return shares, nil
}

// GetShare retrieves a share by its sidechain ID from a preloaded map.
// For efficiency, use LoadAllShares() once and then look up in the returned map.
func (c *CacheReader) GetShare(ctx context.Context, sidechainIdHex string) (*Share, error) {
	// This method is inefficient - it loads all shares to find one.
	// Prefer using LoadAllShares() and looking up in the map.
	sidechainIdHex = strings.ToLower(strings.TrimPrefix(sidechainIdHex, "0x"))

	targetId, err := HashFromHex(sidechainIdHex)
	if err != nil {
		return nil, err
	}

	shares, err := c.LoadAllShares(ctx)
	if err != nil {
		return nil, err
	}

	share, ok := shares[targetId]
	if !ok {
		return nil, fmt.Errorf("share not found: %s", sidechainIdHex)
	}

	return share, nil
}

// GetShareByHash retrieves a share by its sidechain ID from a preloaded map.
// For efficiency, use LoadAllShares() and look up directly.
func (c *CacheReader) GetShareByHash(ctx context.Context, sidechainId Hash) (*Share, error) {
	return c.GetShare(ctx, sidechainId.String())
}

// ScanShares iterates over all shares in the cache.
// Calls the callback for each share. Returns early if callback returns error.
func (c *CacheReader) ScanShares(ctx context.Context, callback func(*Share) error) error {
	shares, err := c.LoadAllShares(ctx)
	if err != nil {
		return err
	}

	for _, share := range shares {
		if err := callback(share); err != nil {
			return err
		}
	}

	return nil
}

// BuildShareMap is an alias for LoadAllShares for compatibility.
func (c *CacheReader) BuildShareMap(ctx context.Context) (map[Hash]*Share, error) {
	return c.LoadAllShares(ctx)
}

// WalkParentChain walks the parent chain from the given share.
// Calls callback for each share including the starting share.
// Stops when reaching genesis (no parent) or maxDepth.
func (c *CacheReader) WalkParentChain(ctx context.Context, share *Share, maxDepth int, callback func(*Share, int) error) error {
	current := share
	depth := 0

	for current != nil && depth < maxDepth {
		if err := callback(current, depth); err != nil {
			return err
		}

		if current.Parent.IsZero() {
			break // Genesis block
		}

		parent, err := c.GetShareByHash(ctx, current.Parent)
		if err != nil {
			// Parent not in cache - stop walking
			break
		}

		current = parent
		depth++
	}

	return nil
}

// WalkParentChainFromMap walks the parent chain using an in-memory share map.
// More efficient than WalkParentChain when you have many shares to process.
func WalkParentChainFromMap(shares map[Hash]*Share, share *Share, maxDepth int, callback func(*Share, int) error) error {
	current := share
	depth := 0

	for current != nil && depth < maxDepth {
		if err := callback(current, depth); err != nil {
			return err
		}

		if current.Parent.IsZero() {
			break // Genesis block
		}

		parent, ok := shares[current.Parent]
		if !ok {
			break // Parent not in map
		}

		current = parent
		depth++
	}

	return nil
}

// HashFromHex parses a hex string into a Hash.
func HashFromHex(s string) (Hash, error) {
	s = strings.ToLower(strings.TrimPrefix(s, "0x"))
	if len(s) != 64 {
		return Hash{}, fmt.Errorf("invalid hash length: %d (expected 64)", len(s))
	}

	var h Hash
	_, err := hex.Decode(h[:], []byte(s))
	if err != nil {
		return Hash{}, fmt.Errorf("hex decode: %w", err)
	}

	return h, nil
}
