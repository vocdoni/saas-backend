package notifications

import (
	"context"
	"errors"
	"fmt"
	"net/textproto"
	"time"

	"github.com/enriquebris/goconcurrentqueue"
	"go.vocdoni.io/dvote/log"
)

const (
	// DefaultOTPCooldown is the minimum wait between notification requests for
	// the same account, used as an anti-spam rate limit across all flows
	// (password recovery, CSP OTP token generation).
	DefaultOTPCooldown = 60 * time.Second
	// DefaultOTPExpiry is the shared validity window for all one-time codes:
	// account verification, password reset, and CSP OTP challenges. After this
	// duration the code is rejected and the user must request a new one.
	DefaultOTPExpiry = 15 * time.Minute
	// DefaultQueueMaxRetries is how many times to retry delivering a notification
	// in case the upstream provider returns a transient error.
	DefaultQueueMaxRetries = 10
	// DefaultQueueWorkers is the number of concurrent senders draining the queue.
	DefaultQueueWorkers = 16
	// DefaultQueueTTL is how long an item may stay in the queue before it is
	// dropped. Must exceed DefaultOTPExpiry so notifications are not dropped
	// before the code they carry has expired.
	DefaultQueueTTL = 20 * time.Minute
)

// QueueItem is a single notification pending delivery. The Meta field is
// opaque caller context that the queue passes through untouched — callers can
// use it to correlate Done events back to their own domain types.
type QueueItem struct {
	Notification *Notification
	Type         NotificationType
	Label        string
	CreatedAt    time.Time
	Retries      int
	Success      bool
	// ExpiresAt, if non-zero, is the deadline after which the notification
	// content is considered stale and should not be delivered. The queue drops
	// the item (with Success=false) without retrying when this time is passed.
	// Use this to prevent delivering expired OTP codes or password-reset links
	// after a prolonged provider outage.
	ExpiresAt time.Time
	// Meta is opaque caller context. The queue does not inspect it.
	Meta any
}

// QueueConfig holds the configuration for a Queue.
type QueueConfig struct {
	// TTL is the maximum age of an item before it is dropped from the queue.
	TTL time.Duration
	// Workers is the number of concurrent senders. Values <= 0 use the default.
	Workers int
	// MaxRetries caps transient-error retries per item. Values <= 0 use the default.
	MaxRetries int
	// MailService and SMSService are the providers. Either may be nil.
	MailService NotificationService
	SMSService  NotificationService
	// BreakerMaxFailures and BreakerCooldown configure the per-provider circuit
	// breakers. Values <= 0 use the defaults.
	BreakerMaxFailures int
	BreakerCooldown    time.Duration
}

// Queue is a FIFO queue that delivers notifications concurrently. A pool of
// workers drains the queue; each send is guarded by a per-provider circuit
// breaker so that a failing provider is given time to recover instead of being
// hammered. Permanent failures are not retried (see isPermanentSendError);
// every other error is treated as transient and retried within the MaxRetries
// and TTL bounds. The result of every item (delivered or given up) is reported
// on the Done channel.
type Queue struct {
	// Done receives each item after final delivery attempt (Success or not).
	// It is buffered generously so workers never block on the consumer.
	Done chan *QueueItem

	ctx         context.Context
	items       *goconcurrentqueue.FIFO
	ttl         time.Duration
	workers     int
	maxRetries  int
	mailService NotificationService
	smsService  NotificationService
	mailBreaker *Breaker
	smsBreaker  *Breaker
}

// NewQueue creates a new Queue from the provided configuration.
func NewQueue(ctx context.Context, conf QueueConfig) *Queue {
	ttl := conf.TTL
	if ttl <= 0 {
		ttl = DefaultQueueTTL
	}
	workers := conf.Workers
	if workers <= 0 {
		workers = DefaultQueueWorkers
	}
	maxRetries := conf.MaxRetries
	if maxRetries <= 0 {
		maxRetries = DefaultQueueMaxRetries
	}
	return &Queue{
		Done:        make(chan *QueueItem, workers*4),
		ctx:         ctx,
		items:       goconcurrentqueue.NewFIFO(),
		ttl:         ttl,
		workers:     workers,
		maxRetries:  maxRetries,
		mailService: conf.MailService,
		smsService:  conf.SMSService,
		mailBreaker: NewBreaker(Email, conf.BreakerMaxFailures, conf.BreakerCooldown),
		smsBreaker:  NewBreaker(SMS, conf.BreakerMaxFailures, conf.BreakerCooldown),
	}
}

// Push adds an item to the queue for processing.
func (q *Queue) Push(item *QueueItem) error {
	if item == nil {
		return fmt.Errorf("nil queue item")
	}
	if item.Notification == nil {
		return fmt.Errorf("nil notification in queue item")
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	log.Debugw("notification enqueued", "label", item.Label, "type", item.Type)
	return q.items.Enqueue(item)
}

// Start launches the worker pool. Workers return when the context is canceled.
func (q *Queue) Start() {
	log.Infow("starting notification queue workers", "workers", q.workers, "ttl", q.ttl.String())
	for i := 0; i < q.workers; i++ {
		go q.worker()
	}
}

func (q *Queue) worker() {
	for {
		item, err := q.next()
		if err != nil {
			return
		}
		if item == nil {
			continue
		}
		q.deliver(item)
	}
}

func (q *Queue) next() (*QueueItem, error) {
	raw, err := q.items.DequeueOrWaitForNextElementContext(q.ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		log.Warnw("queue dequeue error", "error", err)
		return nil, nil
	}
	item, ok := raw.(*QueueItem)
	if !ok {
		log.Warnw("invalid item type in notification queue")
		return nil, nil
	}
	return item, nil
}

func (q *Queue) serviceFor(item *QueueItem) NotificationService {
	if item.Type == SMS {
		return q.smsService
	}
	return q.mailService
}

func (q *Queue) breakerFor(item *QueueItem) *Breaker {
	if item.Type == SMS {
		return q.smsBreaker
	}
	return q.mailBreaker
}

func (q *Queue) deliver(item *QueueItem) {
	// Drop expired items before attempting delivery. This prevents sending
	// stale OTP codes or password-reset links after a prolonged provider outage.
	if !item.ExpiresAt.IsZero() && time.Now().After(item.ExpiresAt) {
		log.Warnw("notification content expired, dropping without send",
			"label", item.Label,
			"type", item.Type,
			"expiredAt", item.ExpiresAt)
		q.giveUp(item)
		return
	}

	br := q.breakerFor(item)

	if !br.Allow() {
		if !q.softReenqueue(item) {
			q.giveUp(item)
		}
		q.sleep(br.RetryAfter())
		return
	}

	svc := q.serviceFor(item)
	if svc == nil {
		log.Warnw("no notification service configured, dropping item", "label", item.Label, "type", item.Type)
		q.giveUp(item)
		return
	}

	sendCtx, cancel := context.WithTimeout(q.ctx, 10*time.Second)
	err := svc.SendNotification(sendCtx, item.Notification)
	cancel()

	if err != nil {
		if IsPermanentSendError(err) {
			log.Warnw("permanent notification failure, giving up",
				"label", item.Label,
				"type", item.Type,
				"error", err)
			q.giveUp(item)
			return
		}
		br.RecordFailure()
		q.handleFailure(item, err)
		return
	}

	br.RecordSuccess()
	log.Debugw("notification successfully sent", "label", item.Label, "type", item.Type)
	item.Success = true
	q.emit(item)
}

func (q *Queue) handleFailure(item *QueueItem, err error) {
	log.Warnw("failed to send notification",
		"label", item.Label,
		"type", item.Type,
		"error", err)
	if rerr := q.reenqueue(item); rerr != nil {
		log.Warnw("notification not re-enqueued",
			"label", item.Label,
			"type", item.Type,
			"error", rerr)
		q.giveUp(item)
	}
}

func (q *Queue) giveUp(item *QueueItem) {
	item.Success = false
	q.emit(item)
}

func (q *Queue) emit(item *QueueItem) {
	select {
	case <-q.ctx.Done():
	case q.Done <- item:
	}
}

func (q *Queue) reenqueue(item *QueueItem) error {
	if item.Retries >= q.maxRetries || time.Since(item.CreatedAt) > q.ttl {
		return fmt.Errorf("TTL or max retries reached")
	}
	item.Retries++
	label := item.Label
	iType := item.Type
	retries := item.Retries
	if err := q.items.Enqueue(item); err != nil {
		return fmt.Errorf("cannot enqueue: %w", err)
	}
	log.Debugw("notification re-enqueued", "label", label, "type", iType, "retry", retries)
	return nil
}

func (q *Queue) softReenqueue(item *QueueItem) bool {
	if time.Since(item.CreatedAt) > q.ttl {
		return false
	}
	if err := q.items.Enqueue(item); err != nil {
		log.Warnw("could not re-enqueue item while breaker open",
			"label", item.Label,
			"type", item.Type,
			"error", err)
		return false
	}
	return true
}

func (q *Queue) sleep(d time.Duration) {
	if d <= 0 {
		return
	}
	if d > MaxBreakerWait {
		d = MaxBreakerWait
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-q.ctx.Done():
	case <-t.C:
	}
}

// IsPermanentSendError reports whether a send error is permanent (retrying
// will never succeed). Permanent errors are SMTP 5xx replies. Everything else
// (network timeouts, transient deferrals, context cancellations) is treated as
// transient and will be retried up to the queue's MaxRetries/TTL bounds.
//
// When delivery goes through a failover service the result is an errors.Join of
// every provider's error. Such a joined error is only permanent if all branches
// are permanent.
func IsPermanentSendError(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		branches := joined.Unwrap()
		if len(branches) == 0 {
			return false
		}
		for _, branch := range branches {
			if !IsPermanentSendError(branch) {
				return false
			}
		}
		return true
	}
	var protoErr *textproto.Error
	if errors.As(err, &protoErr) {
		return protoErr.Code >= 500 && protoErr.Code < 600
	}
	return false
}
