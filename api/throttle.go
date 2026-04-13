package api

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// Throttle enforces per-endpoint pacing and a global mutation budget.
//
// Read endpoints use a token-bucket limiter (RPS + burst).
// Mutation endpoints enforce a minimum gap with jitter and a daily cap.
// Both paths adapt to `x-rate-limit-reset` hints from the server.
type Throttle struct {
	mu sync.Mutex

	buckets        map[string]*tokenBucket
	mutationNext   time.Time // earliest time the next mutation may fire
	mutationCount  int
	mutationDate   dayKey // calendar date the counter belongs to
	errorStreak    int
	autopauseUntil time.Time

	defaults Defaults
}

// dayKey identifies a local calendar day without ambiguity across years.
// Using only time.YearDay() would collide across years (day 100 in 2026 ==
// day 100 in 2027), causing the daily counter to skip a reset.
type dayKey struct {
	year  int
	month time.Month
	day   int
}

func today() dayKey {
	y, m, d := time.Now().Date()
	return dayKey{year: y, month: m, day: d}
}

type Defaults struct {
	ReadRPS           float64
	ReadBurst         int
	MutationMinGap    time.Duration
	MutationMaxGap    time.Duration
	MutationDailyCap  int
	AutopauseAfter    int
	AutopauseDuration time.Duration
}

func NewThrottle(d Defaults) *Throttle {
	if d.ReadRPS <= 0 {
		d.ReadRPS = 0.5
	}
	if d.ReadBurst <= 0 {
		d.ReadBurst = 2
	}
	if d.MutationMinGap <= 0 {
		d.MutationMinGap = 8 * time.Second
	}
	if d.MutationMaxGap <= 0 {
		d.MutationMaxGap = 22 * time.Second
	}
	if d.MutationDailyCap <= 0 {
		d.MutationDailyCap = 200
	}
	if d.AutopauseAfter <= 0 {
		d.AutopauseAfter = 3
	}
	if d.AutopauseDuration <= 0 {
		d.AutopauseDuration = 10 * time.Minute
	}
	return &Throttle{
		buckets:  make(map[string]*tokenBucket),
		defaults: d,
	}
}

// AwaitRead blocks until a read token for `name` is available.
func (t *Throttle) AwaitRead(ctx context.Context, name string, rps float64, burst int) error {
	if err := t.checkAutopause(ctx); err != nil {
		return err
	}
	b := t.bucketFor(name, rps, burst)
	return b.wait(ctx)
}

// AwaitMutation blocks until the next mutation slot, enforcing both the
// per-endpoint gap and the global daily cap.
//
// Invariant: two concurrent callers cannot both observe the same `mutationNext`
// and fire simultaneously. We reserve the slot (advance `mutationNext` to
// `now + gap`) under the lock *before* sleeping. A second caller then sees
// the already-advanced `mutationNext` and stacks its own gap on top.
func (t *Throttle) AwaitMutation(ctx context.Context, minGap, maxGap time.Duration, dailyCap int) error {
	if err := t.checkAutopause(ctx); err != nil {
		return err
	}

	if minGap <= 0 {
		minGap = t.defaults.MutationMinGap
	}
	if maxGap <= 0 {
		maxGap = t.defaults.MutationMaxGap
	}
	// If the caller or defaults produced maxGap < minGap, clamp it up.
	// Otherwise `rand.Int63n(negative)` panics.
	if maxGap < minGap {
		maxGap = minGap
	}
	if dailyCap <= 0 {
		dailyCap = t.defaults.MutationDailyCap
	}

	t.mu.Lock()
	now := time.Now()
	d := today()
	if d != t.mutationDate {
		t.mutationDate = d
		t.mutationCount = 0
	}
	if t.mutationCount >= dailyCap {
		t.mu.Unlock()
		return &BudgetExhaustedError{Cap: dailyCap}
	}

	gap := minGap
	if maxGap > minGap {
		gap += time.Duration(rand.Int63n(int64(maxGap-minGap) + 1))
	}

	var fireAt time.Time
	if t.mutationNext.IsZero() || now.After(t.mutationNext) {
		fireAt = now.Add(gap)
	} else {
		fireAt = t.mutationNext.Add(gap)
	}

	// Reserve the slot while holding the lock — the next caller will queue
	// after this one.
	t.mutationNext = fireAt
	t.mutationCount++
	t.mu.Unlock()

	wait := time.Until(fireAt)
	if wait > 0 {
		select {
		case <-ctx.Done():
			// Roll back the reservation so a cancelled call does not consume
			// quota. Losing the exact ordering on rollback is acceptable.
			t.mu.Lock()
			if t.mutationCount > 0 {
				t.mutationCount--
			}
			t.mu.Unlock()
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return nil
}

// Observe records the result of a request so the throttle can react to
// server-sent hints and consecutive failures.
func (t *Throttle) Observe(status int, rateResetUnix int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	switch {
	case status == 429 && rateResetUnix > 0:
		t.autopauseUntil = time.Unix(rateResetUnix, 0)
	case status >= 500 || status == 429:
		t.errorStreak++
		if t.errorStreak >= t.defaults.AutopauseAfter {
			t.autopauseUntil = time.Now().Add(t.defaults.AutopauseDuration)
		}
	case status >= 200 && status < 300:
		t.errorStreak = 0
	}
}

func (t *Throttle) checkAutopause(ctx context.Context) error {
	t.mu.Lock()
	deadline := t.autopauseUntil
	t.mu.Unlock()
	if deadline.IsZero() || time.Now().After(deadline) {
		return nil
	}
	wait := time.Until(deadline)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

func (t *Throttle) bucketFor(name string, rps float64, burst int) *tokenBucket {
	t.mu.Lock()
	defer t.mu.Unlock()
	if b, ok := t.buckets[name]; ok {
		return b
	}
	if rps <= 0 {
		rps = t.defaults.ReadRPS
	}
	if burst <= 0 {
		burst = t.defaults.ReadBurst
	}
	b := newTokenBucket(rps, burst)
	t.buckets[name] = b
	return b
}

// -----------------------------------------------------------------------------
// Token bucket
// -----------------------------------------------------------------------------

type tokenBucket struct {
	mu       sync.Mutex
	rate     float64
	capacity float64
	tokens   float64
	last     time.Time
}

func newTokenBucket(rps float64, burst int) *tokenBucket {
	return &tokenBucket{
		rate:     rps,
		capacity: float64(burst),
		tokens:   float64(burst),
		last:     time.Now(),
	}
}

func (b *tokenBucket) wait(ctx context.Context) error {
	for {
		b.mu.Lock()
		now := time.Now()
		elapsed := now.Sub(b.last).Seconds()
		b.tokens += elapsed * b.rate
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.last = now

		if b.tokens >= 1 {
			b.tokens--
			b.mu.Unlock()
			return nil
		}

		deficit := 1 - b.tokens
		wait := time.Duration(deficit / b.rate * float64(time.Second))
		b.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

type BudgetExhaustedError struct{ Cap int }

func (e *BudgetExhaustedError) Error() string {
	return "daily mutation budget exhausted (cap reached)"
}
