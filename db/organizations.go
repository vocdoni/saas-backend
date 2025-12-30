package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.vocdoni.io/dvote/log"
)

func (ms *MongoStorage) fetchOrganizationFromDB(ctx context.Context, address common.Address) (*Organization, error) {
	// find the organization in the database by its address
	filter := bson.M{"_id": address}
	result := ms.organizations.FindOne(ctx, filter)
	org := &Organization{}
	if err := result.Decode(org); err != nil {
		// if the organization doesn't exist return a specific error
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	// initialize Meta map if it's nil to prevent nil map assignment errors
	if org.Meta == nil {
		org.Meta = make(map[string]any)
	}
	return org, nil
}

// Organization method returns the organization with the given address.
// If the organization doesn't exist, it returns the specific error.
// If other errors occur, it returns the error.
func (ms *MongoStorage) Organization(address common.Address) (*Organization, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// find the organization in the database
	org, err := ms.fetchOrganizationFromDB(ctx, address)
	if err != nil {
		return nil, err
	}
	return org, nil
}

// OrganizationWithParent method returns the organization with the given address
// and its parent organization if it exists. If the organization doesn't exist
// or the parent organization doesn't exist, it returns the specific error.
// If other errors occur, it returns the error.
func (ms *MongoStorage) OrganizationWithParent(address common.Address) (org *Organization, parent *Organization, err error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// find the organization in the database
	org, err = ms.fetchOrganizationFromDB(ctx, address)
	if err != nil {
		return nil, nil, err
	}
	if org.Parent.Cmp(common.Address{}) == 0 {
		return org, nil, nil
	}
	// find the parent organization in the database
	parent, err = ms.fetchOrganizationFromDB(ctx, org.Parent)
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
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
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
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
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
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// update the creator email in the organizations where it is the creator
	updateDoc := bson.M{"$set": bson.M{"creator": newEmail}}
	if _, err := ms.organizations.UpdateMany(ctx, bson.M{"creator": oldEmail}, updateDoc); err != nil {
		return err
	}
	return nil
}

// OrganizationUsers method returns the users that have a role in the
// organization with the given address. If an error occurs, it returns the
// error.
func (ms *MongoStorage) OrganizationUsers(address common.Address) ([]User, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
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
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()
	if err := cursor.All(ctx, &users); err != nil {
		return nil, err
	}
	return users, nil
}

// UpdateOrganizationUserRole method updates the role of the user in the
// organization with the given address.
func (ms *MongoStorage) UpdateOrganizationUserRole(address common.Address, userID uint64, newRole UserRole) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	filter := bson.M{
		"_id":               userID,
		"organizations._id": address,
	}

	update := bson.M{
		"$set": bson.M{
			"organizations.$.role": newRole,
		},
	}

	_, err := ms.users.UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}
	return nil
}

// RemoveOrganizationUser method removes the user from the organization
// with the given address.
func (ms *MongoStorage) RemoveOrganizationUser(address common.Address, userID uint64) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{
		"_id":               userID,
		"organizations._id": address,
	}
	update := bson.M{
		"$pull": bson.M{
			"organizations": bson.M{
				"_id": address,
			},
		},
	}
	_, err := ms.users.UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}
	return nil
}

// SetOrganizationSubscription method adds the provided subscription to
// the organization with the given address
func (ms *MongoStorage) SetOrganizationSubscription(address common.Address, orgSubscription *OrganizationSubscription) error {
	if _, err := ms.Plan(orgSubscription.PlanID); err != nil {
		return ErrInvalidData
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
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

// SetOrganizationMeta method sets the metadata for the organization with the
// given address. If the organization doesn't exist, it returns an error.
func (ms *MongoStorage) AddOrganizationMeta(address common.Address, meta map[string]any) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// prepare the document to be updated in the database
	filter := bson.M{"_id": address}
	updateDoc := bson.M{"$set": bson.M{"meta": meta}}
	// update the organization in the database
	if _, err := ms.organizations.UpdateOne(ctx, filter, updateDoc); err != nil {
		return err
	}
	return nil
}

// UpdateOrganizationMeta updates individual keys in the 'meta' subdocument
// Has only one layer o depth, if a second layer document is provided, for example meta.doc = [a,b,c] it will
// all the docment will be updated
func (ms *MongoStorage) UpdateOrganizationMeta(address common.Address, metaUpdates map[string]any) error {
	updateFields := bson.M{}

	// Construct dot notation keys like "meta.region": "EU"
	for k, v := range metaUpdates {
		updateFields["meta."+k] = v
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	filter := bson.M{"_id": address}
	update := bson.M{"$set": updateFields}

	result, err := ms.organizations.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to update eta: %w", err)
	}

	if result.MatchedCount == 0 {
		return fmt.Errorf("no organization found with address: %s", address)
	}

	fmt.Printf("Matched: %d, Modified: %d\n", result.MatchedCount, result.ModifiedCount)
	return nil
}

// DeleteOrganizationMetaKeys deletes specific keys from the 'meta' object of a given Organization
func (ms *MongoStorage) DeleteOrganizationMetaKeys(address common.Address, keysToDelete []string) error {
	unsetFields := bson.M{}

	// Build the unset document: { "meta.key1": 1, "meta.key2": 1 }
	// In MongoDB, the value in $unset doesn't matter, but it's conventional to use 1
	for _, key := range keysToDelete {
		unsetFields["meta."+key] = 1
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	filter := bson.M{"_id": address}
	update := bson.M{"$unset": unsetFields}

	result, err := ms.organizations.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to unset meta fields: %w", err)
	}

	if result.MatchedCount == 0 {
		return fmt.Errorf("no organization found with address: %s", address)
	}

	return nil
}

// IncrementOrganizationUsersCounter atomically increments the users counter for the organization with the given address.
func (ms *MongoStorage) IncrementOrganizationUsersCounter(address common.Address) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	org, err := ms.Organization(address)
	if err != nil {
		return fmt.Errorf("could not get organization: %w", err)
	}

	// Check if the organization has a subscription
	if org.Subscription.PlanID == 0 {
		return fmt.Errorf("organization has no subscription plan")
	}

	plan, err := ms.Plan(org.Subscription.PlanID)
	if err != nil {
		return fmt.Errorf("could not get organization plan: %w", err)
	}

	if org.Counters.Users >= plan.Organization.Users {
		return fmt.Errorf("max users reached (%d >= %d)", org.Counters.Users, plan.Organization.Users)
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Prepare the filter to find the organization
	filter := bson.M{"_id": address}

	// Use the $inc operator to atomically increment the users counter
	update := bson.M{"$inc": bson.M{"counters.users": 1}}

	// Update the organization in the database
	result, err := ms.organizations.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to increment users counter: %w", err)
	}

	if result.MatchedCount == 0 {
		return fmt.Errorf("no organization found with address: %s", address)
	}

	return nil
}

// DecrementOrganizationUsersCounter atomically decrements the users counter for the organization with the given address.
func (ms *MongoStorage) DecrementOrganizationUsersCounter(address common.Address) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Prepare the filter to find the organization
	filter := bson.M{"_id": address}

	// Use the $inc operator with a negative value to atomically decrement the users counter
	update := bson.M{"$inc": bson.M{"counters.users": -1}}

	// Update the organization in the database
	result, err := ms.organizations.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to decrement users counter: %w", err)
	}

	if result.MatchedCount == 0 {
		return fmt.Errorf("no organization found with address: %s", address)
	}

	return nil
}

// IncrementOrganizationSubOrgsCounter atomically increments the suborgs counter for the organization with the given address.
func (ms *MongoStorage) IncrementOrganizationSubOrgsCounter(address common.Address) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	org, err := ms.Organization(address)
	if err != nil {
		return fmt.Errorf("could not get organization: %v", err)
	}

	// Check if the organization has a subscription
	if org.Subscription.PlanID == 0 {
		return fmt.Errorf("organization has no subscription plan")
	}

	plan, err := ms.Plan(org.Subscription.PlanID)
	if err != nil {
		return fmt.Errorf("could not get organization plan: %v", err)
	}

	if org.Counters.SubOrgs >= plan.Organization.SubOrgs {
		return fmt.Errorf("max suborgs reached (%d >= %d)", org.Counters.SubOrgs, plan.Organization.SubOrgs)
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Prepare the filter to find the organization
	filter := bson.M{"_id": address}

	// Use the $inc operator to atomically increment the suborgs counter
	update := bson.M{"$inc": bson.M{"counters.subOrgs": 1}}

	// Update the organization in the database
	result, err := ms.organizations.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to increment suborgs counter: %w", err)
	}

	if result.MatchedCount == 0 {
		return fmt.Errorf("no organization found with address: %s", address)
	}

	return nil
}

// DecrementOrganizationSubOrgsCounter atomically decrements the suborgs counter for the organization with the given address.
func (ms *MongoStorage) DecrementOrganizationSubOrgsCounter(address common.Address) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Prepare the filter to find the organization
	filter := bson.M{"_id": address}

	// Use the $inc operator with a negative value to atomically decrement the suborgs counter
	update := bson.M{"$inc": bson.M{"counters.subOrgs": -1}}

	// Update the organization in the database
	result, err := ms.organizations.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to decrement suborgs counter: %w", err)
	}

	if result.MatchedCount == 0 {
		return fmt.Errorf("no organization found with address: %s", address)
	}

	return nil
}
