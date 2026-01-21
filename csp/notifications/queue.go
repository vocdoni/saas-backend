// Package notifications provides a queue system for managing and sending notification
// challenges with throttling, retries, and error handling capabilities.
package notifications

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
	// DefaultQueueMaxRetries is how many times to retry delivering an SMS in case upstream provider returns an error
	DefaultQueueMaxRetries = 10
)

// Queue is a FIFO queue that handles the sending of notifications (SMS or
// email) with a TTL and throttle time. It uses a goconcurrentqueue.FIFO queue
// to store the notifications and a channel to send the response back to the
// caller.
type Queue struct {
	NotificationsSent chan *NotificationChallenge
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
	if ttl == 0 {
		ttl = DefaultSMScoolDownTime
	}
	if throttle == 0 {
		throttle = DefaultSMSthrottleTime
	}
	return &Queue{
		NotificationsSent: make(chan *NotificationChallenge, 1),
		ctx:               ctx,
		items:             goconcurrentqueue.NewFIFO(),
		ttl:               ttl,
		throttle:          throttle,
		smsService:        smsSrv,
		mailService:       mailSrv,
	}
}

// Push adds a notification challenge to the queue for processing.
// It logs the challenge details and returns any error encountered during enqueuing.
func (sq *Queue) Push(challenge *NotificationChallenge) error {
	log.Debugw("notification challenge enqueued",
		"bundleID", challenge.BundleID.String(),
		"userID", challenge.UserID.String(),
		"type", challenge.Type)
	return sq.items.Enqueue(challenge)
}

// dequeueChallenge attempts to dequeue a challenge from the queue
// Returns nil and an error if dequeuing fails or the item is invalid
func (sq *Queue) dequeueChallenge() (*NotificationChallenge, error) {
	c, err := sq.items.Dequeue()
	if err != nil {
		if err.Error() != "empty queue" {
			log.Warnw("dequeue error", "error", err)
		}
		return nil, err
	}

	// Decode the challenge information
	challenge, ok := c.(*NotificationChallenge)
	if !ok {
		log.Warnw("invalid challenge type in queue")
		return nil, fmt.Errorf("invalid challenge type")
	}

	if !challenge.Valid() {
		log.Warnw("invalid notification challenge",
			"bundleID", challenge.BundleID.String(),
			"userID", challenge.UserID.String(),
			"type", challenge.Type)
		return nil, fmt.Errorf("invalid challenge")
	}

	return challenge, nil
}

// getNotificationService returns the appropriate notification service based on challenge type
func (sq *Queue) getNotificationService(challenge *NotificationChallenge) notifications.NotificationService {
	if challenge.Type == SMSChallenge {
		return sq.smsService
	}
	return sq.mailService
}

// handleFailedNotification handles a failed notification attempt
// Returns true if the challenge was successfully re-enqueued
func (sq *Queue) handleFailedNotification(challenge *NotificationChallenge, err error) bool {
	log.Warnw("failed to send notification",
		"bundleID", challenge.BundleID.String(),
		"userID", challenge.UserID.String(),
		"type", challenge.Type,
		"error", err)

	if err := sq.reenqueue(challenge); err != nil {
		log.Warnw("notification challenge not re-enqueued",
			"bundleID", challenge.BundleID.String(),
			"userID", challenge.UserID.String(),
			"type", challenge.Type,
			"error", err)
		// Notify that we're removing this element
		sq.NotificationsSent <- challenge
		return false
	}

	return true
}

// handleSuccessfulNotification handles a successful notification
func (sq *Queue) handleSuccessfulNotification(challenge *NotificationChallenge) {
	log.Debugw("notification with challenge successfully sent",
		"bundleID", challenge.BundleID.String(),
		"userID", challenge.UserID.String(),
		"type", challenge.Type)
	sq.NotificationsSent <- challenge
}

// processNextChallenge processes the next challenge in the queue
func (sq *Queue) processNextChallenge() {
	challenge, err := sq.dequeueChallenge()
	if err != nil {
		return // Nothing to process or invalid challenge
	}

	// Get the appropriate notification service
	notifyService := sq.getNotificationService(challenge)

	// Try to send the notification
	if err := challenge.Send(sq.ctx, notifyService); err != nil {
		// Handle failed notification
		sq.handleFailedNotification(challenge, err)
		return
	}

	// Handle successful notification
	sq.handleSuccessfulNotification(challenge)
}

// Start starts the queue processing loop. It will dequeue elements from the
// queue and send the notification challenge. If the notification fails, it
// will re-enqueue the challenge up to DefaultQueueMaxRetries times. The
// function will return when the context is canceled. All notifications sent
// will be sent back to the caller through the NotificationsSent channel.
func (sq *Queue) Start() {
	ticker := time.NewTicker(sq.throttle)
	defer ticker.Stop()

	for {
		select {
		case <-sq.ctx.Done():
			return
		case <-ticker.C:
			sq.processNextChallenge()
		}
	}
}

// reenqueue tries to re-enqueue the notification challenge. It will return an
// error if the challenge has reached the maximum number of retries or the TTL
// has expired.
func (sq *Queue) reenqueue(challenge *NotificationChallenge) error {
	// check if we have to enqueue it again or not
	if challenge.Retries >= DefaultQueueMaxRetries || time.Since(challenge.CreatedAt) > sq.ttl {
		return fmt.Errorf("TTL or max retries reached")
	}
	// enqueue it again
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
