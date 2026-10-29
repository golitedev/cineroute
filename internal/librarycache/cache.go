// Package librarycache holds immutable library snapshots for discovery badges.
// Download safety checks must continue to read the filesystem directly.
package librarycache

import (
	"context"
	"sync"
	"time"
)

const RefreshInterval = 5 * time.Minute

// Cache publishes complete snapshots. Refreshing never locks out warm readers.
// The first read loads synchronously, including when used without Start in tests.
type Cache[T any] struct {
	load      func() T
	refreshMu sync.Mutex
	mu        sync.RWMutex
	value     T
	ready     bool
	wake      chan struct{}
	lifecycle sync.Mutex
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

func New[T any](load func() T) *Cache[T] {
	return &Cache[T]{load: load, wake: make(chan struct{}, 1)}
}

func (c *Cache[T]) Get() T {
	c.mu.RLock()
	value, ready := c.value, c.ready
	c.mu.RUnlock()
	if ready {
		return value
	}
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	c.mu.RLock()
	value, ready = c.value, c.ready
	c.mu.RUnlock()
	if !ready {
		value = c.publish()
	}
	return value
}

// Refresh rebuilds the snapshot; concurrent refreshes are serialized.
func (c *Cache[T]) Refresh() {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	c.publish()
}

func (c *Cache[T]) publish() T {
	value := c.load()
	c.mu.Lock()
	c.value, c.ready = value, true
	c.mu.Unlock()
	return value
}

// Invalidate coalesces change notifications for the background worker.
func (c *Cache[T]) Invalidate() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Cache[T]) Start(parent context.Context) {
	c.start(parent, RefreshInterval)
}

func (c *Cache[T]) start(parent context.Context, interval time.Duration) {
	c.lifecycle.Lock()
	defer c.lifecycle.Unlock()
	if c.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.Get()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.Refresh()
			case <-c.wake:
				c.Refresh()
			}
		}
	}()
}

func (c *Cache[T]) Stop() {
	c.lifecycle.Lock()
	defer c.lifecycle.Unlock()
	if c.cancel != nil {
		c.cancel()
		c.wg.Wait()
		c.cancel = nil
	}
}
