package api

import (
	"context"
	"fmt"
	"time"

	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
)

// sendMail method sends a localized notification to the email provided.
// It requires the localized email template and the data to fill it. It extracts
// the language from the context and executes the appropriate template variant.
// It returns an error if the mail service is available and the notification could
// not be sent or the email address is invalid. If the mail service is not available,
// it does nothing.
func (a *API) sendMail(ctx context.Context, to string, mail mailtemplates.MailTemplate, data any) error {
	return a.sendMailVia(ctx, a.mail, to, mail, data)
}

// sendResendMail sends a localized notification via the resend mail service when
// configured, falling back to the primary service if no resend service is set.
func (a *API) sendResendMail(ctx context.Context, to string, mail mailtemplates.MailTemplate, data any) error {
	svc := a.resendMail
	if svc == nil {
		svc = a.mail
	}
	return a.sendMailVia(ctx, svc, to, mail, data)
}

// sendMailVia sends a localized notification using the given service.
func (a *API) sendMailVia(ctx context.Context, svc notifications.NotificationService, to string,
	tmpl mailtemplates.MailTemplate, data any,
) error {
	if svc == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()
	if !internal.ValidEmail(to) {
		return fmt.Errorf("invalid email address")
	}
	lang := a.getLanguageFromContext(ctx)
	notification, err := tmpl.Localized(lang).ExecTemplate(data)
	if err != nil {
		return err
	}
	notification.ToAddress = to
	return svc.SendNotification(ctx, notification)
}
