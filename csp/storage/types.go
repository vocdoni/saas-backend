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
	ID        internal.HexBytes      `json:"userID,omitempty" bson:"_id"`
	Processes map[string]UserProcess `json:"processes,omitempty" bson:"processes,omitempty"`
	Bundles   map[string]UserBundle  `json:"bundles,omitempty" bson:"bundles,omitempty"`
	ExtraData string                 `json:"extraData,omitempty" bson:"extradata,omitempty"`
	Phone     string                 `json:"phone,omitempty" bson:"phone,omitempty"`
	Mail      string                 `json:"mail,omitempty" bson:"mail,omitempty"`
}
type UserBundle struct {
	ID        internal.HexBytes      `json:"bundleId" bson:"_id"`
	Processes map[string]UserProcess `json:"processes" bson:"processes"`
}

// UserProcess represents an election and its details owned by a user
// (UserData).
type UserProcess struct {
	ID                internal.HexBytes `json:"processId" bson:"_id"`
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
