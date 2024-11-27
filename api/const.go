package api

import (
	"time"
)

// VerificationCodeExpiration is the duration of the verification code
// before it is invalidated
var VerificationCodeExpiration = 3 * time.Minute

const (
	// VerificationCodeLength is the length of the verification code in bytes
	VerificationCodeLength = 3
	// InvitationExpiration is the duration of the invitation code before it is
	// invalidated
	InvitationExpiration = 5 * 24 * time.Hour // 5 days
)
