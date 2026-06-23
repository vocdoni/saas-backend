package db

import (
	"context"
	"errors"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// APIKey is a programmatic credential owned by an (integrator) organization. Only the SHA-256
// hash of the secret is stored; the plaintext secret is shown to the user once at creation.
type APIKey struct {
	ID         string         `json:"id" bson:"_id"`
	OrgAddress common.Address `json:"orgAddress" bson:"orgAddress"`
	Label      string         `json:"label" bson:"label"`
	// Prefix is the first, non-secret part of the key (e.g. "vsk_1a2b3c4d"), kept for display so
	// a user can identify a key without revealing the secret.
	Prefix string `json:"prefix" bson:"prefix"`
	// Hash is the hex-encoded SHA-256 of the full secret, used to resolve a presented key.
	Hash string `json:"-" bson:"hash"`
	// Scopes the key is allowed to use (see the api package for the canonical scope set).
	Scopes []string `json:"scopes" bson:"scopes"`
	// CreatedBy is the email of the user (admin) who created the key.
	CreatedBy  string     `json:"createdBy" bson:"createdBy"`
	CreatedAt  time.Time  `json:"createdAt" bson:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty" bson:"lastUsedAt,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty" bson:"expiresAt,omitempty"`
	Revoked    bool       `json:"revoked" bson:"revoked"`
}

// SetAPIKey inserts a new API key. The key's ID and Hash must be set by the caller.
func (ms *MongoStorage) SetAPIKey(key *APIKey) error {
	if key.ID == "" || key.Hash == "" {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.apiKeys.InsertOne(ctx, key); err != nil {
		return err
	}
	return nil
}

// APIKeyByHash returns the non-revoked API key matching the given secret hash. A revoked or
// expired key is treated as not found.
func (ms *MongoStorage) APIKeyByHash(hash string) (*APIKey, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	key := &APIKey{}
	if err := ms.apiKeys.FindOne(ctx, bson.M{"hash": hash, "revoked": false}).Decode(key); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if key.ExpiresAt != nil && key.ExpiresAt.Before(time.Now()) {
		return nil, ErrNotFound
	}
	return key, nil
}

// APIKeysByOrg returns all API keys owned by the given organization, most recent first.
func (ms *MongoStorage) APIKeysByOrg(orgAddress common.Address) ([]*APIKey, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	cursor, err := ms.apiKeys.Find(ctx, bson.M{"orgAddress": orgAddress},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()
	keys := []*APIKey{}
	if err := cursor.All(ctx, &keys); err != nil {
		return nil, err
	}
	return keys, nil
}

// APIKeyByID returns a single API key by its ID, scoped to the owning organization.
func (ms *MongoStorage) APIKeyByID(orgAddress common.Address, id string) (*APIKey, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	key := &APIKey{}
	if err := ms.apiKeys.FindOne(ctx, bson.M{"_id": id, "orgAddress": orgAddress}).Decode(key); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return key, nil
}

// RevokeAPIKey marks the org's API key as revoked. Returns ErrNotFound if no such key exists.
func (ms *MongoStorage) RevokeAPIKey(orgAddress common.Address, id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.apiKeys.UpdateOne(ctx,
		bson.M{"_id": id, "orgAddress": orgAddress},
		bson.M{"$set": bson.M{"revoked": true}},
	)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchAPIKey records the last time a key was used. Best-effort: errors are returned but callers
// typically log and ignore them so auth is not blocked by a counter update.
func (ms *MongoStorage) TouchAPIKey(id string, when time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	_, err := ms.apiKeys.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"lastUsedAt": when}})
	return err
}
