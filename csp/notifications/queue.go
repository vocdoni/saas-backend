// Package notifications provides a queue system for managing and sending notification
// challenges with concurrency, retries, circuit breaking, and error handling capabilities.
package notifications

import (
	"context"
	"errors"
	"fmt"
	"net/textproto"
	"time"

	"github.com/enriquebris/goconcurrentqueue"
	"github.com/vocdoni/saas-backend/notifications"
	"go.vocdoni.io/dvote/log"
)

const (
	// DefaultMaxSMSattempts defines the default maximum number of SMS allowed attempts.
	DefaultMaxSMSattempts = 5
	// DefaultSMScoolDownTime defines the default cool down time window for sending challenges.
	DefaultSMScoolDownTime = 2 * time.Minute
	// DefaultQueueMaxRetries is how many times to retry delivering a notification
	// in case the upstream provider returns a transient error.
	DefaultQueueMaxRetries = 10
	// DefaultQueueWorkers is the number of concurrent senders draining the queue.
	// It bounds the maximum number of in-flight provider sends at any time.
	DefaultQueueWorkers = 16
	// DefaultQueueTTL is how long a challenge may stay in the queue before it is
	// dropped. It is generous on purpose so that a transient provider outage
	// (e.g. an inbound rate-limit deferral storm) does not discard OTPs.
	DefaultQueueTTL = 12 * time.Minute
	// DefaultBreakerMaxFailures is the number of consecutive transient send
	// failures that trips a provider's circuit breaker.
	DefaultBreakerMaxFailures = 10
	// DefaultBreakerCooldown is how long a provider's circuit breaker stays open
	// once tripped, before a probe send is allowed again.
	DefaultBreakerCooldown = 30 * time.Second
	// maxBreakerWait bounds how long a worker sleeps in a single iteration while
	// a provider's breaker is open, so workers stay responsive to context
	// cancellation and to the breaker closing early.
	maxBreakerWait = 2 * time.Second
)

// QueueConfig holds the configuration for a notification Queue.
type QueueConfig struct {
	// TTL is the maximum age of a challenge before it is dropped from the queue.
	TTL time.Duration
	// Workers is the number of concurrent senders. Values <= 0 use the default.
	Workers int
	// MailService and SMSService are the providers used to deliver email and SMS
	// challenges respectively. Either may be nil if that channel is unused.
	MailService notifications.NotificationService
	SMSService  notifications.NotificationService
	// BreakerMaxFailures and BreakerCooldown configure the per-provider circuit
	// breakers. Values <= 0 use the defaults.
	BreakerMaxFailures int
	BreakerCooldown    time.Duration
}

// Queue is a FIFO queue that delivers notification challenges (SMS or email)
// concurrently. A pool of workers drains the queue; each send is guarded by a
// per-provider circuit breaker so that a failing provider is given time to
// recover instead of being hammered. Transient failures are retried (bounded by
// the max retries and the challenge TTL); permanent failures are not retried.
// The result of every challenge (delivered or given up) is reported on the
// NotificationsSent channel.
type Queue struct {
	NotificationsSent chan *NotificationChallenge

	ctx         context.Context
	items       *goconcurrentqueue.FIFO
	ttl         time.Duration
	workers     int
	smsService  notifications.NotificationService
	mailService notifications.NotificationService
	mailBreaker *breaker
	smsBreaker  *breaker
}

// NewQueue creates a new notification queue from the provided configuration.
func NewQueue(ctx context.Context, conf QueueConfig) *Queue {
	ttl := conf.TTL
	if ttl <= 0 {
		ttl = DefaultQueueTTL
	}
	workers := conf.Workers
	if workers <= 0 {
		workers = DefaultQueueWorkers
	}
	return &Queue{
		// Buffer the channel generously so concurrent workers never block on the
		// downstream consumer (which performs a synchronous DB write per result).
		NotificationsSent: make(chan *NotificationChallenge, workers*4),
		ctx:               ctx,
		items:             goconcurrentqueue.NewFIFO(),
		ttl:               ttl,
		workers:           workers,
		smsService:        conf.SMSService,
		mailService:       conf.MailService,
		mailBreaker:       newBreaker(conf.BreakerMaxFailures, conf.BreakerCooldown),
		smsBreaker:        newBreaker(conf.BreakerMaxFailures, conf.BreakerCooldown),
	}
}

// Push adds a notification challenge to the queue for processing.
// It logs the challenge details and returns any error encountered during enqueuing.
func (sq *Queue) Push(challenge *NotificationChallenge) error {
	if challenge.CreatedAt.IsZero() {
		challenge.CreatedAt = time.Now()
	}
	log.Debugw("notification challenge enqueued",
		"bundleID", challenge.BundleID.String(),
		"userID", challenge.UserID.String(),
		"type", challenge.Type)
	return sq.items.Enqueue(challenge)
}

// serviceFor returns the notification service for the given challenge type.
func (sq *Queue) serviceFor(challenge *NotificationChallenge) notifications.NotificationService {
	if challenge.Type == SMSChallenge {
		return sq.smsService
	}
	return sq.mailService
}

// breakerFor returns the circuit breaker for the given challenge type.
func (sq *Queue) breakerFor(challenge *NotificationChallenge) *breaker {
	if challenge.Type == SMSChallenge {
		return sq.smsBreaker
	}
	return sq.mailBreaker
}

// Start launches the worker pool. Each worker blocks waiting for the next
// challenge and delivers it. The workers return when the context is canceled.
func (sq *Queue) Start() {
	for i := 0; i < sq.workers; i++ {
		go sq.worker()
	}
}

// worker is the per-goroutine processing loop. It blocks on the queue until a
// challenge is available (or the context is canceled), then attempts delivery.
func (sq *Queue) worker() {
	for {
		challenge, err := sq.nextChallenge()
		if err != nil {
			// Context canceled (or the queue was permanently locked): stop.
			return
		}
		if challenge == nil {
			continue
		}
		sq.deliver(challenge)
	}
}

// nextChallenge blocks until the next valid challenge is available or the
// context is canceled. It returns an error only when the worker should stop.
func (sq *Queue) nextChallenge() (*NotificationChallenge, error) {
	item, err := sq.items.DequeueOrWaitForNextElementContext(sq.ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		// Transient queue error (e.g. too many waiters): skip this iteration.
		log.Warnw("dequeue error", "error", err)
		return nil, nil
	}
	challenge, ok := item.(*NotificationChallenge)
	if !ok {
		log.Warnw("invalid challenge type in queue")
		return nil, nil
	}
	if !challenge.Valid() {
		log.Warnw("invalid notification challenge",
			"bundleID", challenge.BundleID.String(),
			"userID", challenge.UserID.String(),
			"type", challenge.Type)
		return nil, nil
	}
	return challenge, nil
}

// deliver attempts to send a single challenge, applying circuit breaking,
// retry-on-transient-error and drop-on-permanent-error semantics.
func (sq *Queue) deliver(challenge *NotificationChallenge) {
	br := sq.breakerFor(challenge)

	// If the provider's breaker is open, do not attempt a send. Re-enqueue the
	// challenge without consuming a retry (this is a provider outage, not a
	// per-message failure) and wait out the cooldown so workers do not spin.
	if !br.Allow() {
		if !sq.softReenqueue(challenge) {
			sq.giveUp(challenge)
		}
		sq.sleep(br.retryAfter())
		return
	}

	if err := challenge.Send(sq.ctx, sq.serviceFor(challenge)); err != nil {
		if isPermanentSendError(err) {
			// Permanent failure (e.g. invalid recipient): do not retry and do
			// not blame the provider's breaker.
			log.Warnw("permanent notification failure, giving up",
				"bundleID", challenge.BundleID.String(),
				"userID", challenge.UserID.String(),
				"type", challenge.Type,
				"error", err)
			sq.giveUp(challenge)
			return
		}
		br.RecordFailure()
		sq.handleFailedNotification(challenge, err)
		return
	}

	br.RecordSuccess()
	sq.handleSuccessfulNotification(challenge)
}

// sleep waits for the given duration or until the context is canceled. It caps
// the wait at maxBreakerWait so workers stay responsive.
func (sq *Queue) sleep(d time.Duration) {
	if d <= 0 {
		return
	}
	if d > maxBreakerWait {
		d = maxBreakerWait
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-sq.ctx.Done():
	case <-t.C:
	}
}

// handleFailedNotification handles a failed (transient) notification attempt by
// re-enqueuing it for another try. If it cannot be re-enqueued (max retries or
// TTL reached) the challenge is reported as failed.
func (sq *Queue) handleFailedNotification(challenge *NotificationChallenge, err error) {
	log.Warnw("failed to send notification",
		"bundleID", challenge.BundleID.String(),
		"userID", challenge.UserID.String(),
		"type", challenge.Type,
		"error", err)

	if rerr := sq.reenqueue(challenge); rerr != nil {
		log.Warnw("notification challenge not re-enqueued",
			"bundleID", challenge.BundleID.String(),
			"userID", challenge.UserID.String(),
			"type", challenge.Type,
			"error", rerr)
		sq.giveUp(challenge)
	}
}

// handleSuccessfulNotification reports a successfully delivered challenge.
func (sq *Queue) handleSuccessfulNotification(challenge *NotificationChallenge) {
	log.Debugw("notification with challenge successfully sent",
		"bundleID", challenge.BundleID.String(),
		"userID", challenge.UserID.String(),
		"type", challenge.Type)
	challenge.Success = true
	sq.emit(challenge)
}

// giveUp reports a challenge that will not be delivered.
func (sq *Queue) giveUp(challenge *NotificationChallenge) {
	challenge.Success = false
	sq.emit(challenge)
}

// emit reports the final state of a challenge on the NotificationsSent channel,
// without blocking the worker if the context is canceled.
func (sq *Queue) emit(challenge *NotificationChallenge) {
	select {
	case <-sq.ctx.Done():
	case sq.NotificationsSent <- challenge:
	}
}

// reenqueue re-enqueues a challenge after a transient send failure, consuming a
// retry. It returns an error if the challenge has reached the maximum number of
// retries or its TTL has expired.
func (sq *Queue) reenqueue(challenge *NotificationChallenge) error {
	if challenge.Retries >= DefaultQueueMaxRetries || time.Since(challenge.CreatedAt) > sq.ttl {
		return fmt.Errorf("TTL or max retries reached")
	}
	challenge.Retries++
	if err := sq.items.Enqueue(challenge); err != nil {
		return fmt.Errorf("cannot enqueue the challenge: %w", err)
	}
	log.Debugw("notification challenge re-enqueued",
		"bundleID", challenge.BundleID.String(),
		"userID", challenge.UserID.String(),
		"type", challenge.Type,
		"retry", challenge.Retries)
	return nil
}

// softReenqueue re-enqueues a challenge that was not attempted because the
// provider's breaker was open. It does NOT consume a retry (no send happened),
// but it still honors the TTL so a long outage cannot keep a challenge alive
// forever. It returns false when the TTL has expired.
func (sq *Queue) softReenqueue(challenge *NotificationChallenge) bool {
	if time.Since(challenge.CreatedAt) > sq.ttl {
		return false
	}
	if err := sq.items.Enqueue(challenge); err != nil {
		log.Warnw("could not re-enqueue challenge while breaker open",
			"bundleID", challenge.BundleID.String(),
			"userID", challenge.UserID.String(),
			"type", challenge.Type,
			"error", err)
		return false
	}
	return true
}

// isPermanentSendError reports whether a send error is permanent (the message
// will never be deliverable, e.g. an invalid recipient) as opposed to transient
// (deferral, timeout, network error) which is worth retrying. SMTP servers
// signal permanent failures with 5xx reply codes, surfaced by net/smtp as a
// *textproto.Error.
func isPermanentSendError(err error) bool {
	if err == nil {
		return false
	}
	var protoErr *textproto.Error
	if errors.As(err, &protoErr) {
		return protoErr.Code >= 500 && protoErr.Code < 600
	}
	return false
}
