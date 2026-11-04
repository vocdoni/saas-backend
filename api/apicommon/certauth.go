package apicommon

//revive:disable:max-public-structs

import "github.com/vocdoni/saas-backend/internal"

// Purposes of a certificate sign-in attempt (contract-cert-auth.md section 3).
const (
	// CertAuthPurposeVote is a normal sign-in: the redeem issues a CSP token while the process
	// accepts votes.
	CertAuthPurposeVote = "vote"
	// CertAuthPurposeCheck is a pre-check (an UPCOMING process, section 11): the redeem runs every
	// step but never issues a token.
	CertAuthPurposeCheck = "check"
)

// Backend reasons of a certificate sign-in failure (contract section 8). The certificate reasons
// are cert-check's own codes, passed through verbatim.
const (
	CertAuthReasonDNINotInCensus    = "dni_not_in_census"
	CertAuthReasonAttemptExpired    = "attempt_expired"
	CertAuthReasonCodeUsed          = "code_used"
	CertAuthReasonVerifierMismatch  = "verifier_mismatch"
	CertAuthReasonTooManyAttempts   = "too_many_attempts"
	CertAuthReasonCertificateCensus = "certificate_census"
	CertAuthReasonInternal          = "internal"
)

// CertAuthCreateRequest is the body of POST /processes/{processId}/auth/cert.
type CertAuthCreateRequest struct {
	// Challenge is base64url (no padding) of the SHA-256 of the verifier: exactly 43 characters.
	Challenge string `json:"challenge" example:"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"`
	// ChallengeMethod must be "S256".
	ChallengeMethod string `json:"challengeMethod" example:"S256"`
	// ReturnURL is where the auth listener sends the browser back: an allowlisted app origin with
	// an optional /:lang and /processes/{id} path, no query, fragment or userinfo.
	ReturnURL string `json:"returnUrl" example:"https://app.vocdoni.io/ca/processes/6650f0c0a1b2c3d4e5f60718"`
	// Purpose is "vote" or "check".
	Purpose string `json:"purpose" enums:"vote,check" example:"vote"`
}

// CertAuthCreateResponse is the 200 answer of POST /processes/{processId}/auth/cert.
type CertAuthCreateResponse struct {
	// State identifies the attempt: 32 random bytes, base64url. Single use, valid 15 minutes.
	State string `json:"state" example:"q3Jd0zR9xQe1v0bHkq2n8m1tY6cW4pLa7sFuEoZiXgA"`
	// AuthURL is the auth listener URL the browser navigates to, on a fresh per-attempt host.
	AuthURL string `json:"authUrl" example:"https://k7m2q9x4c1v8b3n6z5a0s2d4f6.cert.auth-dev.vocdoni.io/v1/authenticate?state=..."` //nolint:lll
	// ExpiresAt is when the state expires (RFC 3339, informative).
	ExpiresAt string `json:"expiresAt" example:"2026-11-13T09:15:00Z"`
	// ProbeURL is the auth listener's reachability probe, absent when the deployment cannot serve it.
	ProbeURL string `json:"probeUrl,omitempty" example:"https://probe.cert.auth-dev.vocdoni.io/ok"`
}

// CertAuthRedeemRequest is the body of POST /processes/{processId}/auth/cert/redeem.
type CertAuthRedeemRequest struct {
	// Code is the code the auth listener returned in the URL fragment.
	Code string `json:"code" example:"Zx8bQ2mVt1q9LkP0aR7sYc3dWn5uHe2oJi6gFb4xTz8"`
	// Verifier is the PKCE verifier whose S256 challenge the create carried.
	Verifier string `json:"verifier" example:"dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"`
}

// CertAuthHolder is the display identity of a certificate holder: never the full document number.
type CertAuthHolder struct {
	FullName string `json:"fullName" example:"Maria García López"`
	// DocumentType is "DNI" or "NIE".
	DocumentType string `json:"documentType" example:"DNI"`
	// DocumentEnding is the last 4 characters of the canonical 9-character number.
	DocumentEnding string `json:"documentEnding" example:"678Z"`
}

// CertAuthIssuer names the certificate provider of a failure, when it is known.
type CertAuthIssuer struct {
	ID   string `json:"id" example:"fnmt"`
	Name string `json:"name" example:"FNMT"`
}

// CertAuthRedeemResponse is the 200 answer of POST /processes/{processId}/auth/cert/redeem.
type CertAuthRedeemResponse struct {
	// Purpose echoes the create purpose.
	Purpose string `json:"purpose" example:"vote"`
	// AuthToken is the pre-verified CSP token, as an auth/0 token of an auth-only census; null for
	// a check, and for a vote while the process does not accept votes.
	AuthToken *internal.HexBytes `json:"authToken" swaggertype:"string" format:"hex" example:"8d1c5f3e0a2b4c6d8e0f1a2b3c4d5e6f"`
	// Weight is the voter's census weight (bare hex), omitted when AuthToken is null.
	Weight internal.HexBytes `json:"weight,omitempty" swaggertype:"string" format:"hex" example:"01"`
	// VoterRef is opaque and stable for one participant of one process.
	VoterRef string `json:"voterRef" example:"Vt3nQ9xW2kLp7RfA0bYc1d"`
	// Holder is the certificate holder's display identity.
	Holder CertAuthHolder `json:"holder"`
	// Reference is the cert-check diagnostic id of this check (32 hex).
	Reference string `json:"reference" example:"3f9a07c2e41b8d5c2a6f90b3c7e14d28"`
}

// CertAuthErrorData is the data object of every certificate sign-in failure (error code 40950).
type CertAuthErrorData struct {
	Reason    string `json:"reason" example:"dni_not_in_census"`
	Retryable bool   `json:"retryable"`
	// Reference is the diagnostic id to quote to support (32 hex).
	Reference string          `json:"reference,omitempty" example:"3f9a07c2e41b8d5c2a6f90b3c7e14d28"`
	Holder    *CertAuthHolder `json:"holder,omitempty"`
	Issuer    *CertAuthIssuer `json:"issuer,omitempty"`
	// NotAfter is the certificate's expiry (RFC 3339), on expired and not_yet_valid.
	NotAfter string `json:"notAfter,omitempty" example:"2026-10-03T00:00:00Z"`
	// RetryAfter is the seconds until the attempt may be retried, on too_many_attempts.
	RetryAfter int64 `json:"retryAfter,omitempty" example:"1800"`
}
