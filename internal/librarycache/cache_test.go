package librarycache

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestColdReadersShareOneLoadAndWarmReadersUseSnapshot(t *testing.T) {
	var calls atomic.Int32
	c := New(func() int { return int(calls.Add(1)) })
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c.Get() != 1 {
				t.Error("cold readers did not share a snapshot")
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("loaded %d times", calls.Load())
	}
	c.Refresh()
	if c.Get() != 2 || calls.Load() != 2 {
		t.Fatal("explicit refresh did not publish")
	}
}

func TestWarmReadersDoNotBlockDuringRefresh(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	c := New(func() int {
		n := calls.Add(1)
		if n == 2 {
			close(entered)
			<-release
		}
		return int(n)
	})
	c.Get()
	done := make(chan struct{})
	go func() { c.Refresh(); close(done) }()
	<-entered
	if c.Get() != 1 {
		t.Fatal("reader lost previous snapshot")
	}
	close(release)
	<-done
	if c.Get() != 2 {
		t.Fatal("refresh not published")
	}
}

func TestNotificationsAndPeriodicRefreshStopWithLifecycle(t *testing.T) {
	calls := make(chan struct{}, 20)
	c := New(func() int { calls <- struct{}{}; return 1 })
	wait := func() {
		t.Helper()
		select {
		case <-calls:
		case <-time.After(time.Second):
			t.Fatal("refresh did not run")
		}
	}
	c.Start(context.Background())
	wait()
	c.Invalidate()
	wait()
	c.Stop()
	// Use a short interval to verify the same loop used in production.
	c.start(context.Background(), time.Millisecond)
	wait()
	c.Stop()
	for len(calls) > 0 {
		<-calls
	}
	c.Invalidate()
	select {
	case <-calls:
		t.Fatal("refresh ran after stop")
	case <-time.After(10 * time.Millisecond):
	}
}
