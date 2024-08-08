package db

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// nextUserID internal method returns the next available user ID. If an error
// occurs, it returns the error. This method must be called with the keysLock
// held.
func (ms *MongoStorage) nextUserID(ctx context.Context) (uint64, error) {
	var user User
	opts := options.FindOne().SetSort(bson.D{{Key: "_id", Value: -1}})
	if err := ms.users.FindOne(ctx, bson.M{}, opts).Decode(&user); err != nil {
		if err == mongo.ErrNoDocuments {
			return 1, nil
		} else {
			return 0, err
		}
	}
	return user.ID + 1, nil
}

// addOrganizationToUser internal method adds the organization to the user with
// the given email. If an error occurs, it returns the error. This method must
// be called with the keysLock held.
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

// UserByEmail method returns the user with the given email. If the user doesn't
// exist, it returns a specific error. If other errors occur, it returns the
// error.
func (ms *MongoStorage) UserByEmail(email string) (*User, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := ms.users.FindOne(ctx, bson.M{"email": email})
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
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
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

// SetUser method creates or updates the user in the database. If the user
// already exists, it updates the fields that have changed. If the user doesn't
// exist, it creates it. If an error occurs, it returns the error.
func (ms *MongoStorage) SetUser(user *User) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// check if the user exists or needs to be created
	if user.ID > 0 {
		// if the user exists, update it with the new data
		updateDoc, err := dynamicUpdateDocument(user, nil)
		if err != nil {
			return err
		}
		_, err = ms.users.UpdateOne(ctx, bson.M{"_id": user.ID}, updateDoc)
		if err != nil {
			return err
		}
	} else {
		// if the user doesn't exist, create it setting the ID first
		var err error
		if user.ID, err = ms.nextUserID(ctx); err != nil {
			return err
		}
		if user.Organizations == nil {
			user.Organizations = []OrganizationMember{}
		}
		if _, err := ms.users.InsertOne(ctx, user); err != nil {
			return err
		}
	}
	return nil
}

// DelUser method deletes the user from the database. If an error occurs, it
// returns the error.
func (ms *MongoStorage) DelUser(user *User) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// delete the user from the database
	_, err := ms.users.DeleteOne(ctx, bson.M{"_id": user.ID})
	return err
}

// IsMemberOf method checks if the user with the given email is a member of the
// organization with the given address and role. If the user is a member, it
// returns true. If the user is not a member, it returns false. If an error
// occurs, it returns the error.
func (ms *MongoStorage) IsMemberOf(userEmail, organizationAddress string, role UserRole) (bool, error) {
	user, err := ms.UserByEmail(userEmail)
	if err != nil {
		return false, err
	}
	for _, org := range user.Organizations {
		if org.Address == organizationAddress && org.Role == role {
			return true, nil
		}
	}
	return false, nil
}