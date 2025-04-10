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
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Validate inputs before starting transaction
	// Check if expiration date is in the future
	if !invite.Expiration.After(time.Now()) {
		return fmt.Errorf("expiration date must be in the future")
	}
	// Check if the role is valid
	if !IsValidUserRole(invite.Role) {
		return fmt.Errorf("invalid role")
	}

	// Execute the operation within a transaction
	return ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		// Check if the organization exists
		if _, err := ms.fetchOrganizationFromDB(sessCtx, invite.OrganizationAddress); err != nil {
			return err
		}
		// Check if the user exists
		user, err := ms.fetchUserFromDB(sessCtx, invite.CurrentUserID)
		if err != nil {
			return err
		}
		// Check if the user is already a member of the organization
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

		// Insert the invitation in the database
		_, err = ms.organizationInvites.InsertOne(sessCtx, invite)
		// Check if the user is already invited to the organization, the error is
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
	})
}

// Invitation returns the invitation for the given code.
func (ms *MongoStorage) Invitation(invitationCode string) (*OrganizationInvite, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read operations don't need transactions, but we'll use a session for consistency
	session, err := ms.DBClient.StartSession()
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	var invite *OrganizationInvite
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		result := ms.organizationInvites.FindOne(sessCtx, bson.M{"invitationCode": invitationCode})
		invite = &OrganizationInvite{}
		if err := result.Decode(invite); err != nil {
			if err == mongo.ErrNoDocuments {
				return ErrNotFound
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return invite, nil
}

// PendingInvitations returns the pending invitations for the given organization.
func (ms *MongoStorage) PendingInvitations(organizationAddress string) ([]OrganizationInvite, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read operations don't need transactions, but we'll use a session for consistency
	session, err := ms.DBClient.StartSession()
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	var invitations []OrganizationInvite
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		cursor, err := ms.organizationInvites.Find(sessCtx, bson.M{"organizationAddress": organizationAddress})
		if err != nil {
			return err
		}
		defer func() {
			if err := cursor.Close(sessCtx); err != nil {
				log.Warnw("error closing cursor", "error", err)
			}
		}()

		invitations = []OrganizationInvite{}
		return cursor.All(sessCtx, &invitations)
	})
	if err != nil {
		return nil, err
	}
	return invitations, nil
}

// DeleteInvitation removes the invitation from the database.
func (ms *MongoStorage) DeleteInvitation(invitationCode string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Execute the operation within a transaction
	return ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		_, err := ms.organizationInvites.DeleteOne(sessCtx, bson.M{"invitationCode": invitationCode})
		return err
	})
}
