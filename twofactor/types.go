package twofactor

import (
	"crypto/sha256"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/log"
)

// Message is the JSON API body message used by the CSP and the client
type Message struct {
	Error     string            `json:"error,omitempty"`
	TokenR    internal.HexBytes `json:"token,omitempty"`
	AuthToken *uuid.UUID        `json:"authToken,omitempty"`
	Payload   internal.HexBytes `json:"payload,omitempty"`
	Signature internal.HexBytes `json:"signature,omitempty"`
	SharedKey internal.HexBytes `json:"sharedkey,omitempty"`
	Title     string            `json:"title,omitempty"`         // reserved for the info handler
	SignType  []string          `json:"signatureType,omitempty"` // reserver for the info handler
	AuthType  string            `json:"authType,omitempty"`      // reserved for the info handler
	AuthSteps []*AuthField      `json:"authSteps,omitempty"`     // reserved for the info handler
	AuthData  []string          `json:"authData,omitempty"`      // reserved for the auth handler
	Response  []string          `json:"response,omitempty"`      // reserved for the handlers
	Elections []Election        `json:"elections,omitempty"`     // reserved for the indexer handler
}

func (m *Message) Marshal() []byte {
	r, err := json.Marshal(m)
	if err != nil {
		log.Warnf("error marshaling message: %v", err)
	}
	return r
}

func (m *Message) Unmarshal(data []byte) error {
	return json.Unmarshal(data, m)
}

// Election represents a process voting election which might be available for
// CSP signature or not (already used).
type Election struct {
	ElectionID        internal.HexBytes `json:"electionId"`
	RemainingAttempts int               `json:"remainingAttempts"`
	Consumed          bool              `json:"consumed"`
	ExtraData         []string          `json:"extra"`
	Voted             internal.HexBytes `json:"voted,omitempty"`
}

// AuthField is the type used by the Info method for returning the description of the
// authentication steps for the CSP implementation.
type AuthField struct {
	Title string `json:"title"`
	Type  string `json:"type"`
}

// AuthResponse is the type returned by Auth methods on the AuthHandler interface.
// If success true and AuthToken is nil, authentication process is considered finished,
// and the CSP signature is provided to the user.
type AuthResponse struct {
	Success   bool              // Either the authentication step is success or not
	Response  []string          // Response can be used by the handler to provide arbitrary data to the client
	AuthToken *uuid.UUID        // Only if there is a next step
	TokenR    internal.HexBytes // TokenR is the random token generated for the client
	Signature internal.HexBytes // Signature is the CSP signature
	Error     string            // Error is used to provide an error message to the client
}

func (a *AuthResponse) String() string {
	if len(a.Response) == 0 {
		return ""
	}
	var buf strings.Builder
	for i, r := range a.Response {
		buf.WriteString(r)
		if i < len(a.Response)-1 {
			buf.WriteString("/")
		}
	}
	return buf.String()
}

const (
	// SignatureTypeBlind is a secp256k1 blind signature
	SignatureTypeBlind = "blind"
	// SignatureTypeEthereum is the standard secp256k1 signature used in Ethereum
	SignatureTypeEthereum = "ecdsa"
	// SignatureTypeSharedKey identifier the shared key (common for all users on the same processId)
	SignatureTypeSharedKey = "sharedkey"
)

// AllSignatures is a helper list that includes all available CSP signature schemes.
var AllSignatures = []string{SignatureTypeBlind, SignatureTypeEthereum, SignatureTypeSharedKey}

// buildUserID returns the userID for a given participant and bundle
func buildUserID(participantId string, bundleId []byte) internal.HexBytes {
	hash := sha256.Sum256(append([]byte(participantId), []byte(bundleId)...))
	return internal.HexBytes(hash[:])
}
