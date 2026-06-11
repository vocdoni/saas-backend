package notifications

import (
	"sync"
	"time"

	"go.vocdoni.io/dvote/log"
)

const (
	// DefaultBreakerMaxFailures is the number of consecutive transient send
	// failures that trips a provider's circuit breaker.
	DefaultBreakerMaxFailures = 10
	// DefaultBreakerCooldown is how long a provider's circuit breaker stays open
	// once tripped, before a probe send is allowed again.
	DefaultBreakerCooldown = 30 * time.Second
	// maxBreakerWait bounds how long a worker sleeps in a single iteration while
	// a provider's breaker is open, so workers stay responsive to context
	// cancellation and to the breaker closing early.
	MaxBreakerWait = 2 * time.Second
)

// Breaker is a minimal circuit breaker guarding a single notification provider
// (e.g. the email or the SMS service). It opens after a configurable number of
// consecutive transient failures and stays open for a cooldown period. Once the
// cooldown elapses the breaker allows a probe attempt (half-open): a successful
// send closes it again and resets the failure count, while a failed probe
// reopens it for another cooldown.
//
// The breaker only reacts to transient failures (deferrals, timeouts, network
// errors). Permanent failures (e.g. an invalid recipient) are a property of the
// individual message, not the provider, so callers must not record them here.
type Breaker struct {
	name        NotificationType
	mu          sync.Mutex
	failures    int
	openUntil   time.Time
	maxFailures int
	cooldown    time.Duration
	// now is the clock used to evaluate the cooldown window. It is a field so
	// tests can inject a deterministic clock; production code uses time.Now.
	now func() time.Time
}

// NewBreaker creates a Breaker, identified by name (used in logs), that opens
// after maxFailures consecutive failures and stays open for cooldown.
// Non-positive numeric arguments fall back to the package defaults.
func NewBreaker(name NotificationType, maxFailures int, cooldown time.Duration) *Breaker {
	if maxFailures <= 0 {
		maxFailures = DefaultBreakerMaxFailures
	}
	if cooldown <= 0 {
		cooldown = DefaultBreakerCooldown
	}
	return &Breaker{
		name:        name,
		maxFailures: maxFailures,
		cooldown:    cooldown,
		now:         time.Now,
	}
}

// Allow reports whether a send may be attempted now. It returns false while the
// breaker is open (within the cooldown window after tripping).
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.openUntil.After(b.now())
}

// RetryAfter returns how long to wait before the breaker would allow a send
// again. It returns zero when the breaker is currently closed.
func (b *Breaker) RetryAfter() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if d := b.openUntil.Sub(b.now()); d > 0 {
		return d
	}
	return 0
}

// RecordSuccess resets the failure count and closes the breaker.
func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	wasTripped := b.failures >= b.maxFailures
	b.failures = 0
	b.openUntil = time.Time{}
	b.mu.Unlock()
	if wasTripped {
		log.Infow("notification provider recovered, circuit breaker closed", "provider", b.name)
	}
}

// RecordFailure increments the consecutive failure count and opens the breaker
// for the cooldown period once the configured threshold is reached. It is only
// called for attempts the breaker allowed, so an "opened" transition is logged
// at most once per cooldown cycle.
func (b *Breaker) RecordFailure() {
	b.mu.Lock()
	b.failures++
	opened := false
	if b.failures >= b.maxFailures {
		b.openUntil = b.now().Add(b.cooldown)
		opened = true
	}
	failures := b.failures
	b.mu.Unlock()
	if opened {
		log.Warnw("notification provider failing, circuit breaker opened",
			"provider", b.name,
			"consecutiveFailures", failures,
			"cooldown", b.cooldown.String())
	}
}
