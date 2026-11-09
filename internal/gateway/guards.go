package gateway

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ---- circuit breaker (per provider node) ----
//
// Stops hammering a provider that is down: after breakerThreshold provider-side failures within breakerWindow the
// breaker opens and requests fail fast (a combo moves on to its next member). After breakerReset one probe is let
// through (half-open); success closes the breaker, failure re-opens it with a longer wait (×2 every breakerEscalate
// failed probes, up to ×breakerMaxMult). Constants follow XRouter-Next's circuitBreaker.js; failures are counted in a
// sliding window and cleared by a success, so an occasionally flaky provider never trips it.

const (
	breakerThreshold = 5
	breakerWindow    = time.Minute
	breakerReset     = 30 * time.Second
	breakerEscalate  = 3
	breakerMaxMult   = 16
)

type breaker struct {
	mu       sync.Mutex
	fails    []time.Time
	open     bool
	openedAt time.Time
	probing  bool
	probeAt  time.Time
	cycles   int // failed half-open probes
}

func (b *breaker) timeout() time.Duration {
	mult := min(1<<(b.cycles/breakerEscalate), breakerMaxMult)
	return breakerReset * time.Duration(mult)
}

// allow reports whether a request may go upstream; when it may not, wait is how long until the next probe.
func (b *breaker) allow() (ok bool, wait time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.open {
		return true, 0
	}
	if left := b.timeout() - time.Since(b.openedAt); left > 0 {
		return false, left
	}
	// half-open: a single probe at a time; a probe that never reported back (client gone) expires after breakerReset
	if b.probing && time.Since(b.probeAt) < breakerReset {
		return false, breakerReset - time.Since(b.probeAt)
	}
	b.probing, b.probeAt = true, time.Now()
	return true, 0
}

// record notes the outcome of an upstream call; failed means a provider-side failure (5xx, timeout, network).
// It returns "OPEN" or "CLOSED" when the call changed the breaker's state, else "".
func (b *breaker) record(failed bool) (changed string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	switch {
	case !failed:
		if b.open {
			changed = "CLOSED"
		}
		b.fails, b.open, b.probing, b.cycles = nil, false, false, 0
	case b.open: // the half-open probe failed
		b.cycles++
		b.openedAt, b.probing = now, false
		changed = "OPEN"
	default:
		b.fails = append(b.fails, now)
		for len(b.fails) > 0 && now.Sub(b.fails[0]) > breakerWindow {
			b.fails = b.fails[1:]
		}
		if len(b.fails) >= breakerThreshold {
			b.open, b.openedAt, b.probing = true, now, false
			changed = "OPEN"
		}
	}
	return changed
}

func (g *Gateway) breakerFor(nodeID string) *breaker {
	g.mu.Lock()
	defer g.mu.Unlock()
	b := g.breakers[nodeID]
	if b == nil {
		b = &breaker{}
		g.breakers[nodeID] = b
	}
	return b
}

// providerFailure: statuses that mean "the provider is unhealthy" (as opposed to a bad key or a bad request).
func providerFailure(status int) bool {
	switch status {
	case 0, 408, 500, 502, 503, 504, 520, 524:
		return true
	}
	return false
}

// ---- concurrency limiter (per connection) ----
//
// At most `max` in-flight requests per upstream account; extra requests queue FIFO (≤ queueMax, ≤ queueWait) so a
// burst does not turn into a 429 cascade. Defaults follow XRouter-Next's accountSemaphore.js.

const (
	defaultConcurrency = 3
	queueMax           = 20
	queueWait          = 30 * time.Second
)

var errQueueFull = errors.New("concurrency limit reached and the wait queue is full")

type gate struct {
	running int
	waiters []chan struct{}
}

type limiter struct {
	mu    sync.Mutex
	gates map[string]*gate
}

func (l *limiter) get(key string) *gate {
	g := l.gates[key]
	if g == nil {
		g = &gate{}
		l.gates[key] = g
	}
	return g
}

// free reports whether a request could start right now without waiting.
func (l *limiter) free(key string, max int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	g := l.gates[key]
	return g == nil || g.running < max
}

func (l *limiter) release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	g := l.gates[key]
	if g == nil {
		return
	}
	if len(g.waiters) > 0 { // hand the slot straight to the next in line
		w := g.waiters[0]
		g.waiters = g.waiters[1:]
		close(w)
		return
	}
	if g.running--; g.running <= 0 {
		delete(l.gates, key)
	}
}

// acquire takes a slot, waiting in the queue if needed. The returned func releases it (idempotent).
func (l *limiter) acquire(ctx context.Context, key string, max int) (func(), error) {
	l.mu.Lock()
	g := l.get(key)
	var once sync.Once
	release := func() { once.Do(func() { l.release(key) }) }
	if g.running < max {
		g.running++
		l.mu.Unlock()
		return release, nil
	}
	if len(g.waiters) >= queueMax {
		l.mu.Unlock()
		return nil, errQueueFull
	}
	w := make(chan struct{})
	g.waiters = append(g.waiters, w)
	l.mu.Unlock()

	timer := time.NewTimer(queueWait)
	defer timer.Stop()
	select {
	case <-w:
		return release, nil
	case <-timer.C:
	case <-ctx.Done():
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if g := l.gates[key]; g != nil {
		for i, x := range g.waiters {
			if x == w {
				g.waiters = append(g.waiters[:i], g.waiters[i+1:]...)
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, errors.New("timed out waiting for a free slot on this account")
			}
		}
	}
	// granted at the very moment we gave up: we own a slot, so give it back
	select {
	case <-w:
		go l.release(key)
	default:
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, errors.New("timed out waiting for a free slot on this account")
}

// BreakerStatus is the circuit state of a provider for the dashboard: CLOSED, OPEN or HALF_OPEN.
func (g *Gateway) BreakerStatus(nodeID string) (state string, retry time.Duration, failures int) {
	g.mu.Lock()
	b := g.breakers[nodeID]
	g.mu.Unlock()
	if b == nil {
		return "CLOSED", 0, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	cutoff := time.Now().Add(-breakerWindow)
	for _, f := range b.fails {
		if f.After(cutoff) {
			failures++
		}
	}
	if !b.open {
		return "CLOSED", 0, failures
	}
	if left := b.timeout() - time.Since(b.openedAt); left > 0 {
		return "OPEN", left, failures
	}
	return "HALF_OPEN", 0, failures
}
