package db

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/log"
)

// collectionsMap returns a map of collection names to their corresponding field pointers
// in the MongoStorage struct. This is used by both init and Reset methods.
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
		"migrations":          &ms.migrations,
	}
}

func (ms *MongoStorage) init() error {
	// Initialize collection pointers
	for name, collectionPtr := range ms.collectionsMap() {
		*collectionPtr = ms.DBClient.Database(ms.database).Collection(name)
	}

	// run db migrations
	return ms.RunMigrationsUp()
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
		tagName := strings.Split(tag, ",")[0]
		if tagName == "" || tagName == "-" || tagName == "_id" {
			continue
		}
		// check if the field should always be updated or is not the zero value
		_, alwaysUpdate := alwaysUpdateMap[tagName]
		if alwaysUpdate || !reflect.DeepEqual(field.Interface(), reflect.Zero(field.Type()).Interface()) {
			update[tagName] = field.Interface()
		}
	}
	return bson.M{"$set": update}, nil
}

// paginatedDocuments returns totalCount of collection.CountDocuments()
// and a slice of collection.Find(), filtered and paginated.
func paginatedDocuments[T any](collection *mongo.Collection, page, limit int64,
	filter any, findOptions *options.FindOptions,
) (int64, []T, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Count total documents
	totalCount, err := collection.CountDocuments(ctx, filter)
	if err != nil {
		return 0, nil, err
	}

	// Calculate skip value based on page and limit
	skip := (page - 1) * limit
	// Set up options for pagination
	opts := findOptions.SetSkip(skip).SetLimit(limit)

	// Execute the find operation with pagination
	cursor, err := collection.Find(ctx, filter, opts)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to get documents: %w", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	// Decode results
	var list []T
	if err = cursor.All(ctx, &list); err != nil {
		return 0, nil, fmt.Errorf("failed to decode documents: %w", err)
	}

	return totalCount, list, nil
}

func ParseVochainOrganizationMeta(meta *api.AccountMetadata) (name string, logo string) {
	if meta == nil {
		return "", ""
	}
	// get the organization name
	if meta.Name != nil {
		if n, ok := meta.Name["default"]; ok {
			name = n
		}
	}
	// get the organization logo
	if meta.Media != nil {
		logo = meta.Media.Avatar
	}
	return name, logo
}
