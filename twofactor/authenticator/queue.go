package authenticator

import (
	"fmt"
	"sync"
	"time"

	"github.com/enriquebris/goconcurrentqueue"
	"github.com/vocdoni/saas-backend/twofactor/internal"
	"go.vocdoni.io/dvote/log"
)

// ChallengeData represents a challenge to be sent to a user
type ChallengeData struct {
	UserID     internal.UserID
	ElectionID internal.ElectionID
	Contact    string
	Challenge  string
	StartTime  time.Time
	Retries    int
	Success    bool
}

// String returns a string representation of the challenge data
func (c ChallengeData) String() string {
	return fmt.Sprintf("%s[%s]", c.Contact, c.Challenge)
}

// NotificationQueue manages the sending of notifications with throttling and retries
type NotificationQueue struct {
	queue          *goconcurrentqueue.FIFO
	ttl            time.Duration
	throttlePeriod time.Duration
	sendChallenge  []internal.SendChallengeFunc
	response       chan ChallengeData
	maxRetries     int
	stopChan       chan struct{}
	stopWaitGroup  sync.WaitGroup
	updateAttempts func(userID internal.UserID, electionID internal.ElectionID, delta int) error
}

// NewNotificationQueue creates a new notification queue
func NewNotificationQueue(
	ttl time.Duration,
	throttlePeriod time.Duration,
	maxRetries int,
	sendChallengeFuncs []internal.SendChallengeFunc,
	updateAttempts func(userID internal.UserID, electionID internal.ElectionID, delta int) error,
) *NotificationQueue {
	return &NotificationQueue{
		queue:          goconcurrentqueue.NewFIFO(),
		response:       make(chan ChallengeData, 10),
		sendChallenge:  sendChallengeFuncs,
		ttl:            ttl,
		throttlePeriod: throttlePeriod,
		maxRetries:     maxRetries,
		stopChan:       make(chan struct{}),
		updateAttempts: updateAttempts,
	}
}

// Add adds a notification to the queue
func (q *NotificationQueue) Add(userID internal.UserID, electionID internal.ElectionID, contact string, challenge string) error {
	c := ChallengeData{
		UserID:     userID,
		ElectionID: electionID,
		Contact:    contact,
		Challenge:  challenge,
		StartTime:  time.Now(),
		Retries:    0,
	}
	log.Debugw("enqueued new notification with challenge", "challenge", c.String())
	return q.queue.Enqueue(c)
}

// Start starts processing the queue
func (q *NotificationQueue) Start() {
	q.stopWaitGroup.Add(1)
	go q.run()
	q.stopWaitGroup.Add(1)
	go q.processResponses()
}

// Stop stops processing the queue
func (q *NotificationQueue) Stop() {
	close(q.stopChan)
	q.stopWaitGroup.Wait()
}

// run processes the queue
func (q *NotificationQueue) run() {
	defer q.stopWaitGroup.Done()

	for {
		select {
		case <-q.stopChan:
			return
		default:
			time.Sleep(q.throttlePeriod)

			c, err := q.queue.DequeueOrWaitForNextElement()
			if err != nil {
				log.Warnw("queue error", "error", err)
				continue
			}

			challenge := c.(ChallengeData)
			// If multiple providers are defined, use them in round-robin
			// (try #0 will use first provider, retry #1 second provider, retry #2 first provider again)
			sendChallenge := q.sendChallenge[challenge.Retries%len(q.sendChallenge)]
			if err := sendChallenge(challenge.Contact, challenge.Challenge); err != nil {
				// Fail
				log.Warnw("failed to send notification", "challenge", challenge.String(), "error", err)
				if err := q.reenqueue(challenge); err != nil {
					log.Warnw("removed from notification queue", "challenge", challenge.String(), "error", err)
					// Send a signal (channel) to let the caller know we are removing this element
					challenge.Success = false
					q.response <- challenge
				}
				continue
			}

			// Success
			log.Debugw("notification with challenge successfully sent", "challenge", challenge.String())
			// Send a signal (channel) to let the caller know we succeed
			challenge.Success = true
			q.response <- challenge
		}
	}
}

// processResponses processes the responses from the notification service
func (q *NotificationQueue) processResponses() {
	defer q.stopWaitGroup.Done()

	for {
		select {
		case <-q.stopChan:
			return
		case r := <-q.response:
			if r.Success {
				if err := q.updateAttempts(r.UserID, r.ElectionID, -1); err != nil {
					log.Warnw("challenge cannot be sent", "error", err)
				} else {
					log.Infow("challenge successfully sent", "challenge", r.String(), "userID", r.UserID.String())
				}
			} else {
				log.Warnw("challenge sending failed", "challenge", r.String())
			}
		}
	}
}

// reenqueue adds a challenge back to the queue for retry
func (q *NotificationQueue) reenqueue(challenge ChallengeData) error {
	// check if we have to enqueue it again or not
	if challenge.Retries >= q.maxRetries || time.Now().After(challenge.StartTime.Add(q.ttl)) {
		return fmt.Errorf("TTL or max retries reached")
	}

	// enqueue it again
	challenge.Retries++
	if err := q.queue.Enqueue(challenge); err != nil {
		return fmt.Errorf("cannot enqueue notification: %w", err)
	}

	log.Infow("re-enqueued notification", "challenge", challenge.String(), "retry", challenge.Retries)
	return nil
}
