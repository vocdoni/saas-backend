package twofactor

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nyaruka/phonenumbers"
	"go.vocdoni.io/dvote/log"
)

var (
	// ErrTooManyAttempts is returned when no more SMS attempts available for a user.
	ErrTooManyAttempts = fmt.Errorf("too many SMS attempts")
	// ErrUserUnknown is returned if the userID is not found in the database.
	ErrUserUnknown = fmt.Errorf("user is unknown")
	// ErrUserAlreadyVerified is returned if the user is already verified when trying to verify it.
	ErrUserAlreadyVerified = fmt.Errorf("user is already verified")
	// ErrUserNotBelongsToElection is returned if the user does not has participation rights.
	ErrUserNotBelongsToElection = fmt.Errorf("user does not belong to election")
	// ErrInvalidAuthToken is returned if the authtoken does not match with the election.
	ErrInvalidAuthToken = fmt.Errorf("invalid authentication token")
	// ErrChallengeCodeFailure is returned when the challenge code does not match.
	ErrChallengeCodeFailure = fmt.Errorf("challenge code do not match")
	// ErrAttemptCoolDownTime is returned if the cooldown time for a challenge attempt is not reached.
	ErrAttemptCoolDownTime = fmt.Errorf("attempt cooldown time not reached")
)

// Users is the list of smshandler users.
type Users struct {
	Users []HexBytes `json:"users"`
}

// UserData represents a user of the SMS handler.
type UserData struct {
	UserID    HexBytes                  `json:"userID,omitempty" bson:"_id"`
	Elections map[string]UserElection   `json:"elections,omitempty" bson:"elections,omitempty"`
	ExtraData string                    `json:"extraData,omitempty" bson:"extradata,omitempty"`
	Phone     *phonenumbers.PhoneNumber `json:"phone,omitempty" bson:"phone,omitempty"`
}

// UserElection represents an election and its details owned by a user (UserData).
type UserElection struct {
	ElectionID        HexBytes   `json:"electionId" bson:"_id"`
	RemainingAttempts int        `json:"remainingAttempts" bson:"remainingattempts"`
	LastAttempt       *time.Time `json:"lastAttempt,omitempty" bson:"lastattempt,omitempty"`
	Consumed          bool       `json:"consumed" bson:"consumed"`
	AuthToken         *uuid.UUID `json:"authToken,omitempty" bson:"authtoken,omitempty"`
	ChallengeSecret   string     `json:"challenge,omitempty" bson:"challenge,omitempty"`
}

// AuthTokenIndex is used by the storage to index a token with its userID (from UserData).
type AuthTokenIndex struct {
	AuthToken *uuid.UUID `json:"authToken" bson:"_id"`
	UserID    HexBytes   `json:"userID" bson:"userid"`
}

// UserCollection is a dataset containing several users (used for dump and import).
type UserCollection struct {
	Users []UserData `json:"users" bson:"users"`
}

// HexBytesToElection transforms a slice of HexBytes to []Election.
// All entries are set with RemainingAttempts = attempts.
func HexBytesToElection(electionIDs []HexBytes, attempts int) []UserElection {
	elections := []UserElection{}

	for _, e := range electionIDs {
		ue := UserElection{}
		ue.ElectionID = e
		ue.RemainingAttempts = attempts
		elections = append(elections, ue)
	}
	return elections
}

// Message is the JSON API body message used by the CSP and the client
type Message struct {
	Error     string       `json:"error,omitempty"`
	TokenR    HexBytes     `json:"token,omitempty"`
	AuthToken *uuid.UUID   `json:"authToken,omitempty"`
	Payload   HexBytes     `json:"payload,omitempty"`
	Signature HexBytes     `json:"signature,omitempty"`
	SharedKey HexBytes     `json:"sharedkey,omitempty"`
	Title     string       `json:"title,omitempty"`         // reserved for the info handler
	SignType  []string     `json:"signatureType,omitempty"` // reserver for the info handler
	AuthType  string       `json:"authType,omitempty"`      // reserved for the info handler
	AuthSteps []*AuthField `json:"authSteps,omitempty"`     // reserved for the info handler
	AuthData  []string     `json:"authData,omitempty"`      // reserved for the auth handler
	Response  []string     `json:"response,omitempty"`      // reserved for the handlers
	Elections []Election   `json:"elections,omitempty"`     // reserved for the indexer handler
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

// HexBytes is a []byte which encodes as hexadecimal in json, as opposed to the
// base64 default.
type HexBytes []byte

func (b HexBytes) String() string {
	return hex.EncodeToString(b)
}

func (b *HexBytes) FromString(str string) error {
	var err error
	(*b), err = hex.DecodeString(str)
	return err
}

func (b HexBytes) MarshalJSON() ([]byte, error) {
	enc := make([]byte, hex.EncodedLen(len(b))+2)
	enc[0] = '"'
	hex.Encode(enc[1:], b)
	enc[len(enc)-1] = '"'
	return enc, nil
}

func (b *HexBytes) UnmarshalJSON(data []byte) error {
	if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' {
		return fmt.Errorf("invalid JSON string: %q", data)
	}
	data = data[1 : len(data)-1]

	// Strip a leading "0x" prefix, for backwards compatibility.
	if len(data) >= 2 && data[0] == '0' && (data[1] == 'x' || data[1] == 'X') {
		data = data[2:]
	}

	decLen := hex.DecodedLen(len(data))
	if cap(*b) < decLen {
		*b = make([]byte, decLen)
	}
	if _, err := hex.Decode(*b, data); err != nil {
		return err
	}
	return nil
}

// Election represents a process voting election which might be available for
// CSP signature or not (already used).
type Election struct {
	ElectionID        HexBytes `json:"electionId"`
	RemainingAttempts int      `json:"remainingAttempts"`
	Consumed          bool     `json:"consumed"`
	ExtraData         []string `json:"extra"`
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
	Success   bool       // Either the authentication step is success or not
	Response  []string   // Response can be used by the handler to provide arbitrary data to the client
	AuthToken *uuid.UUID // Only if there is a next step
	TokenR    HexBytes   // TokenR is the random token generated for the client
	Signature HexBytes   // Signature is the CSP signature
	Error     string     // Error is used to provide an error message to the client
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
