package twilio

import (
	"context"
	"time"

	"github.com/vocdoni/saas-backend/notifications"
	"go.vocdoni.io/dvote/log"
)

const (
	// canaryBody is the text of the canary SMS.
	canaryBody = "Vocdoni SMS delivery check. No action needed."
	// defaultCanaryInterval applies when RunCanary gets a non-positive interval.
	defaultCanaryInterval = 24 * time.Hour
)

// RunCanary sends one SMS to the number every interval, starting right away,
// until ctx is done. It goes through SendNotification, so it is counted and
// gets a status callback like any voter SMS. With no voter traffic, the canary's
// delivered callback is what keeps saas_sms_last_delivered_timestamp_seconds
// fresh; sending at startup also refills that gauge after a restart.
func (tsms *SMS) RunCanary(ctx context.Context, to string, interval time.Duration) {
	if interval <= 0 {
		interval = defaultCanaryInterval
	}
	send := func() {
		n := &notifications.Notification{ToNumber: to, PlainBody: canaryBody}
		if err := tsms.SendNotification(ctx, n); err != nil {
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
