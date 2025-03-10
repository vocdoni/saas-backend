package csp

import (
	"fmt"

	"github.com/vocdoni/saas-backend/csp/storage"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/log"
)

type CSP struct {
	storage storage.Storage
}

// Indexer takes a unique user identifier and returns the list of processIDs
// where the user is eligible for participation. This includes both individual
// processes and process bundles. This is a helper function that might not be
// implemented in all cases.
func (c *CSP) Indexer(participantId, bundleId, electionId string) []Election {
	if len(participantId) == 0 {
		log.Warnw("no participant ID provided")
		return nil
	}
	// create userID either based on bundleId or electionId
	var userID internal.HexBytes
	switch {
	case len(bundleId) != 0: // bundleId is provided
		bundleIDBytes := internal.HexBytes{}
		if err := bundleIDBytes.FromString(bundleId); err != nil {
			return nil
		}
		userID = buildUserID(participantId, bundleIDBytes)
	case len(electionId) != 0: // electionId is provided
		electionIDBytes := internal.HexBytes{}
		if err := electionIDBytes.FromString(electionId); err != nil {
			return nil
		}
		userID = buildUserID(participantId, electionIDBytes)
	default:
		log.Warnw("no bundle ID or election ID provided")
		return nil
	}
	// get the user from the database based on the userID
	user, err := c.storage.User(userID)
	if err != nil {
		log.Warnw("cannot get indexer elections", "error", err)
		return nil
	}
	indexerElections := []Election{}
	for _, e := range user.Elections {
		ie := Election{
			RemainingAttempts: e.RemainingAttempts,
			Consumed:          e.Consumed,
			ElectionID:        e.ElectionID,
			ExtraData:         []string{user.ExtraData},
			Voted:             e.Voted,
		}
		indexerElections = append(indexerElections, ie)
	}
	return indexerElections
}

// NewUserData methods creates a new UserData object based on the provided
// parameters. The internalID is a unique identifier for the user, and the
// electionIDs is a list of elections where the user is eligible for
// participation. The resulting user ID is generated based on the internalID
// and the userID provided. The internalID use to be the bundleID or the
// electionID.
func (c *CSP) NewUserData(internalID internal.HexBytes, uid, phone, email string, eIDs []internal.HexBytes) (
	*storage.UserData, error,
) {
	if len(internalID) == 0 {
		return nil, fmt.Errorf("no internal ID provided for the user")
	}
	if len(uid) == 0 {
		return nil, fmt.Errorf("no user ID provided for the user")
	}
	if len(phone) == 0 && len(email) == 0 {
		return nil, fmt.Errorf("no phone or email provided for the user")
	}
	user := &storage.UserData{
		UserID: buildUserID(uid, internalID),
		Phone:  phone,
		Mail:   email,
	}
	for _, eid := range eIDs {
		user.Elections[eid.String()] = storage.UserElection{
			ElectionID:        eid,
			RemainingAttempts: c.storage.MaxAttempts(),
		}
	}
	return user, nil
}

// AddUser method registers the users to the storage. It calls the storage
// BultAddUser method with the list of users provided. The users should be
// created with the NewUserData method.
func (c *CSP) AddUsers(users []storage.UserData) error {
	return c.storage.BulkAddUser(users)
}
