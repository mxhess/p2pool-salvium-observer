package main

import (
	"sync"
	"time"
)

// Cache TTL constants
const (
	CacheTTLShort  = 10 * time.Second  // pool_info, recent blocks, miner info
	CacheTTLMedium = 60 * time.Second  // found_blocks with larger limits
	CacheTTLLong   = 120 * time.Second // PPLNS window data
)

var cache = make(map[string]*cachedEntry)
var cacheLock sync.RWMutex

type cachedEntry struct {
	expires time.Time
	data    []byte
}

// cacheGet returns cached data if it exists and hasn't expired
func cacheGet(key string) ([]byte, bool) {
	cacheLock.RLock()
	defer cacheLock.RUnlock()

	entry, ok := cache[key]
	if !ok {
		return nil, false
	}

	if time.Now().After(entry.expires) {
		return nil, false
	}

	return entry.data, true
}

// cacheSet stores data with the specified TTL
func cacheSet(key string, data []byte, ttl time.Duration) {
	cacheLock.Lock()
	defer cacheLock.Unlock()

	cache[key] = &cachedEntry{
		expires: time.Now().Add(ttl),
		data:    data,
	}
}

// cacheCleanup removes expired entries (call periodically)
func cacheCleanup() {
	cacheLock.Lock()
	defer cacheLock.Unlock()

	now := time.Now()
	for key, entry := range cache {
		if now.After(entry.expires) {
			delete(cache, key)
		}
	}
}

// startCacheCleanup starts a background goroutine to clean expired entries
func startCacheCleanup(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			cacheCleanup()
		}
	}()
}
