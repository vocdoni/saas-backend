package twilio

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"github.com/vocdoni/saas-backend/notifications"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
	"go.vocdoni.io/dvote/log"
)

const (
	// defaultCanaryInterval applies when RunCanary gets a non-positive interval.
	defaultCanaryInterval = 24 * time.Hour
	// minCanaryInterval caps the canary at one SMS an hour per instance.
	minCanaryInterval = time.Hour
	// canaryLang and canaryOrganization fill the voter code template the canary
	// sends, so carrier content filters treat it like a real code.
	canaryLang         = "es"
	canaryOrganization = "Vocdoni"
	canaryExpiry       = "10m0s"
)

// RunCanary sends one SMS to the number every interval (at least an hour),
// starting right away, until ctx is done. The message is the voter code template
// with a random code, sent and counted like a voter code but with
// source="canary". With no voter traffic, the canary's delivered callback is
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
		n, err := canaryNotification(to)
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

// canaryNotification renders the voter code template with a random code.
func canaryNotification(to string) (*notifications.Notification, error) {
	code, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return nil, fmt.Errorf("generating canary code: %w", err)
	}
	n, err := mailtemplates.VerifyOTPCodeNotification.Localized(canaryLang).ExecTemplate(struct {
		Code             string
		Organization     string
		OrganizationLogo string
		ExpiryTime       string
	}{fmt.Sprintf("%06d", code.Int64()), canaryOrganization, "", canaryExpiry})
	if err != nil {
		return nil, fmt.Errorf("rendering canary message: %w", err)
	}
	n.ToNumber = to
	return n, nil
}
