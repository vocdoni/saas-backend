package twofactor

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/internal"
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
	Users []internal.HexBytes `json:"users"`
}

// UserData represents a user of the SMS handler.
type UserData struct {
	UserID    internal.HexBytes       `json:"userID,omitempty" bson:"_id"`
	Elections map[string]UserElection `json:"elections,omitempty" bson:"elections,omitempty"`
	ExtraData string                  `json:"extraData,omitempty" bson:"extradata,omitempty"`
	Phone     string                  `json:"phone,omitempty" bson:"phone,omitempty"`
	Mail      string                  `json:"mail,omitempty" bson:"mail,omitempty"`
}

// UserElection represents an election and its details owned by a user (UserData).
type UserElection struct {
	ElectionID        internal.HexBytes `json:"electionId" bson:"_id"`
	RemainingAttempts int               `json:"remainingAttempts" bson:"remainingattempts"`
	LastAttempt       *time.Time        `json:"lastAttempt,omitempty" bson:"lastattempt,omitempty"`
	Consumed          bool              `json:"consumed" bson:"consumed"`
	AuthToken         *uuid.UUID        `json:"authToken,omitempty" bson:"authtoken,omitempty"`
	ChallengeSecret   string            `json:"challengeSecret,omitempty" bson:"challengesecret,omitempty"`
	Voted             internal.HexBytes `json:"voted,omitempty" bson:"voted,omitempty"`
}

// AuthTokenIndex is used by the storage to index a token with its userID (from UserData).
type AuthTokenIndex struct {
	AuthToken *uuid.UUID        `json:"authToken" bson:"_id"`
	UserID    internal.HexBytes `json:"userID" bson:"userid"`
}

// UserCollection is a dataset containing several users (used for dump and import).
type UserCollection struct {
	Users []UserData `json:"users" bson:"users"`
}

// HexBytesToElection transforms a slice of HexBytes to []Election.
// All entries are set with RemainingAttempts = attempts.
func HexBytesToElection(electionIDs []internal.HexBytes, attempts int) []UserElection {
	elections := []UserElection{}

	for _, e := range electionIDs {
		ue := UserElection{}
		ue.ElectionID = e
		ue.RemainingAttempts = attempts
		elections = append(elections, ue)
	}
	return elections
}

// Storage interface implements the storage layer for the smshandler
type Storage interface {
	// Init initializes the storage, maxAttempts is used to set the default maximum SMS attempts.
	// CoolDownTime is the time period on which attempts are allowed.
	Init(dataDir string, maxAttempts int, coolDownTime time.Duration) (err error)
	// Reset clears the storage content
	Reset() (err error)
	// AddUser adds a new user to the storage
	AddUser(userID internal.HexBytes, processIDs []internal.HexBytes, mail, phone, extra string) (err error)
	// BulkAddUser adds multiple users to the storage in a single operation
	BulkAddUser(users []UserData) (err error)
	// Users returns the list of users
	Users() (users *Users, err error)
	// User returns the full information of a user, including the election list.
	User(userID internal.HexBytes) (user *UserData, err error)
	// UpdateUser updates a user
	UpdateUser(udata *UserData) (err error)
	// BelongsToElection returns true if the user belongs to the electionID
	BelongsToElection(userID, electionID internal.HexBytes) (belongs bool, err error)
	// SetAttempts increment or decrement remaining challenge attempts by delta
	SetAttempts(userID, electionID internal.HexBytes, delta int) (err error)
	// MaxAttempts returns the default max attempts
	MaxAttempts() (attempts int)
	// NewAttempt returns the phone, mail, attempt number and decreases attempt counter
	NewAttempt(userID, electionID internal.HexBytes, challengeSecret string,
		token *uuid.UUID) (phone string, mail string, attemptNo int, err error)
	// Exists returns true if the user exists in the database
	Exists(userID internal.HexBytes) (exists bool)
	// Verified returns true if the user is verified
	Verified(userID, electionID internal.HexBytes) (verified bool, error error)
	// VerifyChallenge returns nil if the challenge is solved correctly. Sets verified to true and removes the
	// temporary auth token from the storage
	// GetUserFromToken returns the userData from the authToken
	GetUserFromToken(token *uuid.UUID) (*UserData, error)
	VerifyChallenge(electionID internal.HexBytes, token *uuid.UUID, solution string) (err error)
	// DelUser removes an user from the storage
	DelUser(userID internal.HexBytes) (err error)
	// Search for a term within the extraData user field and returns the list of matching userIDs
	Search(term string) (users *Users, err error)
	// String returns the string representation of the storage
	String() string
	// Import insert or update a collection of users. Follows the Dump() syntax
	Import(data []byte) (err error)
}
