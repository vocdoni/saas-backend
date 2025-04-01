package storage

import (
	"time"

	"github.com/vocdoni/saas-backend/internal"
)

// CSPAuth represents a user authentication information of an user for a bundle
// of processes. It is used to authenticate the user in the CSP. The auth data
// is generated by the CSP with a challenge that should be solved by the user
// to verify the user identity. Once the auth data is verified, the user is
// authenticated in the CSP and can consume the processes of the bundle with
// the token that it contains.
type CSPAuth struct {
	Token      internal.HexBytes `json:"token" bson:"_id"`
	UserID     internal.HexBytes `json:"userID" bson:"userid"`
	BundleID   internal.HexBytes `json:"bundleID" bson:"bundleid"`
	CreatedAt  time.Time         `json:"createdAt" bson:"createdat"`
	Verified   bool              `json:"verified" bson:"verified"`
	VerifiedAt time.Time         `json:"verifiedAt" bson:"verifiedat"`
}

// CSPProcess is the status of a process in a bundle of processes for a
// user. It is used to track the status of the process in the bundle, mainly if
// it has been consumed or not by the user. To consume a process, the user must
// have a verified auth in the CSP. Once the process is consumed, the user
// cannot use the same token to consume the same process again. It stores the
// consumed token, the consumed time and the consumed address.
type CSPProcess struct {
	ID              internal.HexBytes `json:"id" bson:"_id"` // hash(userID + processID)
	UserID          internal.HexBytes `json:"userID" bson:"userid"`
	ProcessID       internal.HexBytes `json:"processID" bson:"processid"`
	Consumed        bool              `json:"consumed" bson:"consumed"`
	ConsumedToken   internal.HexBytes `json:"consumedToken" bson:"consumedtoken"`
	ConsumedAt      time.Time         `json:"consumedAt" bson:"consumedat"`
	ConsumedAddress internal.HexBytes `json:"consumedAddress" bson:"consumedaddress"`
}
