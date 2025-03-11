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
	ID        internal.HexBytes     `json:"userID,omitempty" bson:"_id"`
	Bundles   map[string]BundleData `json:"bundles,omitempty" bson:"bundles,omitempty"`
	ExtraData string                `json:"extraData,omitempty" bson:"extradata,omitempty"`
	Phone     string                `json:"phone,omitempty" bson:"phone,omitempty"`
	Mail      string                `json:"mail,omitempty" bson:"mail,omitempty"`
}

type BundleData struct {
	ID          internal.HexBytes   `json:"bundleId" bson:"_id"`
	PIDs        []internal.HexBytes `json:"processes" bson:"processes"`
	LastAttempt *time.Time          `json:"lastAttempt,omitempty" bson:"lastattempt,omitempty"`
}

// AuthToken is used by the storage to index a token with its userID
// (from UserData).
type AuthToken struct {
	Token     *uuid.UUID        `json:"token" bson:"_id"`
	UserID    internal.HexBytes `json:"userID" bson:"userid"`
	BundleID  internal.HexBytes `json:"bundleID" bson:"bundleid"`
	CreatedAt time.Time         `json:"createdAt" bson:"createdat"`
}
