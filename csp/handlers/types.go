package handlers

//revive:disable:max-public-structs

import (
	"time"

	"github.com/vocdoni/saas-backend/internal"
)

// AuthRequest defines the payload for the authentication request. It includes
// the participant ID, the email, the phone, and the password. Not all
// fields are required for all types of authentication, for example, the
// password is required for password-based authentication, but not for OTP
// authentication. For OTP authentication, the email or phone is required.
type AuthRequest struct {
	Name         string `json:"name"`
	Surname      string `json:"surname"`
	MemberNumber string `json:"memberNumber"`
	NationalID   string `json:"nationalId"`
	BirthDate    string `json:"birthDate"`
	Email        string `json:"email"`
	Phone        string `json:"phone"`
}

// AuthResendRequest defines the payload for the 2FA resend request.
type AuthResendRequest struct {
	AuthToken internal.HexBytes `json:"authToken" swaggertype:"string" format:"hex" example:"deadbeef"`
	Email     string            `json:"email"`
	Phone     string            `json:"phone"`
}

// AuthResponse defines the payload for the authentication response, including
// the authToken and the signature. It is used during the authentication
// process for both steps: the challenge request and the challenge validation.
type AuthResponse struct {
	AuthToken internal.HexBytes `json:"authToken,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
	Signature internal.HexBytes `json:"signature,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
}

// AuthChallengeRequest defines the payload for finishing the authentication process.
// It includes the auth token to verify and the solution to the challenge in
// the authData field.
type AuthChallengeRequest struct {
	AuthToken internal.HexBytes `json:"authToken,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
	AuthData  []string          `json:"authData,omitempty"` // reserved for the auth handler
}

// SignRequest defines the payload for the blind signature request. AuthToken is
// the verified voter token, ProcessID is the election, and Payload is the
// blinded ballot address produced client-side using the R point returned by the
// sign-r endpoint.
type SignRequest struct {
	AuthToken internal.HexBytes `json:"authToken" swaggertype:"string" format:"hex" example:"deadbeef"`
	Payload   string            `json:"payload,omitempty"`
	ProcessID internal.HexBytes `json:"electionId,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
}

// UserWeightRequest defines the payload for the request to get the
// weight of a user for a given bundle. It includes the authToken to query
// the information.
type UserWeightRequest struct {
	AuthToken internal.HexBytes `json:"authToken" swaggertype:"string" format:"hex" example:"deadbeef"`
}

// USerWeightResponse defines the payload for the response to the
// request to get the weight of a user for a given bundle. It includes
// the weight of the user.
type UserWeightResponse struct {
	Weight internal.HexBytes `json:"weight,omitempty" swaggertype:"string" format:"hex" example:"2a"`
}

// CheckMembershipRequest defines the payload for the request to check whether
// the user behind a CSP auth token belongs to a bundle's census. The user is
// identified solely by the authToken; the optional electionId (process ID)
// scopes the hasVoted result to that process.
type CheckMembershipRequest struct {
	AuthToken internal.HexBytes `json:"authToken" swaggertype:"string" format:"hex" example:"deadbeef"`
	ProcessID internal.HexBytes `json:"electionId,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
}

// CheckMembershipResponse defines the payload for the response to the census
// membership check. Belongs reports whether the token's user is an eligible
// participant of the bundle's census, Weight is the voter weight (1 unless the
// census is weighted) and HasVoted reports whether the user already cast a
// ballot in the requested process (only meaningful when electionId is provided).
type CheckMembershipResponse struct {
	Belongs  bool              `json:"belongs"`
	Weight   internal.HexBytes `json:"weight,omitempty" swaggertype:"string" format:"hex" example:"2a"`
	HasVoted bool              `json:"hasVoted"`
}

// ConsumedAddressRequest defines the payload for the request to get the
// if a token was used and which address was used. It includes the
// authToken to query the information.
type ConsumedAddressRequest struct {
	AuthToken internal.HexBytes `json:"authToken" swaggertype:"string" format:"hex" example:"deadbeef"`
}

// ConsumedAddressResponse defines the payload for the response to the
// request to get the if a token was used and which address was used.
// It includes the address, the nullifier, and the timestamp of the
// usage. For blind-signed processes Address and Nullifier are nil.
type ConsumedAddressResponse struct {
	Address   internal.HexBytes `json:"authToken,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
	Nullifier internal.HexBytes `json:"nullifier,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
	At        time.Time         `json:"at"`
}

// SignRRequest defines the payload for requesting the blind signing R point.
// The client submits its verified auth token and the process ID it intends to
// vote in. The server returns the R point and a weight attestation.
type SignRRequest struct {
	AuthToken internal.HexBytes `json:"authToken" swaggertype:"string" format:"hex" example:"deadbeef"`
	ProcessID internal.HexBytes `json:"electionId" swaggertype:"string" format:"hex" example:"deadbeef"`
}

// SignRResponse defines the payload returned by the sign-r endpoint.
// TokenR is the R point (33 bytes, compressed secp256k1) the client uses to
// blind its ballot address. Weight is the voter's weight for this process.
// WeightCert is a non-blind ECDSA signature over {processID, weight} that the
// chain can verify independently from the anonymous blind credential.
type SignRResponse struct {
	TokenR     internal.HexBytes `json:"tokenR" swaggertype:"string" format:"hex" example:"deadbeef"`
	Weight     internal.HexBytes `json:"weight,omitempty" swaggertype:"string" format:"hex" example:"2a"`
	WeightCert internal.HexBytes `json:"weightCert,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
}
