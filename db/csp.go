package db

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// CSPAuth represents a user authentication information for a bundle of processes
type CSPAuth struct {
	Token      internal.HexBytes `json:"token" bson:"_id"`
	UserID     internal.HexBytes `json:"userID" bson:"userid"`
	BundleID   internal.HexBytes `json:"bundleID" bson:"bundleid"`
	CreatedAt  time.Time         `json:"createdAt" bson:"createdat"`
	Verified   bool              `json:"verified" bson:"verified"`
	VerifiedAt time.Time         `json:"verifiedAt" bson:"verifiedat"`
}

// CSPProcess is the status of a process in a bundle of processes for a user
type CSPProcess struct {
	ID          internal.HexBytes `json:"id" bson:"_id"` // hash(userID + processID)
	UserID      internal.HexBytes `json:"userID" bson:"userid"`
	ProcessID   internal.HexBytes `json:"processID" bson:"processid"`
	Used        bool              `json:"used" bson:"consumed"`
	UsedToken   internal.HexBytes `json:"usedToken" bson:"consumedtoken"`
	UsedAt      time.Time         `json:"usedAt" bson:"consumedat"`
	UsedAddress internal.HexBytes `json:"usedAddress" bson:"consumedaddress"`
	TimesVoted  int               `json:"timesVoted" bson:"timesVoted"`
}

// SetCSPAuth method stores a new CSP authentication token for a user and a
// bundle of processes. It returns an error if the token, user ID or bundle
// ID are nil.
func (ms *MongoStorage) SetCSPAuth(token, userID, bundleID internal.HexBytes) error {
	if token == nil || userID == nil || bundleID == nil {
		return ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// insert the token
	if _, err := ms.cspTokens.InsertOne(ctx, CSPAuth{
		Token:     token,
		UserID:    userID,
		BundleID:  bundleID,
		CreatedAt: time.Now(),
		Verified:  false,
	}); err != nil {
		return errors.Join(ErrStoreToken, err)
	}
	return nil
}

// CSPAuth method returns the CSP authentication data for a given token. It
// returns an error if the token is nil or the token does not exist.
func (ms *MongoStorage) CSPAuth(token internal.HexBytes) (*CSPAuth, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// find the token
	return ms.fetchCSPAuthFromDB(ctx, token)
}

// LastCSPAuth method returns the last CSP authentication data for a given
// user and bundle of processes. It returns an error if the user ID or bundle
// ID are nil or the token does not exist.
func (ms *MongoStorage) LastCSPAuth(userID, bundleID internal.HexBytes) (*CSPAuth, error) {
	if userID == nil || bundleID == nil {
		return nil, ErrBadInputs
	}
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// generate filter and options to find the last token for the user and
	// bundle
	filter := bson.M{"userid": userID, "bundleid": bundleID}
	opts := options.FindOne().SetSort(bson.M{"createdat": -1})
	tokenData := new(CSPAuth)
	// find the last token
	if err := ms.cspTokens.FindOne(ctx, filter, opts).Decode(tokenData); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrTokenNotFound
		}
		return nil, err
	}
	return tokenData, nil
}

// VerifyCSPAuth method verifies a CSP authentication token. It returns an
// error if the token is nil or the token does not exist.
func (ms *MongoStorage) VerifyCSPAuth(token internal.HexBytes) error {
	if token == nil {
		return ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// ensure that the token exists
	if _, err := ms.fetchCSPAuthFromDB(ctx, token); err != nil {
		return err
	}
	// update the token
	filter := bson.M{"_id": token}
	updateDoc := bson.M{"$set": bson.M{"verified": true, "verifiedat": time.Now()}}
	if _, err := ms.cspTokens.UpdateOne(ctx, filter, updateDoc, nil); err != nil {
		return errors.Join(ErrStoreToken, err)
	}
	return nil
}

// CSPProcess returns the CSPProcess for the given token and processID.
// It returns an error if the token or processID are nil.
func (ms *MongoStorage) CSPProcess(token, processID internal.HexBytes) (*CSPProcess, error) {
	if token == nil || processID == nil {
		return nil, ErrBadInputs
	}
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// get the token data
	tokenData, err := ms.fetchCSPAuthFromDB(ctx, token)
	if err != nil {
		return nil, err
	}
	// find the token status by id
	return ms.fetchCSPProcessFromDB(ctx, tokenData.UserID, processID)
}

// IsCSPProcessConsumed method checks if a CSP process has been consumed by a
// user. It returns an error if the userID or processID are nil. It returns
// true if the process has been consumed, false if it has not been consumed and
// an error if the process does not exist or the token is not verified.
func (ms *MongoStorage) IsCSPProcessConsumed(userID, processID internal.HexBytes) (bool, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// try to find the token status by id
	currentStatus, err := ms.fetchCSPProcessFromDB(ctx, userID, processID)
	if err != nil {
		if err == ErrTokenNotFound {
			return false, nil
		}
		return false, err
	}
	// check if the token is verified
	tokenData, err := ms.fetchCSPAuthFromDB(ctx, currentStatus.UsedToken)
	if err != nil {
		return false, err
	}
	if !tokenData.Verified {
		return false, ErrTokenNotVerified
	}
	return currentStatus.TimesVoted > MaxVoteOverwritesPerProcess, nil
}

// ConsumeCSPProcess method consumes a CSP process for a user. It returns an
// error if the token, processID or address are nil. It returns an error if
// the token does not exist, the process has already been consumed or thecspAuthTokenStatusID(
// token is not verified.
func (ms *MongoStorage) ConsumeCSPProcess(token, processID, address internal.HexBytes) error {
	if token == nil || processID == nil || address == nil {
		return ErrBadInputs
	}
	// lock the keys
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// check if the token exists
	tokenData, err := ms.fetchCSPAuthFromDB(ctx, token)
	if err != nil {
		return err
	}
	// get the token status
	tokenStatus, err := ms.fetchCSPProcessFromDB(ctx, tokenData.UserID, processID)
	if err != nil && !errors.Is(err, ErrTokenNotFound) {
		return err
	}
	// check if the token is already consumed
	if tokenStatus != nil && tokenStatus.TimesVoted > MaxVoteOverwritesPerProcess {
		return ErrProcessAlreadyConsumed
	}
	timesVoted := 1
	if tokenStatus != nil {
		timesVoted = tokenStatus.TimesVoted + 1
		// check if the address is the same as the previous one used to vote
		if tokenStatus.UsedAddress != nil && !tokenStatus.UsedAddress.Equals(address) {
			return ErrInvalidData
		}
	}
	// calculate the status id
	id := cspAuthTokenStatusID(tokenData.UserID, processID)
	// prepare the document to update
	updateDoc, err := dynamicUpdateDocument(CSPProcess{
		ID:          id,
		UserID:      tokenData.UserID,
		ProcessID:   processID,
		Used:        true,
		UsedAt:      time.Now(),
		UsedToken:   token,
		UsedAddress: address,
		TimesVoted:  timesVoted,
	}, nil)
	if err != nil {
		return errors.Join(ErrPrepareDocument, err)
	}
	// set the filter and update options to create the document if it does not
	// exist
	filter := bson.M{"_id": id}
	opts := options.Update().SetUpsert(true)
	// update the token status
	if _, err = ms.cspTokensStatus.UpdateOne(ctx, filter, updateDoc, opts); err != nil {
		return errors.Join(ErrStoreToken, err)
	}
	return nil
}

func (ms *MongoStorage) fetchCSPAuthFromDB(ctx context.Context, token internal.HexBytes) (*CSPAuth, error) {
	tokenData := new(CSPAuth)
	if err := ms.cspTokens.FindOne(ctx, bson.M{"_id": token}).Decode(tokenData); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrTokenNotFound
		}
		return nil, err
	}
	return tokenData, nil
}

func (ms *MongoStorage) fetchCSPProcessFromDB(ctx context.Context, userID, processID internal.HexBytes) (*CSPProcess, error) {
	// calculate the status ID
	id := cspAuthTokenStatusID(userID, processID)
	// find the token status
	tokenStatus := new(CSPProcess)
	if err := ms.cspTokensStatus.FindOne(ctx, bson.M{"_id": id}).Decode(tokenStatus); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrTokenNotFound
		}
		return nil, err
	}
	return tokenStatus, nil
}

func cspAuthTokenStatusID(userID, processID internal.HexBytes) internal.HexBytes {
	hash := sha256.Sum256(append(userID, processID...))
	return internal.HexBytes(hash[:])
}

// CountCSPAuthByBundle counts the total number of CSP authentication tokens
// for a given bundle ID. Returns an error if the bundleID is nil.
func (ms *MongoStorage) CountCSPAuthByBundle(bundleID internal.HexBytes) (int64, error) {
	if bundleID == nil {
		return 0, ErrBadInputs
	}
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// count documents matching the bundle ID
	filter := bson.M{"bundleid": bundleID}
	count, err := ms.cspTokens.CountDocuments(ctx, filter)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// CountCSPAuthVerifiedByBundle counts the number of verified CSP authentication
// tokens for a given bundle ID. Returns an error if the bundleID is nil.
func (ms *MongoStorage) CountCSPAuthVerifiedByBundle(bundleID internal.HexBytes) (int64, error) {
	if bundleID == nil {
		return 0, ErrBadInputs
	}
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// count documents matching the bundle ID and verified status
	filter := bson.M{"bundleid": bundleID, "verified": true}
	count, err := ms.cspTokens.CountDocuments(ctx, filter)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// CountCSPProcessConsumedByProcess counts the number of consumed CSP processes
// for a given process ID. Returns an error if the processID is nil.
func (ms *MongoStorage) CountCSPProcessConsumedByProcess(processID internal.HexBytes) (int64, error) {
	if processID == nil {
		return 0, ErrBadInputs
	}
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// count documents matching the process ID and consumed status
	filter := bson.M{"processid": processID, "consumed": true}
	count, err := ms.cspTokensStatus.CountDocuments(ctx, filter)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// CSPProcessByUserAndProcess retrieves the CSP process status for a given
// user and process. Returns an error if the userID or processID are nil.
func (ms *MongoStorage) CSPProcessByUserAndProcess(userID, processID internal.HexBytes) (*CSPProcess, error) {
	if userID == nil || processID == nil {
		return nil, ErrBadInputs
	}
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// fetch the process status
	return ms.fetchCSPProcessFromDB(ctx, userID, processID)
}
