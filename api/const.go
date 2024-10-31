package api

import "time"

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
	// InvitationEmailSubject is the subject of the invitation email
	InvitationEmailSubject = "Vocdoni organization invitation"
	// InvitationTextBody is the body of the invitation email
	InvitationTextBody = "You code to join to '%s' organization is: %s"
	// InvitationExpiration is the duration of the invitation code before it is invalidated
	InvitationExpiration = 5 * 24 * time.Hour // 5 days
)
