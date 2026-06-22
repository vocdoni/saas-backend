// Package notifications provides a queue system for managing and sending notification
// challenges with concurrency, retries, circuit breaking, and error handling capabilities.
package notifications

import (
	"context"
	"time"

	"github.com/vocdoni/saas-backend/notifications"
	"go.vocdoni.io/dvote/log"
)

const (
	// DefaultMaxSMSattempts defines the default maximum number of SMS allowed attempts.
	DefaultMaxSMSattempts = 5
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
	DefaultBreakerMaxFailures = notifications.DefaultBreakerMaxFailures
	// DefaultBreakerCooldown is how long a provider's circuit breaker stays open
	// once tripped, before a probe send is allowed again.
	DefaultBreakerCooldown = notifications.DefaultBreakerCooldown
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
// concurrently. It delegates all delivery mechanics to notifications.Queue and
// translates between the CSP-specific NotificationChallenge type and the
// generic notifications.QueueItem. The result of every challenge
// (delivered or given up) is reported on the NotificationsSent channel.
type Queue struct {
	NotificationsSent chan *NotificationChallenge

	inner *notifications.Queue
	ctx   context.Context
}

// NewQueue creates a new notification queue from the provided configuration.
func NewQueue(ctx context.Context, conf QueueConfig) *Queue {
	workers := conf.Workers
	if workers <= 0 {
		workers = DefaultQueueWorkers
	}
	innerConf := notifications.QueueConfig{
		TTL:                conf.TTL,
		Workers:            workers,
		MaxRetries:         DefaultQueueMaxRetries,
		MailService:        conf.MailService,
		SMSService:         conf.SMSService,
		BreakerMaxFailures: conf.BreakerMaxFailures,
		BreakerCooldown:    conf.BreakerCooldown,
	}
	inner := notifications.NewQueue(ctx, innerConf)
	return &Queue{
		// Buffer the channel generously so concurrent workers never block on the
		// downstream consumer (which performs a synchronous DB write per result).
		NotificationsSent: make(chan *NotificationChallenge, workers*4),
		inner:             inner,
		ctx:               ctx,
	}
}

// toItem maps a NotificationChallenge to a generic inner queue item, stamping
// CreatedAt and carrying the challenge as Meta so results can be mapped back.
func (*Queue) toItem(challenge *NotificationChallenge) *notifications.QueueItem {
	if challenge.CreatedAt.IsZero() {
		challenge.CreatedAt = time.Now()
	}
	log.Debugw("notification challenge enqueued",
		"bundleID", challenge.BundleID.String(),
		"userID", challenge.UserID.String(),
		"type", challenge.Type)
	return &notifications.QueueItem{
		Notification: challenge.Notification,
		Type:         challenge.Type,
		Label:        challenge.BundleID.String(),
		CreatedAt:    challenge.CreatedAt,
		ExpiresAt:    challenge.ExpiresAt,
		Meta:         challenge,
	}
}

// Push adds a notification challenge to the queue for processing.
func (sq *Queue) Push(challenge *NotificationChallenge) error {
	return sq.inner.Push(sq.toItem(challenge))
}

// PushWait enqueues the challenge and returns a channel signalled once its
// delivery completes (delivered or given up). See notifications.Queue.PushWait;
// it exists for callers (notably tests) that must observe delivery before
// proceeding. Results are still forwarded to NotificationsSent as usual.
func (sq *Queue) PushWait(challenge *NotificationChallenge) (<-chan *notifications.QueueItem, error) {
	return sq.inner.PushWait(sq.toItem(challenge))
}

// Start launches the worker pool and the result-forwarding goroutine.
func (sq *Queue) Start() {
	sq.inner.Start()
	go sq.forwardResults()
}

// forwardResults reads delivered items from the inner queue's Done channel,
// casts Meta back to *NotificationChallenge, and forwards to NotificationsSent.
func (sq *Queue) forwardResults() {
	for {
		select {
		case <-sq.ctx.Done():
			return
		case item, ok := <-sq.inner.Done:
			if !ok {
				return
			}
			challenge, ok := item.Meta.(*NotificationChallenge)
			if !ok {
				log.Warnw("unexpected meta type in csp notification queue result")
				continue
			}
			challenge.Success = item.Success
			challenge.Retries = item.Retries
			select {
			case <-sq.ctx.Done():
				return
			case sq.NotificationsSent <- challenge:
			}
		}
	}
}
