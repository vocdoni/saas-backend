package db

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// CreateInvitation creates a new invitation for a user to join an organization.
func (ms *MongoStorage) CreateInvitation(invite *OrganizationInvite) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := ms.organizationInvites.InsertOne(ctx, invite)
	return err
}

// Invitation returns the invitation for the given code.
func (ms *MongoStorage) Invitation(invitationCode string) (*OrganizationInvite, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := ms.organizationInvites.FindOne(ctx, bson.M{"invitationCode": invitationCode})
	invite := &OrganizationInvite{}
	if err := result.Decode(invite); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return invite, nil
}

// DeclineInvitation removes the invitation from the database.
func (ms *MongoStorage) DeclineInvitation(invitationCode string) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := ms.organizationInvites.DeleteOne(ctx, bson.M{"invitationCode": invitationCode})
	return err
}
