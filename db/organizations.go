package db

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Organization method returns the organization with the given address. If the
// parent flag is true, it also returns the parent organization if it exists. If
// the organization doesn't exist or the parent organization doesn't exist and
// it should be returned, it returns the specific error. If other errors occur,
// it returns the error.
func (ms *MongoStorage) Organization(address string, parent bool) (*Organization, *Organization, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// find the organization in the database
	result := ms.organizations.FindOne(ctx, bson.M{"_id": address})
	org := &Organization{Subscription: OrganizationSubscription{}}
	if err := result.Decode(org); err != nil {
		// if the organization doesn't exist return a specific error
		if err == mongo.ErrNoDocuments {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	if !parent || org.Parent == "" {
		return org, nil, nil
	}
	// find the parent organization in the database
	result = ms.organizations.FindOne(ctx, bson.M{"_id": org.Parent})
	parentOrg := &Organization{}
	if err := result.Decode(parentOrg); err != nil {
		// if the parent organization doesn't exist return a specific error
		if err == mongo.ErrNoDocuments {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	return org, parentOrg, nil
}

// SetOrganization method creates or updates the organization in the database.
// If the organization already exists, it updates the fields that have changed.
// If the organization doesn't exist, it creates it. If an error occurs, it
// returns the error.
func (ms *MongoStorage) SetOrganization(org *Organization) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// prepare the document to be updated in the database modifying only the
	// fields that have changed
	// define 'active' parameter to be updated always to update it even its new
	// value is false
	updateDoc, err := dynamicUpdateDocument(org, []string{"active"})
	if err != nil {
		return err
	}
	// set upsert to true to create the document if it doesn't exist
	opts := options.Update().SetUpsert(true)
	if _, err := ms.organizations.UpdateOne(ctx, bson.M{"_id": org.Address}, updateDoc, opts); err != nil {
		if strings.Contains(err.Error(), "duplicate key error") {
			return ErrAlreadyExists
		}
		return err
	}
	// assing organization to the creator if it's not empty including the address
	// in the organizations list of the user if it's not already there as admin
	if org.Creator != "" {
		if err := ms.addOrganizationToUser(ctx, org.Creator, org.Address, AdminRole); err != nil {
			// if an error occurs, delete the organization from the database
			if _, delErr := ms.organizations.DeleteOne(ctx, bson.M{"_id": org.Address}); delErr != nil {
				return errors.Join(err, delErr)
			}
			return err
		}
	}
	return nil
}

// DelOrganization method deletes the organization from the database. If an
// error occurs, it returns the error.
func (ms *MongoStorage) DelOrganization(org *Organization) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// delete the organization from the database
	_, err := ms.organizations.DeleteOne(ctx, bson.M{"_id": org.Address})
	return err
}

// ReplaceCreatorEmail method replaces the creator email in the organizations
// where it is the creator. If an error occurs, it returns the error.
func (ms *MongoStorage) ReplaceCreatorEmail(oldEmail, newEmail string) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// update the creator email in the organizations where it is the creator
	updateDoc := bson.M{"$set": bson.M{"creator": newEmail}}
	if _, err := ms.organizations.UpdateMany(ctx, bson.M{"creator": oldEmail}, updateDoc); err != nil {
		return err
	}
	return nil
}

// OrganizationsMembers method returns the users that are members of the
// organization with the given address. If an error occurs, it returns the
// error.
func (ms *MongoStorage) OrganizationsMembers(address string) ([]User, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// find the organization in the database
	filter := bson.M{
		"organizations": bson.M{
			"$elemMatch": bson.M{
				"_id": address,
			},
		},
	}
	users := []User{}
	cursor, err := ms.users.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	if err := cursor.All(ctx, &users); err != nil {
		return nil, err
	}
	return users, nil
}

// addSubscriptionToOrganization internal method adds the subscription to the organiation with
// the given email. If an error occurs, it returns the error. This method must
// be called with the keysLock held.
func (ms *MongoStorage) AddSubscriptionToOrganization(address string, orgSubscription *OrganizationSubscription) error {
	if _, err := ms.Subscription(orgSubscription.SubscriptionID); err != nil {
		return ErrInvalidData
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// prepare the document to be updated in the database
	filter := bson.M{"_id": address}
	updateDoc := bson.M{"$set": bson.M{"subscription": orgSubscription}}
	// update the organization in the database
	if _, err := ms.organizations.UpdateOne(ctx, filter, updateDoc); err != nil {
		return err
	}
	return nil
}
