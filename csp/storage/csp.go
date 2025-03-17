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
	_, err := ms.cspTokens.InsertOne(ctx, CSPAuthToken{
		Token:     token,
		UserID:    userID,
		BundleID:  bundleID,
		CreatedAt: time.Now(),
		Verified:  false,
	})
	return err
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
	_, err := ms.cspTokens.UpdateOne(ctx, bson.M{"_id": token},
		bson.M{"$set": bson.M{"verified": true, "verifiedAt": time.Now()}}, nil)
	return err
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
	// find the token status
	return ms.cspAuthTokenStatus(ctx, token, pid)
}

func (ms *MongoStorage) ConsumeCSPAuthToken(token, pid, address internal.HexBytes) error {
	if token == nil || pid == nil || address == nil {
		return ErrBadInputs
	}
	// calculate the id -> hash(token + pid)
	hash := sha256.Sum256(append(token, pid...))
	id := internal.HexBytes(hash[:])
	// lock the keys
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// check if the token exists
	if _, err := ms.cspAuthToken(ctx, token); err != nil {
		return err
	}
	// get the token status
	tokenStatus, err := ms.cspAuthTokenStatus(ctx, token, pid)
	if err != nil && !errors.Is(err, ErrTokenNotFound) {
		return err
	}
	// check if the token is already consumed
	if tokenStatus != nil && tokenStatus.Consumed {
		return nil
	}
	// prepare the document to update
	updateDoc, err := dynamicUpdateDocument(CSPAuthTokenStatus{
		ID:              id,
		Token:           token,
		Consumed:        true,
		ConsumedAt:      time.Now(),
		ConsumedPID:     pid,
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
	_, err = ms.cspTokensStatus.UpdateOne(ctx, filter, updateDoc, opts)
	return err
}

func (ms *MongoStorage) cspAuthToken(ctx context.Context, token internal.HexBytes) (*CSPAuthToken, error) {
	tokenData := new(CSPAuthToken)
	if err := ms.cspTokens.FindOne(ctx, bson.M{"_id": token}).Decode(tokenData); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, errors.Join(ErrTokenNotFound, err)
		}
		return nil, err
	}
	return tokenData, nil
}

func (ms *MongoStorage) cspAuthTokenStatus(ctx context.Context, token, pid internal.HexBytes) (*CSPAuthTokenStatus, error) {
	// calculate the id
	id := cspAuthTokenStatusID(token, pid)
	// find the token status
	tokenStatus := new(CSPAuthTokenStatus)
	err := ms.cspTokensStatus.FindOne(ctx, bson.M{"_id": id}).Decode(tokenStatus)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, errors.Join(ErrTokenNotFound, err)
		}
		return nil, err
	}
	return tokenStatus, nil
}

func cspAuthTokenStatusID(token, pid internal.HexBytes) internal.HexBytes {
	hash := sha256.Sum256(append(token, pid...))
	return internal.HexBytes(hash[:])
}
