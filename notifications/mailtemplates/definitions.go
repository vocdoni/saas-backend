// Package mailtemplates provides predefined email templates for various notification types
// such as account verification, password reset, and organization invitations,
// along with utilities for rendering email content.
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

// VerifyOTPCodeNotification is the notification to be sent when a user wants
// to login using OTP code.
var VerifyOTPCodeNotification = MailTemplate{
	File: "verification_code_otp",
	Placeholder: notifications.Notification{
		Subject:   "Codi de Verificació - Vocdoni",
		PlainBody: `El teu codi de verificació és: {{.Code}}`,
	},
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

// PlainNotification is a notification that does not require any template
var SupportNotification = MailTemplate{
	File: "support",
	Placeholder: notifications.Notification{
		Subject: "New {{.Type}} Ticket from {{.Email}}: {{.Title}}",
		PlainBody: `You have a new support request:

		Title: {{.Title}}
		Type: {{.Type}}
		User: {{.Email}}
		Organization: {{.Organization}}

		Description:
		{{.Description}}`,
	},
}

// MembersImportCompletionNotification is the notification to be sent when
// a members import process is completed.
var MembersImportCompletionNotification = MailTemplate{
	File: "members_import_done",
	Placeholder: notifications.Notification{
		Subject: "Members import completed for {{.OrganizationName}}",
		PlainBody: `Hello {{.UserName}},

Your members import for organization "{{.OrganizationName}}" has been completed.

Import Summary:
- Total members processed: {{.TotalMembers}}
- Successfully added: {{.AddedMembers}}
- Errors: {{.ErrorCount}}

{{if .Errors}}
Error details:
{{range .Errors}}• {{.}}
{{end}}
{{end}}

Completed at: {{.CompletedAt}}

Best regards,
The Vocdoni Team`,
	},
}
