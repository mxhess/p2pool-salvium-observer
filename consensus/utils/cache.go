package utils

import (
	"github.com/hashicorp/golang-lru/v2"
	"sync"
	"sync/atomic"
)

type Cache[K comparable, T any] interface {
	Get(key K) (value T, ok bool)
	Set(key K, value T)
	Delete(key K)
	Clear()
	Stats() (hits, misses uint64)
}

type LRUCache[K comparable, T any] struct {
	values       atomic.Pointer[lru.Cache[K, T]]
	hits, misses atomic.Uint64
	size         int
}

func NewLRUCache[K comparable, T any](size int) *LRUCache[K, T] {
	c := &LRUCache[K, T]{
		size: size,
	}
	c.Clear()
	return c
}

func (c *LRUCache[K, T]) Get(key K) (value T, ok bool) {
	if value, ok = c.values.Load().Get(key); ok {
		c.hits.Add(1)
		return value, true
	} else {
		c.misses.Add(1)
		return value, false
	}
}

func (c *LRUCache[K, T]) Set(key K, value T) {
	c.values.Load().Add(key, value)
}

func (c *LRUCache[K, T]) Delete(key K) {
	c.values.Load().Remove(key)
}

func (c *LRUCache[K, T]) Clear() {
	cache, err := lru.New[K, T](c.size)
	if err != nil {
		panic(err)
	}
	c.values.Store(cache)
}

func (c *LRUCache[K, T]) Stats() (hits, misses uint64) {
	return c.hits.Load(), c.misses.Load()
}

type MapCache[K comparable, T any] struct {
	lock         sync.RWMutex
	values       map[K]T
	hits, misses atomic.Uint64
	size         int
}

func NewMapCache[K comparable, T any](preAllocateSize int) *MapCache[K, T] {
	return &MapCache[K, T]{
		values: make(map[K]T, preAllocateSize),
		size:   preAllocateSize,
	}
}

func (m *MapCache[K, T]) Get(key K) (value T, ok bool) {
	m.lock.RLock()
	defer m.lock.RUnlock()
	value, ok = m.values[key]
	if ok {
		m.hits.Add(1)
	} else {
		m.misses.Add(1)
	}
	return value, ok
}

func (m *MapCache[K, T]) Set(key K, value T) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.values[key] = value
}

func (m *MapCache[K, T]) Delete(key K) {
	m.lock.Lock()
	defer m.lock.Unlock()
	delete(m.values, key)
}

func (m *MapCache[K, T]) Clear() {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.values = make(map[K]T, m.size)
}

func (m *MapCache[K, T]) Stats() (hits, misses uint64) {
	m.lock.Lock()
	defer m.lock.Unlock()
	return m.hits.Load(), m.misses.Load()
}

type NilCache[K comparable, T any] struct {
}

func NewNilCache[K comparable, T any]() NilCache[K, T] {
	return NilCache[K, T]{}
}

func (m NilCache[K, T]) Get(key K) (value T, ok bool) {
	return value, false
}

func (m NilCache[K, T]) Set(key K, value T) {

}

func (m NilCache[K, T]) Delete(key K) {

}

func (m NilCache[K, T]) Clear() {

}

func (m NilCache[K, T]) Stats() (hits, misses uint64) {
	return 0, 0
}
