package storage

import (
	"time"

	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/internal"
)

// Storage interface implements the storage layer for the smshandler
type Storage interface {
	// Init initializes the storage, maxAttempts is used to set the default maximum SMS attempts.
	// CoolDownTime is the time period on which attempts are allowed.
	Init(clientOrUri any, maxAttempts int, coolDownTime time.Duration) (err error)
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
