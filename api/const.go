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
	VerificationCodeTextBody = "Your Vocdoni verification code is: "
	// verificationURI is the URI to verify the user account in the web app that
	// must be included in the verification email.
	VerificationURI = "/account/verify?email=%s&code=%s"
	// VerificationAccountTemplate is the key that identifies the verification
	// account email template. It must be also the name of the file in the
	// email templates directory.
	VerificationAccountTemplate notifications.MailTemplate = "verification_account"
	// PasswordResetTemplate is the key that identifies the password reset email
	// template. It must be also the name of the file in the email templates
	// directory.
	PasswordResetTemplate notifications.MailTemplate = "password_reset"
	// InvitationEmailSubject is the subject of the invitation email
	InvitationEmailSubject = "Vocdoni organization invitation"
	// InvitationTextBody is the body of the invitation email
	InvitationTextBody = "You code to join to '%s' organization is: %s"
	// InvitationExpiration is the duration of the invitation code before it is invalidated
	InvitationExpiration = 5 * 24 * time.Hour // 5 days
)
