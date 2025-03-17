package storage

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

func (ms *MongoStorage) SetCSPAuthToken(token, userID, bundleID internal.HexBytes) error {
	if token == nil || userID == nil || bundleID == nil {
		return ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// insert the token
	if _, err := ms.cspTokens.InsertOne(ctx, CSPAuthToken{
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

func (ms *MongoStorage) CSPAuthToken(token internal.HexBytes) (*CSPAuthToken, error) {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// find the token
	return ms.cspAuthToken(ctx, token)
}

func (ms *MongoStorage) LastCSPAuthToken(userID, bundleID internal.HexBytes) (*CSPAuthToken, error) {
	if userID == nil || bundleID == nil {
		return nil, ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// generate filter and options to find the last token for the user and
	// bundle
	filter := bson.M{"userid": userID, "bundleid": bundleID}
	opts := options.FindOne().SetSort(bson.M{"createdat": -1})
	tokenData := new(CSPAuthToken)
	// find the last token
	if err := ms.cspTokens.FindOne(ctx, filter, opts).Decode(tokenData); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrTokenNotFound
		}
		return nil, err
	}
	return tokenData, nil
}

func (ms *MongoStorage) VerifyCSPAuthToken(token internal.HexBytes) error {
	if token == nil {
		return ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// ensure that the token exists
	if _, err := ms.cspAuthToken(ctx, token); err != nil {
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

func (ms *MongoStorage) CSPAuthTokenStatus(token, pid internal.HexBytes) (*CSPAuthTokenStatus, error) {
	if token == nil || pid == nil {
		return nil, ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// get the token data
	tokenData, err := ms.cspAuthToken(ctx, token)
	if err != nil {
		return nil, err
	}
	// find the token status by id
	return ms.cspAuthTokenStatus(ctx, cspAuthTokenStatusID(tokenData.UserID, pid))
}

func (ms *MongoStorage) IsPIDConsumedCSP(userID, processID internal.HexBytes) (bool, error) {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// try to find the token status by id
	currentStatus, err := ms.cspAuthTokenStatus(ctx, cspAuthTokenStatusID(userID, processID))
	if err != nil {
		if err == ErrTokenNotFound {
			return false, nil
		}
		return false, err
	}
	// check if the token is verified
	tokenData, err := ms.cspAuthToken(ctx, currentStatus.ConsumedToken)
	if err != nil {
		return false, err
	}
	if !tokenData.Verified {
		return false, ErrTokenNoVerified
	}
	return currentStatus.Consumed, nil
}

func (ms *MongoStorage) ConsumeCSPAuthToken(token, pid, address internal.HexBytes) error {
	if token == nil || pid == nil || address == nil {
		return ErrBadInputs
	}
	// lock the keys
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// check if the token exists
	tokenData, err := ms.cspAuthToken(ctx, token)
	if err != nil {
		return err
	}
	// calculate the status id
	id := cspAuthTokenStatusID(tokenData.UserID, pid)
	// get the token status
	tokenStatus, err := ms.cspAuthTokenStatus(ctx, id)
	if err != nil && !errors.Is(err, ErrTokenNotFound) {
		return err
	}
	// check if the token is already consumed
	if tokenStatus != nil && tokenStatus.Consumed {
		return ErrProcessAlreadyConsumed
	}
	// prepare the document to update
	updateDoc, err := dynamicUpdateDocument(CSPAuthTokenStatus{
		ID:              id,
		UserID:          tokenData.UserID,
		ProcessID:       pid,
		Consumed:        true,
		ConsumedAt:      time.Now(),
		ConsumedToken:   token,
		ConsumedAddress: address,
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

func (ms *MongoStorage) cspAuthToken(ctx context.Context, token internal.HexBytes) (*CSPAuthToken, error) {
	tokenData := new(CSPAuthToken)
	if err := ms.cspTokens.FindOne(ctx, bson.M{"_id": token}).Decode(tokenData); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrTokenNotFound
		}
		return nil, err
	}
	return tokenData, nil
}

func (ms *MongoStorage) cspAuthTokenStatus(ctx context.Context, id internal.HexBytes) (*CSPAuthTokenStatus, error) {
	// find the token status
	tokenStatus := new(CSPAuthTokenStatus)
	if err := ms.cspTokensStatus.FindOne(ctx, bson.M{"_id": id}).Decode(tokenStatus); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrTokenNotFound
		}
		return nil, err
	}
	return tokenStatus, nil
}

func cspAuthTokenStatusID(uid, pid internal.HexBytes) internal.HexBytes {
	hash := sha256.Sum256(append(uid, pid...))
	return internal.HexBytes(hash[:])
}
