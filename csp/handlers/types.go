package handlers

import (
	"time"

	"github.com/vocdoni/saas-backend/internal"
)

// AuthRequest defines the payload for the authentication request. It includes
// the participant number, the email, the phone, and the password. Not all
// fields are required for all types of authentication, for example, the
// password is required for password-based authentication, but not for OTP
// authentication. For OTP authentication, the email or phone is required.
type AuthRequest struct {
	ParticipantNo string `json:"participantNo"`
	Email         string `json:"email,omitempty"`
	Phone         string `json:"phone,omitempty"`
	Password      string `json:"password,omitempty"`
}

// AuthResponse defines the payload for the authentication response, including
// the authToken and the signature. It is used during the authentication
// process for both steps: the challenge request and the challenge validation.
type AuthResponse struct {
	AuthToken internal.HexBytes `json:"authToken,omitempty"`
	Signature internal.HexBytes `json:"signature,omitempty"`
}

// AuthRequest defines the payload for finishing the authentication process.
// It includes the auth token to verify and the solution to the challenge in
// the authData field.
type AuthChallengeRequest struct {
	AuthToken internal.HexBytes `json:"authToken,omitempty"`
	AuthData  []string          `json:"authData,omitempty"` // reserved for the auth handler
}

// SignRequest defines the payload for the signature request. It includes the
// tokenR, the authToken, the payload to sign, and the processID (election ID)
// if applicable. Not all fields are required for all types of signatures.
type SignRequest struct {
	TokenR    internal.HexBytes `json:"tokenR"`
	AuthToken internal.HexBytes `json:"authToken"`
	Payload   string            `json:"payload,omitempty"`
	ProcessID internal.HexBytes `json:"electionId,omitempty"`
}

// ConsumedAddressRequest defines the payload for the request to get the
// if a token was consumed and which address was used. It includes the
// authToken to query the information.
type ConsumedAddressRequest struct {
	AuthToken internal.HexBytes `json:"authToken"`
}

// ConsumedAddressResponse defines the payload for the response to the
// request to get the if a token was consumed and which address was used.
// It includes the address, the nullifier, and the timestamp of the
// consumption.
type ConsumedAddressResponse struct {
	Address   internal.HexBytes `json:"authToken"`
	Nullifier internal.HexBytes `json:"nullifier"`
	At        time.Time         `json:"at"`
}
