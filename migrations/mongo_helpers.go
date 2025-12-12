package migrations

import (
	"context"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.vocdoni.io/dvote/log"
)

// listCollectionsInDB returns the names of the collections in the given database.
// It uses the ListCollections method of the MongoDB client to get the
// collections info and decode the names from the result.
func listCollectionsInDB(ctx context.Context, database *mongo.Database) ([]string, error) {
	collectionsCursor, err := database.ListCollections(ctx, bson.D{})
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

func renameFieldAndReindex(
	ctx context.Context,
	collection *mongo.Collection,
	oldField, newField string,
	oldIndexes []string,
	newIndexes []mongo.IndexModel,
) error {
	updateFunc := func() error {
		// 2) rename docs (only where old exists and new doesn't)
		filter := bson.M{
			oldField: bson.M{"$exists": true},
			newField: bson.M{"$exists": false},
		}
		update := bson.M{"$rename": bson.M{oldField: newField}}
		if _, err := collection.UpdateMany(ctx, filter, update); err != nil {
			return fmt.Errorf("failed to rename %s -> %s in %s: %w",
				oldField, newField, collection.Name(), err)
		}
		return nil
	}
	return replaceIndexWithUpdateFunc(ctx, collection, oldIndexes, newIndexes, updateFunc)
}

func replaceIndex(
	ctx context.Context,
	collection *mongo.Collection,
	oldIndexes []string,
	newIndexes []mongo.IndexModel,
) error {
	return replaceIndexWithUpdateFunc(ctx, collection, oldIndexes, newIndexes, nil)
}

func replaceIndexWithUpdateFunc(
	ctx context.Context,
	collection *mongo.Collection,
	oldIndexes []string,
	newIndexes []mongo.IndexModel,
	updateFunc func() error,
) error {
	// 1) drop old indexes
	for _, name := range oldIndexes {
		if _, err := collection.Indexes().DropOne(ctx, name); err != nil {
			if strings.Contains(err.Error(), "IndexNotFound") {
				continue
			}
			return fmt.Errorf("failed to drop index %s for collection %s: %w",
				name, collection.Name(), err)
		}
	}

	if updateFunc != nil {
		if err := updateFunc(); err != nil {
			return err
		}
	}

	// 3) create new indexes
	for _, index := range newIndexes {
		if _, err := collection.Indexes().CreateOne(ctx, index); err != nil {
			return fmt.Errorf("failed to create index %v on %s: %w",
				index.Keys, collection.Name(), err)
		}
	}

	return nil
}
