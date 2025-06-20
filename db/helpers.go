package db

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"time"

	root "github.com/vocdoni/saas-backend"
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

// createIndexes creates the indexes for the collections in the MongoDB
// database. Add more indexes here as needed.
func (ms *MongoStorage) createIndexes() error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// create an index for the 'email' field on users
	if _, err := ms.users.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "email", Value: 1}}, // 1 for ascending order
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return fmt.Errorf("failed to create index on email for users: %w", err)
	}

	// create an index for the ('code', 'type') tuple on user verifications (must be unique)
	if _, err := ms.verifications.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "code", Value: 1}, // 1 for ascending order
			{Key: "type", Value: 1}, // 1 for ascending order
		},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return fmt.Errorf("failed to create index on code and type for verifications: %w", err)
	}

	if _, err := ms.organizationInvites.Indexes().CreateMany(ctx, []mongo.IndexModel{
		// create an index for the 'invitationCode' field on organization invites (must be unique)
		{
			Keys:    bson.D{{Key: "invitationCode", Value: 1}}, // 1 for ascending order
			Options: options.Index().SetUnique(true),
		},
		// create a ttl index for the 'expiration' field on organization invites
		{
			Keys:    bson.D{{Key: "expiration", Value: 1}}, // 1 for ascending order
			Options: options.Index().SetExpireAfterSeconds(0),
		},
		// create an index to ensure that the tuple ('organizationAddress', 'newUserEmail') is unique
		{
			Keys: bson.D{
				{Key: "organizationAddress", Value: 1}, // 1 for ascending order
				{Key: "newUserEmail", Value: 1},        // 1 for ascending order
			},
			Options: options.Index().SetUnique(true),
		},
	}); err != nil {
		return fmt.Errorf("failed to create many indexes for organization invites: %w", err)
	}

	// create an index for the id field on organization members
	if _, err := ms.orgMembers.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "_id", Value: 1}, // 1 for ascending order
		},
	}); err != nil {
		return fmt.Errorf("failed to create index on _id for orgMembers: %w", err)
	}

	// create an index for the orgAddress/id field on organization members
	if _, err := ms.orgMembers.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "orgAddress", Value: 1}, // 1 for ascending order
			{Key: "_id", Value: 1},        // 1 for ascending order
		},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return fmt.Errorf("failed to create index on orgAddress and id for orgMembers: %w", err)
	}

	// create an index for the orgAddress
	if _, err := ms.orgMembers.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "orgAddress", Value: 1}, // 1 for ascending order
		},
	}); err != nil {
		return fmt.Errorf("failed to create index on orgAddress for orgMembers: %w", err)
	}

	// create an index for the tuple orgAddress and memberNumber
	if _, err := ms.orgMembers.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "orgAddress", Value: 1},   // 1 for ascending order
			{Key: "memberNumber", Value: 1}, // 1 for ascending order
		},
	}); err != nil {
		return fmt.Errorf("failed to create index on orgAddress and memberNumber for orgMembers: %w", err)
	}

	// create an index for the tuple orgAddress and email on organization members
	if _, err := ms.orgMembers.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "orgAddress", Value: 1}, // 1 for ascending order
			{Key: "email", Value: 1},      // 1 for ascending order
		},
	}); err != nil {
		return fmt.Errorf("failed to create index on orgAddress and email for orgMembers: %w", err)
	}

	// create an index for the tuple orgAddress and hashedPhone on organization members
	if _, err := ms.orgMembers.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "orgAddress", Value: 1},  // 1 for ascending order
			{Key: "hashedPhone", Value: 1}, // 1 for ascending order
		},
	}); err != nil {
		return fmt.Errorf("failed to create index on orgAddress and hashedPhone for orgMembers: %w", err)
	}

	// index for the censusId
	if _, err := ms.censusParticipants.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "censusId", Value: 1}, // 1 for ascending order
		},
	}); err != nil {
		return fmt.Errorf("failed to create index on censusId for censusParticipants: %w", err)
	}

	// index for the participantID
	if _, err := ms.censusParticipants.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "participantID", Value: 1}, // 1 for ascending order
		},
	}); err != nil {
		return fmt.Errorf("failed to create index on participantID for censusParticipants: %w", err)
	}

	// index for the censusId and participantID tuple
	if _, err := ms.censusParticipants.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "censusId", Value: 1},      // 1 for ascending order
			{Key: "participantID", Value: 1}, // 1 for ascending order
		},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return fmt.Errorf("failed to create index on censusId and participantID for censusParticipants: %w", err)
	}

	// unique index over userID and processID
	if _, err := ms.cspTokensStatus.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "userid", Value: 1},
			{Key: "processid", Value: 1},
		},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return fmt.Errorf("failed to create index on userid and processid for cspTokensStatus: %w", err)
	}

	// member properties text index for filtering
	if _, err := ms.orgMembers.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "email", Value: "text"},
			{Key: "memberNumber", Value: "text"},
			{Key: "nationalID", Value: "text"},
			{Key: "name", Value: "text"},
			{Key: "surname", Value: "text"},
			{Key: "birthDate", Value: "text"},
		},
	}); err != nil {
		return fmt.Errorf("failed to create text index on orgMembers: %w", err)
	}

	return nil
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

// ReadPlanJSON reads a JSON file with an array of subscriptions
// and return it as a Plan array
func ReadPlanJSON() ([]*Plan, error) {
	file, err := root.Assets.Open("assets/plans.json")
	if err != nil {
		return nil, err
	}

	// Create a JSON decoder
	decoder := json.NewDecoder(file)

	var plans []*Plan
	err = decoder.Decode(&plans)
	if err != nil {
		return nil, err
	}
	return plans, nil
}
