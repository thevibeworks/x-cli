package api

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestTokenBucketInstantBurst(t *testing.T) {
	th := NewThrottle(Defaults{})
	ctx := context.Background()

	start := time.Now()
	for i := 0; i < 5; i++ {
		if err := th.AwaitRead(ctx, "burst", 50, 5); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("full burst took %v (expected near-instant)", elapsed)
	}
}

func TestTokenBucketPacesAfterBurst(t *testing.T) {
	th := NewThrottle(Defaults{})
	ctx := context.Background()

	// burst=1 at 20rps → second token available in ~50ms
	if err := th.AwaitRead(ctx, "paced", 20, 1); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := th.AwaitRead(ctx, "paced", 20, 1); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed < 20*time.Millisecond {
		t.Errorf("second token not throttled, elapsed=%v", elapsed)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("second token took too long, elapsed=%v", elapsed)
	}
}

func TestTokenBucketContextCancel(t *testing.T) {
	th := NewThrottle(Defaults{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	// First token is free (burst=1).
	if err := th.AwaitRead(ctx, "slow", 0.1, 1); err != nil {
		t.Fatal(err)
	}
	// Second token needs ~10s — context should cancel first.
	err := th.AwaitRead(ctx, "slow", 0.1, 1)
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestMutationGapEnforced(t *testing.T) {
	th := NewThrottle(Defaults{})
	ctx := context.Background()

	if err := th.AwaitMutation(ctx, 30*time.Millisecond, 35*time.Millisecond, 100); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := th.AwaitMutation(ctx, 30*time.Millisecond, 35*time.Millisecond, 100); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed < 30*time.Millisecond {
		t.Errorf("mutation gap not enforced, elapsed=%v", elapsed)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("mutation gap too long, elapsed=%v", elapsed)
	}
}

func TestMutationJitterWithinBounds(t *testing.T) {
	th := NewThrottle(Defaults{})
	ctx := context.Background()

	if err := th.AwaitMutation(ctx, 20*time.Millisecond, 60*time.Millisecond, 100); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := th.AwaitMutation(ctx, 20*time.Millisecond, 60*time.Millisecond, 100); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed < 20*time.Millisecond {
		t.Errorf("elapsed %v below min jitter", elapsed)
	}
	if elapsed > 300*time.Millisecond {
		t.Errorf("elapsed %v above max jitter ceiling", elapsed)
	}
}

func TestMutationDailyCapExhaustion(t *testing.T) {
	th := NewThrottle(Defaults{})
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if err := th.AwaitMutation(ctx, time.Nanosecond, time.Nanosecond, 2); err != nil {
			t.Fatalf("mutation %d: %v", i, err)
		}
	}
	err := th.AwaitMutation(ctx, time.Nanosecond, time.Nanosecond, 2)
	if err == nil {
		t.Fatal("expected budget exhausted error")
	}
	var budgetErr *BudgetExhaustedError
	if !errors.As(err, &budgetErr) {
		t.Errorf("want *BudgetExhaustedError, got %T: %v", err, err)
	}
}

func TestMutationContextCancel(t *testing.T) {
	th := NewThrottle(Defaults{})
	_ = th.AwaitMutation(context.Background(), time.Nanosecond, time.Nanosecond, 100)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	// Second call needs to wait at least 1s; ctx will cancel.
	err := th.AwaitMutation(ctx, time.Second, 2*time.Second, 100)
	if err == nil {
		t.Fatal("expected cancellation")
	}
}

func TestObserve429SetsAutopause(t *testing.T) {
	th := NewThrottle(Defaults{})
	futureReset := time.Now().Add(time.Hour).Unix()
	th.Observe(429, futureReset)

	th.mu.Lock()
	deadline := th.autopauseUntil
	th.mu.Unlock()
	if deadline.IsZero() {
		t.Fatal("autopauseUntil not set by Observe(429, ...)")
	}
	if !deadline.After(time.Now()) {
		t.Errorf("autopauseUntil %v is not in the future", deadline)
	}
}

func TestErrorStreakAutopause(t *testing.T) {
	th := NewThrottle(Defaults{
		AutopauseAfter:    2,
		AutopauseDuration: 40 * time.Millisecond,
	})

	th.Observe(500, 0)
	th.Observe(500, 0) // second consecutive error → autopause

	ctx := context.Background()
	start := time.Now()
	if err := th.AwaitRead(ctx, "any", 100, 10); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed < 20*time.Millisecond {
		t.Errorf("autopause did not block, elapsed=%v", elapsed)
	}
}

func TestSuccessResetsErrorStreak(t *testing.T) {
	th := NewThrottle(Defaults{
		AutopauseAfter:    3,
		AutopauseDuration: 5 * time.Second, // would definitely block if it fired
	})

	th.Observe(500, 0)
	th.Observe(500, 0)
	th.Observe(200, 0) // resets
	th.Observe(500, 0)
	th.Observe(500, 0) // only 2 consecutive; should not trigger

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := th.AwaitRead(ctx, "any", 100, 10); err != nil {
		t.Errorf("should not have blocked, got %v", err)
	}
}

func TestThrottleConcurrentSafe(t *testing.T) {
	th := NewThrottle(Defaults{})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = th.AwaitRead(ctx, "shared", 50, 10)
			th.Observe(200, 0)
		}()
	}
	wg.Wait()
}

// TestConcurrentMutationGapInvariant proves two goroutines cannot both
// fire within one min_gap window. The earlier implementation released the
// lock before sleeping, letting both callers read the same mutationLast
// value and schedule for the same instant.
func TestConcurrentMutationGapInvariant(t *testing.T) {
	th := NewThrottle(Defaults{})
	ctx := context.Background()

	const workers = 4
	const gap = 25 * time.Millisecond

	fireTimes := make(chan time.Time, workers)
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := th.AwaitMutation(ctx, gap, gap, 1000); err != nil {
				t.Errorf("AwaitMutation: %v", err)
				return
			}
			fireTimes <- time.Now()
		}()
	}
	wg.Wait()
	close(fireTimes)

	var sorted []time.Time
	for t := range fireTimes {
		sorted = append(sorted, t)
	}
	// Sort by fire time.
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Before(sorted[j-1]); j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}

	// Adjacent fires must be spaced by at least gap minus a slack for
	// scheduler jitter. 15ms is the smallest value that's robust
	// across container / CI / high-load environments.
	const slack = 15 * time.Millisecond
	for i := 1; i < len(sorted); i++ {
		d := sorted[i].Sub(sorted[i-1])
		if d+slack < gap {
			t.Errorf("gap[%d] = %v < %v; mutation race", i, d, gap)
		}
	}
	t.Logf("fired %d mutations in %v", len(sorted), time.Since(start))
}

// TestMutationRollbackOnContextCancel ensures a cancelled mutation does not
// consume the daily quota counter.
func TestMutationRollbackOnContextCancel(t *testing.T) {
	th := NewThrottle(Defaults{})
	_ = th.AwaitMutation(context.Background(), time.Nanosecond, time.Nanosecond, 10)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	// Large gap guarantees we cancel during the sleep, not before reservation.
	err := th.AwaitMutation(ctx, 500*time.Millisecond, 500*time.Millisecond, 10)
	if err == nil {
		t.Fatal("expected context cancellation")
	}

	th.mu.Lock()
	count := th.mutationCount
	th.mu.Unlock()
	// First call succeeded (+1). Second cancelled (reserved +1, rolled back -1). Net = 1.
	if count != 1 {
		t.Errorf("mutationCount = %d, want 1 (rollback failed)", count)
	}
}

// TestMutationMaxGapLessThanMinGapDoesNotPanic guards against a config where
// the caller passes min_gap larger than the default max_gap.
func TestMutationMaxGapLessThanMinGapDoesNotPanic(t *testing.T) {
	th := NewThrottle(Defaults{MutationMaxGap: 100 * time.Millisecond})
	err := th.AwaitMutation(context.Background(), time.Second, 0, 10)
	if err != nil {
		t.Fatalf("AwaitMutation with inverted gap: %v", err)
	}
}

