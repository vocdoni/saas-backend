package mailtemplates

import "github.com/vocdoni/saas-backend/notifications"

// VerifyAccountNotification is the notification to be sent when a user creates
// an account and needs to verify it.
var VerifyAccountNotification = MailTemplate{
	File: "verification_account",
	Placeholder: notifications.Notification{
		Subject: "Vocdoni verification code",
		PlainBody: `Your Vocdoni verification code is: {{.Code}}

You can also use this link to verify your account: {{.Link}}`,
	},
	WebAppURI: "/account/verify",
}

// PasswordResetNotification is the notification to be sent when a user requests
// a password reset.
var PasswordResetNotification = MailTemplate{
	File: "forgot_password",
	Placeholder: notifications.Notification{
		Subject: "Vocdoni password reset",
		PlainBody: `Your Vocdoni password reset code is: {{.Code}}

You can also use this link to reset your password: {{.Link}}`,
	},
	WebAppURI: "/account/password/reset",
}

// InviteNotification is the notification to be sent when a user is invited
// to be an admin of an organization.
var InviteNotification = MailTemplate{
	File: "invite_admin",
	Placeholder: notifications.Notification{
		Subject: "Vocdoni organization invitation",
		PlainBody: `You code to join to '{{.Organization}}' organization is: {{.Code}}

You can also use this link to join the organization: {{.Link}}`,
	},
	WebAppURI: "/account/invite",
}
