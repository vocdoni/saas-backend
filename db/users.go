package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// nextUserID internal method returns the next available user ID. If an error
// occurs, it returns the error.
func (ms *MongoStorage) nextUserID(ctx context.Context) (uint64, error) {
	var user User
	opts := options.FindOne().SetSort(bson.D{{Key: "_id", Value: -1}})
	if err := ms.users.FindOne(ctx, bson.M{}, opts).Decode(&user); err != nil {
		if err == mongo.ErrNoDocuments {
			return 1, nil
		}
		return 0, err
	}
	return user.ID + 1, nil
}

// addOrganizationToUser internal method adds the organization to the user with
// the given email. If an error occurs, it returns the error.
func (ms *MongoStorage) addOrganizationToUser(ctx context.Context, userEmail, address string, role UserRole) error {
	// check if the user exists after add the organization
	filter := bson.M{"email": userEmail}
	count, err := ms.users.CountDocuments(ctx, filter)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return ErrNotFound
		}
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	// add the organization to the user
	updateDoc := bson.M{
		"$addToSet": bson.M{
			"organizations": OrganizationMember{
				Address: address,
				Role:    role,
			},
		},
	}
	if _, err := ms.users.UpdateOne(ctx, filter, updateDoc); err != nil {
		log.Warnw("error adding organization to user", "error", err)
		return err
	}
	return nil
}

func (ms *MongoStorage) fetchUserFromDB(ctx context.Context, id uint64) (*User, error) {
	// find the user in the database
	result := ms.users.FindOne(ctx, bson.M{"_id": id})
	user := &User{}
	if err := result.Decode(user); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return user, nil
}

// User method returns the user with the given ID. If the user doesn't exist, it
// returns a specific error. If other errors occur, it returns the error.
func (ms *MongoStorage) User(id uint64) (*User, error) {
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read operations don't need transactions, but we'll use a session for consistency
	session, err := ms.DBClient.StartSession()
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	var user *User
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		var err error
		user, err = ms.fetchUserFromDB(sessCtx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}

// UserByEmail method returns the user with the given email. If the user doesn't
// exist, it returns a specific error. If other errors occur, it returns the
// error.
func (ms *MongoStorage) UserByEmail(email string) (*User, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read operations don't need transactions, but we'll use a session for consistency
	session, err := ms.DBClient.StartSession()
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	var user *User
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		result := ms.users.FindOne(sessCtx, bson.M{"email": email})
		user = &User{}
		if err := result.Decode(user); err != nil {
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
	return user, nil
}

// SetUser method creates or updates the user in the database. If the user
// already exists, it updates the fields that have changed. If the user doesn't
// exist, it creates it. If an error occurs, it returns the error.
func (ms *MongoStorage) SetUser(user *User) (uint64, error) {
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// If the user provided doesn't have organizations, create an empty slice
	if user.Organizations == nil {
		user.Organizations = []OrganizationMember{}
	}

	var userID uint64
	// Execute the operation within a transaction
	err := ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		// Get the next available user ID
		nextID, err := ms.nextUserID(sessCtx)
		if err != nil {
			return err
		}

		// Check if the user exists or needs to be created
		if user.ID > 0 {
			if user.ID >= nextID {
				return ErrInvalidData
			}
			// If the user exists, update it with the new data
			updateDoc, err := dynamicUpdateDocument(user, nil)
			if err != nil {
				return err
			}
			_, err = ms.users.UpdateOne(sessCtx, bson.M{"_id": user.ID}, updateDoc)
			if err != nil {
				return err
			}
			userID = user.ID
		} else {
			// If the user doesn't exist, create it setting the ID first
			user.ID = nextID
			if _, err := ms.users.InsertOne(sessCtx, user); err != nil {
				if strings.Contains(err.Error(), "duplicate key error") {
					return ErrAlreadyExists
				}
				return err
			}
			userID = nextID
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return userID, nil
}

// DelUser method deletes the user from the database. If an error occurs, it
// returns the error.
func (ms *MongoStorage) DelUser(user *User) error {
	// Check if the user is valid (has an ID or an email)
	if user.ID == 0 && user.Email == "" {
		return ErrInvalidData
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Execute the operation within a transaction
	return ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		// Delete the user from the database using the ID or the email
		filter := bson.M{"_id": user.ID}
		if user.ID == 0 {
			filter = bson.M{"email": user.Email}
		}
		_, err := ms.users.DeleteOne(sessCtx, filter)
		return err
	})
}

// VerifyUserAccount method verifies the user provided, modifying the user to
// mark as verified and removing the verification code. If an error occurs, it
// returns the error.
func (ms *MongoStorage) VerifyUserAccount(user *User) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Execute the operation within a transaction
	return ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		// Try to get the user to ensure it exists
		if _, err := ms.fetchUserFromDB(sessCtx, user.ID); err != nil {
			return err
		}
		// Update the user to mark as verified
		filter := bson.M{"_id": user.ID}
		if _, err := ms.users.UpdateOne(sessCtx, filter, bson.M{"$set": bson.M{"verified": true}}); err != nil {
			return err
		}
		// Remove the verification code
		return ms.delVerificationCode(sessCtx, user.ID, CodeTypeVerifyAccount)
	})
}

// IsMemberOf method checks if the user with the given email is a member of the
// organization with the given address and role. If the user is a member, it
// returns true. If the user is not a member, it returns false. If an error
// occurs, it returns the error.
func (ms *MongoStorage) IsMemberOf(userEmail, organizationAddress string, role UserRole) (bool, error) {
	// This function already uses UserByEmail which now uses sessions
	user, err := ms.UserByEmail(userEmail)
	if err != nil {
		return false, err
	}
	for _, org := range user.Organizations {
		if org.Address == organizationAddress {
			return org.Role == role, nil
		}
	}
	return false, ErrNotFound
}
