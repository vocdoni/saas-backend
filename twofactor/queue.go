package twofactor

import (
	"fmt"
	"time"

	"github.com/enriquebris/goconcurrentqueue"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/log"
)

type challengeData struct {
	userID     internal.HexBytes
	electionID internal.HexBytes
	contact    string
	challenge  string
	startTime  time.Time
	retries    int
	success    bool
}

func (c challengeData) String() string {
	return fmt.Sprintf("%s[%s]", c.contact, c.challenge)
}

type Queue struct {
	queue         *goconcurrentqueue.FIFO
	ttl           time.Duration
	throttle      time.Duration
	sendChallenge []SendChallengeFunc
	response      chan (challengeData)
}

func newQueue(ttl, throttle time.Duration, sChFns []SendChallengeFunc) *Queue {
	return &Queue{
		queue:         goconcurrentqueue.NewFIFO(),
		response:      make(chan challengeData, 1),
		sendChallenge: sChFns,
		ttl:           ttl,
		throttle:      throttle,
	}
}

func (sq *Queue) add(userID, electionID internal.HexBytes, contact string, challenge string) error {
	c := challengeData{
		userID:     userID,
		electionID: electionID,
		contact:    contact,
		challenge:  challenge,
		startTime:  time.Now(),
		retries:    0,
	}
	defer log.Debugw("enqueued new sms with challenge", "challenge", c.String())
	return sq.queue.Enqueue(c)
}

func (sq *Queue) run() {
	for {
		time.Sleep(sq.throttle)
		c, err := sq.queue.DequeueOrWaitForNextElement()
		if err != nil {
			log.Warnw("queue error", "error", err)
			continue
		}
		challenge := c.(challengeData)
		// if multiple providers are defined, use them in round-robin
		// (try #0 will use first provider, retry #1 second provider, retry #2 first provider again)
		sendChallenge := sq.sendChallenge[challenge.retries%2]
		if err := sendChallenge(challenge.contact, challenge.challenge); err != nil {
			// Fail
			log.Warnw("failed to send notification", "challenge", challenge.String(), "error", err)
			if err := sq.reenqueue(challenge); err != nil {
				log.Warnw("removed from notification queue", "challenge", challenge.String(), "error", err)
				// Send a signal (channel) to let the caller know we are removing this element
				challenge.success = false
				sq.response <- challenge
			}
			continue
		}
		// Success
		log.Debugw("sms with challenge successfully sent", "challenge", challenge.String())
		// Send a signal (channel) to let the caller know we succeed
		challenge.success = true
		sq.response <- challenge
	}
}

func (sq *Queue) reenqueue(challenge challengeData) error {
	// check if we have to enqueue it again or not
	if challenge.retries >= DefaultSMSqueueMaxRetries || time.Now().After(challenge.startTime.Add(sq.ttl)) {
		return fmt.Errorf("TTL or max retries reached")
	}
	// enqueue it again
	challenge.retries++
	if err := sq.queue.Enqueue(challenge); err != nil {
		return fmt.Errorf("cannot enqueue sms: %w", err)
	}
	log.Infow("re-enqueued sms", "challenge", challenge.String(), "retry", challenge.retries)
	return nil
}
