package db

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.vocdoni.io/dvote/log"
)

// CreateInvitation creates a new invitation for a user to join an organization.
func (ms *MongoStorage) CreateInvitation(invite *OrganizationInvite) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// check if the organization exists
	if _, err := ms.fetchOrganizationFromDB(ctx, invite.OrganizationAddress); err != nil {
		return err
	}
	// check if the user exists
	user, err := ms.fetchUserFromDB(ctx, invite.CurrentUserID)
	if err != nil {
		return err
	}
	// check if the user is already a member of the organization
	partOfOrg := false
	for _, org := range user.Organizations {
		if org.Address == invite.OrganizationAddress {
			partOfOrg = true
			break
		}
	}
	if !partOfOrg {
		return fmt.Errorf("user is not part of the organization")
	}
	// check if expiration date is in the future
	if !invite.Expiration.After(time.Now()) {
		return fmt.Errorf("expiration date must be in the future")
	}
	// check if the role is valid
	if !IsValidUserRole(invite.Role) {
		return fmt.Errorf("invalid role")
	}
	// insert the invitation in the database
	_, err = ms.organizationInvites.InsertOne(ctx, invite)
	// check if the user is already invited to the organization, the error is
	// about the unique index
	if merr, ok := err.(mongo.WriteException); ok {
		for _, we := range merr.WriteErrors {
			// duplicate key error has the code 11000:
			// https://www.mongodb.com/docs/manual/reference/error-codes
			if we.Code == 11000 {
				return ErrAlreadyExists
			}
		}
	}
	return err
}

// Invitation returns the invitation for the given code.
func (ms *MongoStorage) Invitation(invitationCode string) (*OrganizationInvite, error) {
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

// PendingInvitations returns the pending invitations for the given organization.
func (ms *MongoStorage) PendingInvitations(organizationAddress string) ([]OrganizationInvite, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cursor, err := ms.organizationInvites.Find(ctx, bson.M{"organizationAddress": organizationAddress})
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()
	invitations := []OrganizationInvite{}
	if err := cursor.All(ctx, &invitations); err != nil {
		return nil, err
	}
	return invitations, nil
}

// DeleteInvitation removes the invitation from the database.
func (ms *MongoStorage) DeleteInvitation(invitationCode string) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := ms.organizationInvites.DeleteOne(ctx, bson.M{"invitationCode": invitationCode})
	return err
}
