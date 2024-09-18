package api

import "github.com/vocdoni/saas-backend/notifications"

const (
	// VerificationCodeLength is the length of the verification code in bytes
	VerificationCodeLength = 3
	// VerificationCodeEmailSubject is the subject of the verification code email
	VerificationCodeEmailSubject = "Vocdoni verification code"
	// VerificationCodeEmailBody is the body of the verification code email
	VerificationCodeEmailPlainBody = "Your verification code is: "
	// VerificationAccountTemplate is the key that identifies the verification
	// account email template. It must be also the name of the file in the
	// email templates directory.
	VerificationAccountTemplate notifications.MailTemplate = "verification_account"
	// PasswordResetTemplate is the key that identifies the password reset email
	// template. It must be also the name of the file in the email templates
	// directory.
	PasswordResetTemplate notifications.MailTemplate = "password_reset"
)
