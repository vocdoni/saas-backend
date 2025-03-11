package twofactor

import (
	"context"
	"fmt"
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
	// DefaultSMSthrottleTime is the default throttle time for the SMS provider API.
	DefaultSMSthrottleTime = time.Millisecond * 500
	// DefaultSMSqueueMaxRetries is how many times to retry delivering an SMS in case upstream provider returns an error
	DefaultSMSqueueMaxRetries = 10
)

// Queue is a FIFO queue that handles the sending of notifications (SMS or
// email) with a TTL and throttle time. It uses a goconcurrentqueue.FIFO queue
// to store the notifications and a channel to send the response back to the
// caller.
type Queue struct {
	NotificationsSent chan NotificationChallenge
	ctx               context.Context
	items             *goconcurrentqueue.FIFO
	ttl               time.Duration
	throttle          time.Duration
	smsService        notifications.NotificationService
	mailService       notifications.NotificationService
}

// NewQueue creates a new queue with the provided TTL and throttle time.
func NewQueue(ctx context.Context, ttl, throttle time.Duration,
	mailSrv, smsSrv notifications.NotificationService,
) *Queue {
	return &Queue{
		NotificationsSent: make(chan NotificationChallenge, 1),
		ctx:               ctx,
		items:             goconcurrentqueue.NewFIFO(),
		ttl:               ttl,
		throttle:          throttle,
		smsService:        smsSrv,
		mailService:       mailSrv,
	}
}

func (sq *Queue) Push(challenge NotificationChallenge) error {
	return sq.items.Enqueue(challenge)
}

// Start starts the queue processing loop. It will dequeue elements from the
// queue and send the notification challenge. If the notification fails, it
// will re-enqueue the challenge up to DefaultSMSqueueMaxRetries times. The
// function will return when the context is canceled. All notifications sent
// will be sent back to the caller through the NotificationsSent channel.
func (sq *Queue) Start(ctx context.Context) {
	ticker := time.NewTicker(sq.throttle)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// get the next element from the queue
			c, err := sq.items.DequeueOrWaitForNextElement()
			if err != nil {
				log.Warnw("dequeue error", "error", err)
				continue
			}
			// decode the challenge information
			challenge := c.(NotificationChallenge)
			notifyService := sq.smsService
			if challenge.Type == EmailChallenge {
				notifyService = sq.mailService
			}
			// try to send the notification, if it fails, try to re-enqueue it
			if err := challenge.Send(sq.ctx, notifyService); err != nil {
				log.Warnw("failed to send notification",
					"challenge", challenge.String(),
					"error", err)
				if err := sq.reenqueue(challenge); err != nil {
					log.Warnw("notification challenge no re-enqueued",
						"challenge", challenge.String(),
						"error", err)
					// send a signal (channel) to let the caller know we are removing this element
					challenge.Success = false
					sq.NotificationsSent <- challenge
				}
				continue
			}
			// Success
			log.Debugw("sms with challenge successfully sent", "challenge", challenge.String())
			sq.NotificationsSent <- challenge
		}
	}
}

// reenqueue tries to re-enqueue the notification challenge. It will return an
// error if the challenge has reached the maximum number of retries or the TTL
// has expired.
func (sq *Queue) reenqueue(challenge NotificationChallenge) error {
	// check if we have to enqueue it again or not
	if challenge.Retries >= DefaultSMSqueueMaxRetries || time.Since(challenge.CreatedAt) > sq.ttl {
		return fmt.Errorf("TTL or max retries reached")
	}
	// enqueue it again
	challenge.Retries++
	if err := sq.items.Enqueue(challenge); err != nil {
		return fmt.Errorf("cannot enqueue the challenge: %w", err)
	}
	log.Debugw("notification challenge re-enqueued",
		"challenge", challenge.String(),
		"retry", challenge.Retries)
	return nil
}
