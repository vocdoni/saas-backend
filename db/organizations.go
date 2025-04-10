package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.vocdoni.io/dvote/log"
)

func (ms *MongoStorage) fetchOrganizationFromDB(ctx context.Context, address string) (*Organization, error) {
	// find the organization in the database by its address (case insensitive)
	filter := bson.M{"_id": bson.M{"$regex": address, "$options": "i"}}
	result := ms.organizations.FindOne(ctx, filter)
	org := &Organization{Subscription: OrganizationSubscription{}}
	if err := result.Decode(org); err != nil {
		// if the organization doesn't exist return a specific error
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return org, nil
}

// Organization method returns the organization with the given address.
// If the organization doesn't exist, it returns the specific error.
// If other errors occur, it returns the error.
func (ms *MongoStorage) Organization(address string) (*Organization, error) {
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read operations don't need transactions, but we'll use a session for consistency
	session, err := ms.DBClient.StartSession()
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	var org *Organization
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		var err error
		org, err = ms.fetchOrganizationFromDB(sessCtx, address)
		return err
	})
	if err != nil {
		return nil, err
	}
	return org, nil
}

// OrganizationWithParent method returns the organization with the given address
// and its parent organization if it exists. If the organization doesn't exist
// or the parent organization doesn't exist, it returns the specific error.
// If other errors occur, it returns the error.
func (ms *MongoStorage) OrganizationWithParent(address string) (org *Organization, parent *Organization, err error) {
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read operations don't need transactions, but we'll use a session for consistency
	session, err := ms.DBClient.StartSession()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		// Find the organization in the database
		org, err = ms.fetchOrganizationFromDB(sessCtx, address)
		if err != nil {
			return err
		}
		if org.Parent == "" {
			return nil
		}
		// Find the parent organization in the database
		parent, err = ms.fetchOrganizationFromDB(sessCtx, org.Parent)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return org, parent, nil
}

// SetOrganization method creates or updates the organization in the database.
// If the organization already exists, it updates the fields that have changed.
// If the organization doesn't exist, it creates it. If an error occurs, it
// returns the error.
func (ms *MongoStorage) SetOrganization(org *Organization) error {
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Execute the operation within a transaction
	return ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		// Prepare the document to be updated in the database modifying only the
		// fields that have changed
		// Define 'active' parameter to be updated always to update it even its new
		// value is false
		updateDoc, err := dynamicUpdateDocument(org, []string{"active"})
		if err != nil {
			return err
		}
		// Set upsert to true to create the document if it doesn't exist
		opts := options.Update().SetUpsert(true)
		if _, err := ms.organizations.UpdateOne(sessCtx, bson.M{"_id": org.Address}, updateDoc, opts); err != nil {
			if strings.Contains(err.Error(), "duplicate key error") {
				return ErrAlreadyExists
			}
			return err
		}
		// Assign organization to the creator if it's not empty including the address
		// in the organizations list of the user if it's not already there as admin
		if org.Creator != "" {
			if err := ms.addOrganizationToUser(sessCtx, org.Creator, org.Address, AdminRole); err != nil {
				// If an error occurs, delete the organization from the database
				if _, delErr := ms.organizations.DeleteOne(sessCtx, bson.M{"_id": org.Address}); delErr != nil {
					return errors.Join(err, delErr)
				}
				return err
			}
		}
		return nil
	})
}

// DelOrganization method deletes the organization from the database. If an
// error occurs, it returns the error.
func (ms *MongoStorage) DelOrganization(org *Organization) error {
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Execute the operation within a transaction
	return ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		// Delete the organization from the database
		_, err := ms.organizations.DeleteOne(sessCtx, bson.M{"_id": org.Address})
		return err
	})
}

// ReplaceCreatorEmail method replaces the creator email in the organizations
// where it is the creator. If an error occurs, it returns the error.
func (ms *MongoStorage) ReplaceCreatorEmail(oldEmail, newEmail string) error {
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Execute the operation within a transaction
	return ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		// Update the creator email in the organizations where it is the creator
		updateDoc := bson.M{"$set": bson.M{"creator": newEmail}}
		if _, err := ms.organizations.UpdateMany(sessCtx, bson.M{"creator": oldEmail}, updateDoc); err != nil {
			return err
		}
		return nil
	})
}

// OrganizationsMembers method returns the users that are members of the
// organization with the given address. If an error occurs, it returns the
// error.
func (ms *MongoStorage) OrganizationsMembers(address string) ([]User, error) {
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read operations don't need transactions, but we'll use a session for consistency
	session, err := ms.DBClient.StartSession()
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	var users []User
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		// Find the organization in the database
		filter := bson.M{
			"organizations": bson.M{
				"$elemMatch": bson.M{
					"_id": address,
				},
			},
		}
		users = []User{}
		cursor, err := ms.users.Find(sessCtx, filter)
		if err != nil {
			return err
		}
		defer func() {
			if err := cursor.Close(sessCtx); err != nil {
				log.Warnw("error closing cursor", "error", err)
			}
		}()
		return cursor.All(sessCtx, &users)
	})
	if err != nil {
		return nil, err
	}
	return users, nil
}

// SetOrganizationSubscription method adds the provided subscription to
// the organization with the given address
func (ms *MongoStorage) SetOrganizationSubscription(address string, orgSubscription *OrganizationSubscription) error {
	// Validate the plan ID before starting the transaction
	if _, err := ms.Plan(orgSubscription.PlanID); err != nil {
		return ErrInvalidData
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Execute the operation within a transaction
	return ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		// Prepare the document to be updated in the database
		filter := bson.M{"_id": address}
		updateDoc := bson.M{"$set": bson.M{"subscription": orgSubscription}}
		// Update the organization in the database
		if _, err := ms.organizations.UpdateOne(sessCtx, filter, updateDoc); err != nil {
			return err
		}
		return nil
	})
}
