package storage

import (
	"time"

	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/internal"
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

// UserElection represents an election and its details owned by a user
// (UserData).
type UserElection struct {
	ElectionID        internal.HexBytes `json:"electionId" bson:"_id"`
	RemainingAttempts int               `json:"remainingAttempts" bson:"remainingattempts"`
	LastAttempt       *time.Time        `json:"lastAttempt,omitempty" bson:"lastattempt,omitempty"`
	Consumed          bool              `json:"consumed" bson:"consumed"`
	AuthToken         *uuid.UUID        `json:"authToken,omitempty" bson:"authtoken,omitempty"`
	ChallengeSecret   string            `json:"challengeSecret,omitempty" bson:"challengesecret,omitempty"`
	Voted             internal.HexBytes `json:"voted,omitempty" bson:"voted,omitempty"`
}

// AuthTokenIndex is used by the storage to index a token with its userID
// (from UserData).
type AuthTokenIndex struct {
	AuthToken *uuid.UUID        `json:"authToken" bson:"_id"`
	UserID    internal.HexBytes `json:"userID" bson:"userid"`
}

// UserCollection is a dataset containing several users (used for dump and
// import).
type UserCollection struct {
	Users []UserData `json:"users" bson:"users"`
}

// HexBytesToElection transforms a slice of HexBytes to []Election. All entries
// are set with RemainingAttempts = attempts.
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
