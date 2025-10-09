package api

import (
	"context"
	"fmt"
	"time"

	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
)

// sendMail method sends a localized notification to the email provided.
// It requires the localized email template and the data to fill it. It extracts
// the language from the context and executes the appropriate template variant.
// It returns an error if the mail service is available and the notification could
// not be sent or the email address is invalid. If the mail service is not available,
// it does nothing.
func (a *API) sendMail(ctx context.Context, to string, mail mailtemplates.MailTemplate, data any) error {
	if a.mail != nil {
		ctx, cancel := context.WithTimeout(ctx, time.Second*10)
		defer cancel()
		// check if the email address is valid
		if !internal.ValidEmail(to) {
			return fmt.Errorf("invalid email address")
		}
		// get the language from context
		lang := a.getLanguageFromContext(ctx)
		// execute the localized mail template to get the notification
		notification, err := mail.Localized(lang).ExecTemplate(data)
		if err != nil {
			return err
		}
		// set the recipient email address
		notification.ToAddress = to
		// send the mail notification
		if err := a.mail.SendNotification(ctx, notification); err != nil {
			return err
		}
	}
	return nil
}
