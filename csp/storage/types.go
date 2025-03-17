package storage

import (
	"time"

	"github.com/vocdoni/saas-backend/internal"
)

type CSPAuthToken struct {
	Token      internal.HexBytes `json:"token" bson:"_id"`
	UserID     internal.HexBytes `json:"userID" bson:"userid"`
	BundleID   internal.HexBytes `json:"bundleID" bson:"bundleid"`
	CreatedAt  time.Time         `json:"createdAt" bson:"createdat"`
	Verified   bool              `json:"verified" bson:"verified"`
	VerifiedAt time.Time         `json:"verifiedAt" bson:"verifiedat"`
}

type CSPAuthTokenStatus struct {
	ID              internal.HexBytes `json:"id" bson:"_id"` // hash(userID + processID)
	UserID          internal.HexBytes `json:"userID" bson:"userid"`
	ProcessID       internal.HexBytes `json:"processID" bson:"processid"`
	Consumed        bool              `json:"consumed" bson:"consumed"`
	ConsumedToken   internal.HexBytes `json:"consumedToken" bson:"consumedtoken"`
	ConsumedAt      time.Time         `json:"consumedAt" bson:"consumedat"`
	ConsumedAddress internal.HexBytes `json:"consumedAddress" bson:"consumedaddress"`
}
