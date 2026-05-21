package extraction

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRateLimiter_MaxConcurrency(t *testing.T) {
	rl := NewRateLimiter(2, 0) // no interval, only concurrency

	var maxConcurrent atomic.Int32
	var current atomic.Int32
	var wg sync.WaitGroup
	const n = 6

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rl.Acquire(context.Background())
			defer rl.Release()

			v := current.Add(1)
			if v > maxConcurrent.Load() {
				maxConcurrent.Store(v)
			}
			time.Sleep(50 * time.Millisecond)
			current.Add(-1)
		}()
	}
	wg.Wait()

	if max := maxConcurrent.Load(); max > 2 {
		t.Fatalf("max concurrent: want ≤2, got %d", max)
	}
	if max := maxConcurrent.Load(); max == 0 {
		t.Fatal("max concurrent should be >0, got 0")
	}
}

func TestRateLimiter_MinInterval(t *testing.T) {
	rl := NewRateLimiter(1, 100*time.Millisecond)

	// First call: no delay (lastStart is zero, always passes)
	rl.Acquire(context.Background())
	rl.Release()

	// Second call: must wait at least 100ms since last start
	start := time.Now()
	rl.Acquire(context.Background())
	elapsed := time.Since(start)
	rl.Release()

	if elapsed < 90*time.Millisecond {
		t.Fatalf("min interval not enforced: waited %v, want ≥100ms", elapsed)
	}
}

func TestRateLimiter_ContextCancel(t *testing.T) {
	rl := NewRateLimiter(1, 0)

	// Fill the slot
	rl.Acquire(context.Background())

	// Try to acquire with cancelled context — must return immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- rl.Acquire(ctx)
	}()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error from cancelled context, got nil")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout: context cancellation did not unblock acquire")
	}

	rl.Release()
}

func TestRateLimiter_NoStarvation(t *testing.T) {
	rl := NewRateLimiter(1, 5*time.Millisecond)

	var mu sync.Mutex
	order := make([]int, 0)
	var wg sync.WaitGroup
	const n = 5

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			rl.Acquire(context.Background())
			defer rl.Release()
			mu.Lock()
			order = append(order, id)
			mu.Unlock()
			time.Sleep(1 * time.Millisecond)
		}(i)
	}
	wg.Wait()

	if len(order) != n {
		t.Fatalf("expected %d entries, got %d", n, len(order))
	}
}
