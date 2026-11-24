package twilio

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"github.com/vocdoni/saas-backend/notifications"
	"go.vocdoni.io/dvote/log"
)

const (
	// defaultCanaryInterval applies when RunCanary gets a non-positive interval.
	defaultCanaryInterval = 24 * time.Hour
	// minCanaryInterval caps the canary at one SMS an hour per instance.
	minCanaryInterval = time.Hour
)

// RunCanary sends one SMS to the number every interval (at least an hour),
// starting right away, until ctx is done. The message reads like a verification
// code, so carrier content filters treat it like a voter code, and is sent and
// counted like one but with source="canary". With no voter traffic, the canary's delivered callback is
// what keeps saas_sms_last_delivered_timestamp_seconds fresh. Every instance that
// runs it sends its own SMS, so enable it on one instance only.
func (tsms *SMS) RunCanary(ctx context.Context, to string, interval time.Duration) {
	switch {
	case interval <= 0:
		interval = defaultCanaryInterval
	case interval < minCanaryInterval:
		log.Warnw("sms canary interval too short, using the minimum", "interval", interval, "minimum", minCanaryInterval)
		interval = minCanaryInterval
	default:
	}
	send := func() {
		n, err := canaryNotification(to, tsms.config.Label)
		if err != nil {
			log.Warnw("sms canary not sent", "error", err)
			return
		}
		if err := tsms.deliver(ctx, n, sourceCanary); err != nil {
			log.Warnw("sms canary send failed", "error", err)
			return
		}
		log.Infow("sms canary sent")
	}
	send()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			send()
		}
	}
}

// canaryNotification builds the canary message with a random code. A non-empty
// label names the environment, e.g. "Your Vocdoni Canary (DEV) verification
// code is: 123456".
func canaryNotification(to, label string) (*notifications.Notification, error) {
	code, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return nil, fmt.Errorf("generating canary code: %w", err)
	}
	name := "Vocdoni Canary"
	if label != "" {
		name += " (" + label + ")"
	}
	return &notifications.Notification{
		ToNumber:  to,
		PlainBody: fmt.Sprintf("Your %s verification code is: %06d", name, code.Int64()),
	}, nil
}
