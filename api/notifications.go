package api

import (
	"context"
	"fmt"
	"time"

	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
	"go.vocdoni.io/dvote/log"
)

// sendMail enqueues a localized notification to the given email address.
// It extracts the language from the context, executes the template, and pushes
// the result onto the notify queue for async delivery with retry and circuit
// breaking. expiresAt, if non-zero, is the deadline after which the content
// (e.g. an OTP code) is stale and the queue must not deliver it.
// If the notify queue is not configured (notifyQueue is nil), it returns an error.
// Returns an error only for missing queue configuration, invalid email addresses, or template failures.
func (a *API) sendMail(ctx context.Context, to string, mail mailtemplates.MailTemplate, data any, expiresAt time.Time) error {
	if a.notifyQueue == nil {
		return fmt.Errorf("no notification queue configured")
	}
	if !internal.ValidEmail(to) {
		return fmt.Errorf("invalid email address")
	}
	lang := a.getLanguageFromContext(ctx)
	notification, err := mail.Localized(lang).ExecTemplate(data)
	if err != nil {
		return err
	}
	notification.ToAddress = to
	item := &notifications.QueueItem{
		Notification: notification,
		Type:         notifications.Email,
		Label:        to,
		ExpiresAt:    expiresAt,
	}
	// In synchronous-delivery mode (used by tests) block until the notification
	// is actually delivered, so callers that subsequently inspect the inbox
	// observe a deterministic happens-before instead of racing the queue worker.
	if a.notifySync {
		return a.sendMailSync(ctx, item)
	}
	if err := a.notifyQueue.Push(item); err != nil {
		log.Warnw("could not enqueue mail notification", "to", to, "error", err)
	}
	return nil
}

// sendMailSync enqueues the item and waits for its delivery outcome, bounded by
// the caller's context and a hard timeout so a stuck provider can never hang the
// request indefinitely. It is only used when NotificationsSyncDelivery is set.
func (a *API) sendMailSync(ctx context.Context, item *notifications.QueueItem) error {
	done, err := a.notifyQueue.PushWait(item)
	if err != nil {
		log.Warnw("could not enqueue mail notification", "to", item.Label, "error", err)
		return nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case res := <-done:
		if res != nil && !res.Success {
			return fmt.Errorf("notification delivery failed for %s", item.Label)
		}
		return nil
	case <-waitCtx.Done():
		return fmt.Errorf("timed out waiting for notification delivery to %s: %w", item.Label, waitCtx.Err())
	}
}
