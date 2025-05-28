package db

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.vocdoni.io/dvote/log"
)

// CreateInvitation creates a new invitation for a user to join an organization.
func (ms *MongoStorage) CreateInvitation(invite *OrganizationInvite) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
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
	invite.ID = primitive.NewObjectID()
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

// Invitation returns the invitation for the given ID.
func (ms *MongoStorage) Invitation(invitationID string) (*OrganizationInvite, error) {
	objID, err := primitive.ObjectIDFromHex(invitationID)
	if err != nil {
		return nil, fmt.Errorf("invalid invitation ID: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	result := ms.organizationInvites.FindOne(ctx, bson.M{"_id": objID})
	invite := &OrganizationInvite{}
	if err := result.Decode(invite); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return invite, nil
}

// UpdateInvitation updates an existing invitation in the database.
func (ms *MongoStorage) UpdateInvitation(invite *OrganizationInvite) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if invite.ID == primitive.NilObjectID {
		return fmt.Errorf("invitation ID cannot be empty")
	}
	updateDoc, err := dynamicUpdateDocument(invite, nil)
	if err != nil {
		return err
	}
	filter := bson.M{"_id": invite.ID}
	// update the invitation in the database
	_, err = ms.organizationInvites.UpdateOne(ctx, filter, updateDoc)
	return err
}

// InvitationByCode returns the invitation for the given code.
func (ms *MongoStorage) InvitationByCode(invitationCode string) (*OrganizationInvite, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
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

// InvitationByEmail returns the invitation for the given email.
func (ms *MongoStorage) InvitationByEmail(email string) (*OrganizationInvite, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	result := ms.organizationInvites.FindOne(ctx, bson.M{"newUserEmail": email})
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
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
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

// deleteInvitation is a helper function to delete an invitation based on the provided filter.
func (ms *MongoStorage) deleteInvitationHelper(filter bson.M) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Delete the invitation from the database
	_, err := ms.organizationInvites.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to delete invitation: %w", err)
	}
	return nil
}

// DeleteInvitation removes the invitation from the database by its ID.
func (ms *MongoStorage) DeleteInvitation(invitationID string) error {
	objID, err := primitive.ObjectIDFromHex(invitationID)
	if err != nil {
		return fmt.Errorf("invalid invitation ID: %w", err)
	}

	return ms.deleteInvitationHelper(bson.M{"_id": objID})
}

// DeleteInvitationByCode removes the invitation from the database.
func (ms *MongoStorage) DeleteInvitationByCode(invitationCode string) error {
	return ms.deleteInvitationHelper(bson.M{"invitationCode": invitationCode})
}

// DeleteInvitationByEmail removes the invitation from the database.
func (ms *MongoStorage) DeleteInvitationByEmail(email string) error {
	return ms.deleteInvitationHelper(bson.M{"newUserEmail": email})
}
