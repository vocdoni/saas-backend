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
	Weight    internal.HexBytes `json:"weight,omitempty" swaggertype:"string" format:"hex" example:"2a"`
}

// AuthRequest defines the payload for finishing the authentication process.
// It includes the auth token to verify and the solution to the challenge in
// the authData field.
type AuthChallengeRequest struct {
	AuthToken internal.HexBytes `json:"authToken,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
	AuthData  []string          `json:"authData,omitempty"` // reserved for the auth handler
}

// SignRequest defines the payload for the signature request. It includes the
// tokenR, the authToken, the payload to sign, and the processID (election ID)
// if applicable. Not all fields are required for all types of signatures.
type SignRequest struct {
	TokenR    internal.HexBytes `json:"tokenR" swaggertype:"string" format:"hex" example:"deadbeef"`
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

// ProcessCheckResponse is the voter status/eligibility response of the new /processes
// flow. BelongsToProcess reports whether the token's member is in the process census;
// Weight is the member weight; Questions carries per-question eligibility and vote status.
type ProcessCheckResponse struct {
	BelongsToProcess bool                    `json:"belongsToProcess"`
	Weight           internal.HexBytes       `json:"weight,omitempty" swaggertype:"string" format:"hex" example:"2a"`
	Questions        []ProcessQuestionStatus `json:"questions"`
}

// ProcessQuestionStatus is the per-question voter status within a ProcessCheckResponse.
type ProcessQuestionStatus struct {
	QuestionID string            `json:"questionId"`
	UpstreamID internal.HexBytes `json:"upstreamId,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
	CanVote    bool              `json:"canVote"`
	HasVoted   bool              `json:"hasVoted"`
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
// usage.
type ConsumedAddressResponse struct {
	Address   internal.HexBytes `json:"address" swaggertype:"string" format:"hex" example:"deadbeef"`
	Nullifier internal.HexBytes `json:"nullifier" swaggertype:"string" format:"hex" example:"deadbeef"`
	At        time.Time         `json:"at"`
}

// QuestionConsumedAddress is one question's consumed voting info for a voter: the address that
// consumed that question's election, its nullifier, and when. Only questions the voter has voted
// are included.
type QuestionConsumedAddress struct {
	QuestionID string            `json:"questionId"`
	UpstreamID internal.HexBytes `json:"upstreamId" swaggertype:"string" format:"hex" example:"deadbeef"`
	Address    internal.HexBytes `json:"address" swaggertype:"string" format:"hex" example:"deadbeef"`
	Nullifier  internal.HexBytes `json:"nullifier" swaggertype:"string" format:"hex" example:"deadbeef"`
	At         time.Time         `json:"at"`
}

// ProcessSignInfoResponse is the per-question consumed-address view for a voter across a voting
// process (the /processes replacement of the single-election ConsumedAddressResponse).
type ProcessSignInfoResponse struct {
	Consumed []QuestionConsumedAddress `json:"consumed"`
}
