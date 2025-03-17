package storage

import (
	"time"

	"github.com/vocdoni/saas-backend/internal"
)

// UserData represents a user of the SMS handler.
type UserData struct {
	ID      internal.HexBytes     `json:"userID,omitempty" bson:"_id"`
	Bundles map[string]BundleData `json:"bundles,omitempty" bson:"bundles"`
}

// BundleData represents a bundle of processes. It includes the bundle ID, the
// processes, and the last attempt to sign.
type BundleData struct {
	ID          internal.HexBytes      `json:"bundleId" bson:"_id"`
	Processes   map[string]ProcessData `json:"processes" bson:"processes"`
	LastAttempt time.Time              `json:"lastAttempt,omitempty" bson:"lastattempt"`
}

// AuthToken is used by the storage to index a token with its userID
// (from UserData).
type AuthToken struct {
	Token     internal.HexBytes `json:"token" bson:"_id"`
	UserID    internal.HexBytes `json:"userID" bson:"userid"`
	BundleID  internal.HexBytes `json:"bundleID" bson:"bundleid"`
	CreatedAt time.Time         `json:"createdAt" bson:"createdat"`
	Verified  bool              `json:"verified" bson:"verified"`
}

// ProcessData represents a process of a bundle. It includes the process ID,
// the consumed status, the token used, the address used, and the time of the
// signature.
type ProcessData struct {
	ID          internal.HexBytes `json:"processId" bson:"_id"`
	Consumed    bool              `json:"consumed" bson:"consumed"`
	WithToken   internal.HexBytes `json:"withToken" bson:"withtoken"`
	WithAddress internal.HexBytes `json:"withAddress" bson:"withaddress"`
	At          time.Time         `json:"at" bson:"at"`
}

type CSPAuthToken struct {
	Token      internal.HexBytes `json:"token" bson:"_id"`
	UserID     internal.HexBytes `json:"userID" bson:"userid"`
	BundleID   internal.HexBytes `json:"bundleID" bson:"bundleid"`
	CreatedAt  time.Time         `json:"createdAt" bson:"createdat"`
	Verified   bool              `json:"verified" bson:"verified"`
	VerifiedAt time.Time         `json:"verifiedAt" bson:"verifiedat"`
}

type CSPAuthTokenStatus struct {
	ID              internal.HexBytes `json:"id" bson:"_id"` // hash(token + pid)
	Token           internal.HexBytes `json:"token" bson:"token"`
	Consumed        bool              `json:"consumed" bson:"consumed"`
	ConsumedAt      time.Time         `json:"consumedAt" bson:"consumedat"`
	ConsumedPID     internal.HexBytes `json:"consumedPID" bson:"consumedpid"`
	ConsumedAddress internal.HexBytes `json:"consumedAddress" bson:"consumedaddress"`
}
