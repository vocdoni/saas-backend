package db

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	root "github.com/vocdoni/saas-backend"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// initCollections creates the collections in the MongoDB database if they
// don't exist. It also includes the registered validations for every collection
// and creates the indexes for the collections.
func (ms *MongoStorage) initCollections(database string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// get the current collections names to create only the missing ones
	currentCollections, err := ms.collectionNames(ctx, database)
	if err != nil {
		return err
	}
	log.Infow("current collections", "collections", currentCollections)
	// aux method to get a collection if it exists, or create it if it doesn't
	getCollection := func(name string) (*mongo.Collection, error) {
		alreadyCreated := false
		for _, c := range currentCollections {
			if c == name {
				alreadyCreated = true
				break
			}
		}
		// if the collection doesn't exist, create it
		if alreadyCreated {
			if validator, ok := collectionsValidators[name]; ok {
				err := ms.client.Database(database).RunCommand(ctx, bson.D{
					{Key: "collMod", Value: name},
					{Key: "validator", Value: validator},
				}).Err()
				if err != nil {
					return nil, fmt.Errorf("failed to update collection validator: %w", err)
				}
			}
			if name == "plans" {
				// clear subscriptions collection and update the DB with the new ones
				if _, err := ms.client.Database(database).Collection(name).DeleteMany(ctx, bson.D{}); err != nil {
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
			if err := ms.client.Database(database).CreateCollection(ctx, name, opts); err != nil {
				return nil, err
			}
		}
		if name == "plans" {
			var plans []interface{}
			for _, plan := range ms.stripePlans {
				plans = append(plans, plan)
			}
			count, err := ms.client.Database(database).Collection(name).InsertMany(ctx, plans)
			if err != nil || len(count.InsertedIDs) != len(ms.stripePlans) {
				return nil, fmt.Errorf("failed to insert plans: %w", err)
			}
		}
		// return the collection
		return ms.client.Database(database).Collection(name), nil
	}
	// users collection
	if ms.users, err = getCollection("users"); err != nil {
		return err
	}
	// verifications collection
	if ms.verifications, err = getCollection("verifications"); err != nil {
		return err
	}
	// organizations collection
	if ms.organizations, err = getCollection("organizations"); err != nil {
		return err
	}
	// organizationInvites collection
	if ms.organizationInvites, err = getCollection("organizationInvites"); err != nil {
		return err
	}
	// subscriptions collection
	if ms.plans, err = getCollection("plans"); err != nil {
		return err
	}
	return nil
}

// collectionNames returns the names of the collections in the given database.
// It uses the ListCollections method of the MongoDB client to get the
// collections info and decode the names from the result.
func (ms *MongoStorage) collectionNames(ctx context.Context, database string) ([]string, error) {
	collectionsCursor, err := ms.client.Database(database).ListCollections(ctx, bson.D{})
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
	userEmailIndex := mongo.IndexModel{
		Keys:    bson.D{{Key: "email", Value: 1}}, // 1 for ascending order
		Options: options.Index().SetUnique(true),
	}
	if _, err := ms.users.Indexes().CreateOne(ctx, userEmailIndex); err != nil {
		return fmt.Errorf("failed to create index on email for users: %w", err)
	}
	// create an index for the ('code', 'type') tuple on user verifications (must be unique)
	verificationCodeIndex := mongo.IndexModel{
		Keys: bson.D{
			{Key: "code", Value: 1}, // 1 for ascending order
			{Key: "type", Value: 1}, // 1 for ascending order
		},
		Options: options.Index().SetUnique(true),
	}
	if _, err := ms.verifications.Indexes().CreateOne(ctx, verificationCodeIndex); err != nil {
		return fmt.Errorf("failed to create index on code for verifications: %w", err)
	}
	// create an index for the 'invitationCode' field on organization invites (must be unique)
	organizationInviteIndex := mongo.IndexModel{
		Keys:    bson.D{{Key: "invitationCode", Value: 1}}, // 1 for ascending order
		Options: options.Index().SetUnique(true),
	}
	// create a ttl index for the 'expiration' field on organization invites
	organizationInviteExpirationIndex := mongo.IndexModel{
		Keys:    bson.D{{Key: "expiration", Value: 1}}, // 1 for ascending order
		Options: options.Index().SetExpireAfterSeconds(0),
	}
	// create an index to ensure that the tuple ('organizationAddress', 'newUserEmail') is unique
	organizationInviteUniqueIndex := mongo.IndexModel{
		Keys: bson.D{
			{Key: "organizationAddress", Value: 1}, // 1 for ascending order
			{Key: "newUserEmail", Value: 1},        // 1 for ascending order
		},
		Options: options.Index().SetUnique(true),
	}
	if _, err := ms.organizationInvites.Indexes().CreateMany(ctx, []mongo.IndexModel{
		organizationInviteIndex,
		organizationInviteExpirationIndex,
		organizationInviteUniqueIndex,
	}); err != nil {
		return fmt.Errorf("failed to create index on invitationCode for organization invites: %w", err)
	}
	return nil
}

// dynamicUpdateDocument creates a BSON update document from a struct, including only non-zero fields.
// It uses reflection to iterate over the struct fields and create the update document.
// The struct fields must have a bson tag to be included in the update document.
// The _id field is skipped.
func dynamicUpdateDocument(item interface{}, alwaysUpdateTags []string) (bson.M, error) {
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

// readPlanJSON reads a JSON file with an array of subscritpions
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
