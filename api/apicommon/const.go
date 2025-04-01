// Package apicommon provides common types, constants, and helper functions for the API.
package apicommon

import "time"

// MetadataKey is a type to define the key for the metadata stored in the
// context.
type MetadataKey string

// UserMetadataKey is the key used to store the user in the context.
const UserMetadataKey MetadataKey = "user"

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
