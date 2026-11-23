// Package librarycache holds immutable library snapshots for discovery badges.
// Download safety checks must continue to read the filesystem directly.
package librarycache

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const RefreshInterval = 24 * time.Hour

// Cache publishes complete snapshots. Refreshing never locks out warm readers.
// The first read restores a saved snapshot or loads the library synchronously.
type Cache[T any] struct {
	load      func() T
	disk      snapshotFile[T]
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

// NewPersistent saves snapshots for reuse by later processes with the same source.
func NewPersistent[T any](load func() T, path, source string) *Cache[T] {
	c := New(load)
	c.disk = snapshotFile[T]{path: path, source: source}
	return c
}

// Peek returns the current snapshot without starting or waiting for a scan.
func (c *Cache[T]) Peek() (T, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.value, c.ready
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
		if saved, ok := c.disk.restore(); ok {
			c.publish(saved)
			return saved
		}
		value = c.refresh()
	}
	return value
}

// Restore loads a saved snapshot without running the library scanner.
func (c *Cache[T]) Restore() {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	if _, ready := c.Peek(); !ready {
		if saved, ok := c.disk.restore(); ok {
			c.publish(saved)
		}
	}
}

// Refresh rebuilds the snapshot; concurrent refreshes are serialized.
func (c *Cache[T]) Refresh() {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	c.refresh()
}

func (c *Cache[T]) refresh() T {
	value := c.load()
	c.publish(value)
	if err := c.disk.save(value); err != nil {
		slog.Warn("cannot save library cache", "path", c.disk.path, "err", err)
	}
	return value
}

func (c *Cache[T]) publish(value T) {
	c.mu.Lock()
	c.value, c.ready = value, true
	c.mu.Unlock()
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
