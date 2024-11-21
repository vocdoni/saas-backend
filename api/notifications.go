package api

import (
	"context"
	"fmt"
	"time"

	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications"
)

// apiNotification is an internal struct that represents a notification to be
// sent via notifications package. It contains the mail template, the link path
// and the notification to be sent.
type apiNotification struct {
	Template     notifications.MailTemplate
	LinkPath     string
	Notification notifications.Notification
}

// VerifyAccountNotification is the notification to be sent when a user creates
// an account and needs to verify it.
var VerifyAccountNotification = apiNotification{
	Template: "verification_account",
	LinkPath: "/account/verify",
	Notification: notifications.Notification{
		Subject: "Vocdoni verification code",
		PlainBody: `Your Vocdoni password reset code is: {{.Code}}

You can also use this link to reset your password: {{.Link}}`,
	},
}

// PasswordResetNotification is the notification to be sent when a user requests
// a password reset.
var PasswordResetNotification = apiNotification{
	Template: "forgot_password",
	LinkPath: "/account/password/reset",
	Notification: notifications.Notification{
		Subject: "Vocdoni password reset",
		PlainBody: `Your Vocdoni password reset code is: {{.Code}}

You can also use this link to reset your password: {{.Link}}`,
	},
}

// InviteAdminNotification is the notification to be sent when a user is invited
// to be an admin of an organization.
var InviteAdminNotification = apiNotification{
	Template: "invite_admin",
	LinkPath: "/account/invite",
	Notification: notifications.Notification{
		Subject: "Vocdoni organization invitation",
		PlainBody: `You code to join to '{{.Organization}}' organization is: {{.Code}}

You can also use this link to join the organization: {{.Link}}`,
	},
}

// sendNotification method sends a notification to the email provided. It
// requires the email the API notification definition and the data to fill it.
// It clones the notification included in the API notification and fills it
// with the recipient email address and the data provided, using the template
// defined in the API notification. It returns an error if the mail service is
// available and the notification could not be sent. If the mail service is not
// available, the notification is not sent but the function returns nil.
func (a *API) sendNotification(ctx context.Context, to string, an apiNotification, data any) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()
	// send the verification code via email if the mail service is available
	if a.mail != nil {
		// check if the email address is valid
		if !internal.ValidEmail(to) {
			return fmt.Errorf("invalid email address")
		}
		// clone the notification and create a pointer to it
		notification := &an.Notification
		// set the recipient email address
		notification.ToAddress = to
		// execute the template with the data provided
		if err := notification.ExecTemplate(a.mailTemplates[an.Template], data); err != nil {
			return err
		}
		// send the notification
		if err := a.mail.SendNotification(ctx, notification); err != nil {
			return err
		}
	}
	return nil
}
