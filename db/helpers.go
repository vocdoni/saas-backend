package db

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// collectionsMap returns a map of collection names to their corresponding field pointers
// in the MongoStorage struct. This is used by both initCollections and Reset methods.
func (ms *MongoStorage) collectionsMap() map[string]**mongo.Collection {
	return map[string]**mongo.Collection{
		"users":               &ms.users,
		"verifications":       &ms.verifications,
		"organizations":       &ms.organizations,
		"organizationInvites": &ms.organizationInvites,
		"plans":               &ms.plans,
		"objects":             &ms.objects,
		"census":              &ms.censuses,
		"orgMembers":          &ms.orgMembers,
		"orgMemberGroups":     &ms.orgMemberGroups,
		"censusParticipants":  &ms.censusParticipants,
		"publishedCensuses":   &ms.publishedCensuses,
		"processes":           &ms.processes,
		"processBundles":      &ms.processBundles,
		"cspTokens":           &ms.cspTokens,
		"cspTokensStatus":     &ms.cspTokensStatus,
		"jobs":                &ms.jobs,
	}
}

// initCollections creates the collections in the MongoDB database if they
// don't exist. It also includes the registered validations for every collection
// and creates the indexes for the collections.
func (ms *MongoStorage) initCollections(database string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// get the current collections names to create only the missing ones
	currentCollections, err := ms.listCollectionsInDB(ctx, database)
	if err != nil {
		return fmt.Errorf("failed to get current collections: %w", err)
	}
	log.Infow("current collections", "collections", currentCollections)
	// aux method to get a collection if it exists, or create it if it doesn't
	getCollection := func(name string) (*mongo.Collection, error) {
		alreadyCreated := slices.Contains(currentCollections, name)
		// if the collection doesn't exist, create it
		if alreadyCreated {
			if validator, ok := collectionsValidators[name]; ok {
				err := ms.DBClient.Database(database).RunCommand(ctx, bson.D{
					{Key: "collMod", Value: name},
					{Key: "validator", Value: validator},
				}).Err()
				if err != nil {
					return nil, fmt.Errorf("failed to update collection validator: %w", err)
				}
			}
			if name == "plans" {
				// clear subscriptions collection and update the DB with the new ones
				if _, err := ms.DBClient.Database(database).Collection(name).DeleteMany(ctx, bson.D{}); err != nil {
					return nil, err
				}
			}
		} else {
			// if the collection has a validator create it with it
			opts := options.CreateCollection()
			if validator, ok := collectionsValidators[name]; ok {
				opts = opts.SetValidator(validator).SetValidationLevel("strict").SetValidationAction("error")
			}
			// create the collection
			if err := ms.DBClient.Database(database).CreateCollection(ctx, name, opts); err != nil {
				return nil, err
			}
		}
		if name == "plans" && len(ms.stripePlans) > 0 {
			var plans []any
			for _, plan := range ms.stripePlans {
				plans = append(plans, plan)
			}
			count, err := ms.DBClient.Database(database).Collection(name).InsertMany(ctx, plans)
			if err != nil || len(count.InsertedIDs) != len(ms.stripePlans) {
				return nil, fmt.Errorf("failed to insert plans: %w", err)
			}
		}
		// return the collection
		return ms.DBClient.Database(database).Collection(name), nil
	}

	// Initialize all collections
	for name, collectionPtr := range ms.collectionsMap() {
		collection, err := getCollection(name)
		if err != nil {
			return err
		}
		*collectionPtr = collection
	}

	return nil
}

// listCollectionsInDB returns the names of the collections in the given database.
// It uses the ListCollections method of the MongoDB client to get the
// collections info and decode the names from the result.
func (ms *MongoStorage) listCollectionsInDB(ctx context.Context, database string) ([]string, error) {
	collectionsCursor, err := ms.DBClient.Database(database).ListCollections(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := collectionsCursor.Close(ctx); err != nil {
			log.Warnw("failed to close collections cursor", "error", err)
		}
	}()
	collections := []bson.D{}
	if err := collectionsCursor.All(ctx, &collections); err != nil {
		return nil, err
	}
	names := []string{}
	for _, col := range collections {
		for _, v := range col {
			if v.Key == "name" {
				names = append(names, v.Value.(string))
			}
		}
	}
	return names, nil
}

// dynamicUpdateDocument creates a BSON update document from a struct, including only non-zero fields.
// It uses reflection to iterate over the struct fields and create the update document.
// The struct fields must have a bson tag to be included in the update document.
// The _id field is skipped.
func dynamicUpdateDocument(item any, alwaysUpdateTags []string) (bson.M, error) {
	val := reflect.ValueOf(item)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	if !val.IsValid() || val.Kind() != reflect.Struct {
		return nil, fmt.Errorf("input must be a valid struct")
	}
	update := bson.M{}
	typ := val.Type()
	// create a map for quick lookup
	alwaysUpdateMap := make(map[string]bool, len(alwaysUpdateTags))
	for _, tag := range alwaysUpdateTags {
		alwaysUpdateMap[tag] = true
	}
	for i := 0; i < val.NumField(); i++ {
		field := val.Field(i)
		if !field.CanInterface() {
			continue
		}
		fieldType := typ.Field(i)
		tag := fieldType.Tag.Get("bson")
		if tag == "" || tag == "-" || tag == "_id" {
			continue
		}
		// check if the field should always be updated or is not the zero value
		_, alwaysUpdate := alwaysUpdateMap[tag]
		if alwaysUpdate || !reflect.DeepEqual(field.Interface(), reflect.Zero(field.Type()).Interface()) {
			update[tag] = field.Interface()
		}
	}
	return bson.M{"$set": update}, nil
}
