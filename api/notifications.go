package api

import (
	"context"
	"fmt"
	"time"

	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
)

// sendMail method sends a notification to the email provided. It requires the
// email template and the data to fill it. It executes the mail template with
// the data to get the notification and sends it with the recipient email
// address provided. It returns an error if the mail service is available and
// the notification could not be sent or the email address is invalid. If the
// mail service is not available, it does nothing.
func (a *API) sendMail(ctx context.Context, to string, mail mailtemplates.MailTemplate, data any) error {
	if a.mail != nil {
		ctx, cancel := context.WithTimeout(ctx, time.Second*10)
		defer cancel()
		// check if the email address is valid
		if !internal.ValidEmail(to) {
			return fmt.Errorf("invalid email address")
		}
		// execute the mail template to get the notification
		notification, err := mail.ExecTemplate(data)
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

// sendSMS method sends a notification to the phone number provided. It requires
// the email template and the data to fill it. It executes the mail template
// with the data to get the notification and sends it with the recipient phone
// number provided, but it only uses the plain content of the resulting
// notigication. It returns an error if the SMS service is available and the
// notification could not be sent or the phone number is invalid. If the SMS
// service is not available, it does nothing.
// nolint:unused
func (a *API) sendSMS(ctx context.Context, to string, mail mailtemplates.MailTemplate, data any) error {
	if a.sms != nil {
		ctx, cancel := context.WithTimeout(ctx, time.Second*10)
		defer cancel()
		// check if the phone number is valid
		recipient, err := internal.SanitizeAndVerifyPhoneNumber(to)
		if err != nil {
			return err
		}
		// execute the mail template to get the notification
		notification, err := mail.ExecPlain(data)
		if err != nil {
			return err
		}
		// set the recipient phone number
		notification.ToNumber = recipient
		// send the mail notification
		if err := a.sms.SendNotification(ctx, notification); err != nil {
			return err
		}
	}
	return nil
}
