package cmap

import (
	"iter"
	"maps"
	"sync"
)

type ConcurrentMap[K comparable, V any] struct {
	mu sync.RWMutex
	m  map[K]V
}

func New[K comparable, V any](sizeHint ...int) *ConcurrentMap[K, V] {
	var size int
	if len(sizeHint) > 0 {
		size = sizeHint[0]
	}
	return &ConcurrentMap[K, V]{m: make(map[K]V, size)}
}

func (c *ConcurrentMap[K, V]) Get(k K) (V, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.m[k]
	return v, ok
}

func (c *ConcurrentMap[K, V]) Set(k K, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[k] = v
}

func (c *ConcurrentMap[K, V]) Delete(k K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, k)
}

func (c *ConcurrentMap[K, V]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.m)
}

func (c *ConcurrentMap[K, V]) All() iter.Seq2[K, V] {
	// Only hold the lock long enough to create a snapshot; prevents deadlocking.
	// Clone op should be fine from a time perspective given the low, hard cap on map size
	// (6 players).
	c.mu.RLock()
	snapshot := maps.Clone(c.m)
	c.mu.RUnlock()

	return func(yield func(K, V) bool) {
		for k, v := range snapshot {
			if !yield(k, v) {
				return
			}
		}
	}
}

func (c *ConcurrentMap[K, V]) Keys() iter.Seq[K] {
	// See `All()`
	c.mu.RLock()
	snapshot := maps.Clone(c.m)
	c.mu.RUnlock()

	return func(yield func(K) bool) {
		for k := range snapshot {
			if !yield(k) {
				return
			}
		}
	}
}

func (c *ConcurrentMap[K, V]) Values() iter.Seq[V] {
	// See `All()`
	c.mu.RLock()
	snapshot := maps.Clone(c.m)
	c.mu.RUnlock()

	return func(yield func(V) bool) {
		for _, v := range snapshot {
			if !yield(v) {
				return
			}
		}
	}
}
