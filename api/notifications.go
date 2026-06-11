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
	if err := a.notifyQueue.Push(&notifications.QueueItem{
		Notification: notification,
		Type:         notifications.Email,
		Label:        to,
		ExpiresAt:    expiresAt,
	}); err != nil {
		log.Warnw("could not enqueue mail notification", "to", to, "error", err)
	}
	return nil
}
