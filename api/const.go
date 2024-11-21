package api

import (
	"time"

	"github.com/vocdoni/saas-backend/notifications"
)

// VerificationCodeExpiration is the duration of the verification code
// before it is invalidated
var VerificationCodeExpiration = 3 * time.Minute

const (
	// VerificationCodeLength is the length of the verification code in bytes
	VerificationCodeLength = 3
	// VerificationCodeEmailSubject is the subject of the verification code email
	VerificationCodeEmailSubject = "Vocdoni verification code"
	// VerificationCodeTextBody is the body of the verification code email
	VerificationCodeTextBody = `Your Vocdoni verification code is: %s

You can also use this link to verify your account: %s`
	// verificationURI is the URI to verify the user account in the web app that
	// must be included in the verification email.
	VerificationURI = "/account/verify"
	// WelcomeTemplate is the key that identifies the wellcome email template.
	// It must be also the name of the file in the email templates directory.
	WelcomeTemplate notifications.MailTemplate = "welcome"
	// VerificationAccountTemplate is the key that identifies the verification
	// account email template. It must be also the name of the file in the
	// email templates directory.
	VerificationAccountTemplate notifications.MailTemplate = "verification_account"
	// PasswordResetEmailSubject is the subject of the password reset email.
	PasswordResetEmailSubject = "Vocdoni password reset"
	// PasswordResetTextBody is the body of the password reset email
	PasswordResetTextBody = `Your Vocdoni password reset code is: %s

You can also use this link to reset your password: %s`
	// PasswordResetURI is the URI to reset the user password in the web app
	// that must be included in the password reset email.
	PasswordResetURI = "/account/password/reset"
	// PasswordResetTemplate is the key that identifies the password reset email
	// template. It must be also the name of the file in the email templates
	// directory.
	PasswordResetTemplate notifications.MailTemplate = "forgot_password"
	// InviteAdminTemplate is the key that identifies the invitation email
	// template for the organization admin. It must be also the name of the file
	// in the email templates directory.
	InviteAdminTemplate notifications.MailTemplate = "invite_admin"
	// InvitationEmailSubject is the subject of the invitation email
	InvitationEmailSubject = "Vocdoni organization invitation"
	// InvitationTextBody is the body of the invitation email
	InvitationTextBody = `You code to join to '%s' organization is: %s
	
You can also use this link to join the organization: %s`
	InvitationURI = "/account/invite"
	// InvitationExpiration is the duration of the invitation code before it is invalidated
	InvitationExpiration = 5 * 24 * time.Hour // 5 days
)
