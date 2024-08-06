package db

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Organization method returns the organization with the given address. If the
// organization doesn't exist, it returns a specific error. If other errors
// occur, it returns the error.
func (ms *MongoStorage) Organization(address string) (*Organization, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// find the organization in the database
	result := ms.organizations.FindOne(ctx, bson.M{"_id": address})
	org := &Organization{}
	if err := result.Decode(org); err != nil {
		// if the organization doesn't exist return a specific error
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return org, nil
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
	updateDoc, err := dynamicUpdateDocument(org, nil)
	if err != nil {
		return err
	}
	// set upsert to true to create the document if it doesn't exist
	opts := options.Update().SetUpsert(true)
	if _, err := ms.organizations.UpdateOne(ctx, bson.M{"_id": org.Address}, updateDoc, opts); err != nil {
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
