package api

import "time"

// VerificationCodeExpiration is the duration of the verification code
// before it is invalidated
var VerificationCodeExpiration = 2 * time.Minute

const (
	// VerificationCodeLength is the length of the verification code in bytes
	VerificationCodeLength = 3
	// VerificationCodeEmailSubject is the subject of the verification code email
	VerificationCodeEmailSubject = "Vocdoni verification code"
	// VerificationCodeTextBody is the body of the verification code email
	VerificationCodeTextBody = "Your Vocdoni verification code is: "
)
