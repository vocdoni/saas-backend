package db

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func (ms *MongoStorage) initCollections(database string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	currentCollections, err := ms.client.Database(database).ListCollectionNames(ctx, nil)
	if err != nil {
		return err
	}
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
		if !alreadyCreated {
			// if the collection has a validator create it with it
			opts := options.CreateCollection()
			if validator, ok := collectionsValidators[name]; ok {
				opts.SetValidator(validator)
			}
			// create the collection
			if err := ms.client.Database(database).CreateCollection(ctx, "users", opts); err != nil {
				return nil, err
			}
		}
		// return the collection
		return ms.client.Database(database).Collection(name), nil
	}
	// users collection
	if ms.users, err = getCollection("users"); err != nil {
		return err
	}
	// organizations collection
	if ms.organizations, err = getCollection("organizations"); err != nil {
		return nil
	}
	return nil
}

func (ms *MongoStorage) createIndexes() error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Create an index for the 'email' field on users
	userEmailIndex := mongo.IndexModel{
		Keys:    bson.D{{Key: "email", Value: 1}}, // 1 for ascending order
		Options: options.Index().SetUnique(true),
	}
	if _, err := ms.users.Indexes().CreateOne(ctx, userEmailIndex); err != nil {
		return fmt.Errorf("failed to create index on addresses for users: %w", err)
	}

	// Create an index for the 'name' field on organizations (must be unique)
	organizationNameIndex := mongo.IndexModel{
		Keys:    bson.D{{Key: "name", Value: 1}}, // 1 for ascending order
		Options: options.Index().SetUnique(true),
	}
	if _, err := ms.organizations.Indexes().CreateOne(ctx, organizationNameIndex); err != nil {
		return fmt.Errorf("failed to create index on name for organizations: %w", err)
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

	// Create a map for quick lookup
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

		// Check if the field should always be updated or is not the zero value
		_, alwaysUpdate := alwaysUpdateMap[tag]
		if alwaysUpdate || !reflect.DeepEqual(field.Interface(), reflect.Zero(field.Type()).Interface()) {
			update[tag] = field.Interface()
		}
	}

	return bson.M{"$set": update}, nil
}
