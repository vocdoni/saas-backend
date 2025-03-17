package storage

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"reflect"
	"sync"
	"syscall"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.vocdoni.io/dvote/log"
)

// DefaultDatabase is the default name of the MongoDB database used by the
// MongoDB storage.
const DefaultDatabase = "twofactor"

var (
	// ErrTokenNotFound is returned if the token is not found in the database.
	ErrTokenNotFound = fmt.Errorf("token not found")
	// ErrPrepareUpdate is returned if the update document cannot be created.
	// It is a previous step before setting or updating the data.
	ErrPrepareDocument = fmt.Errorf("cannot create update document")
	// ErrStoreToken is returned if the token cannot be created or updated.
	ErrStoreToken = fmt.Errorf("cannot set token")
	// ErrProcessNotFound is returned if the process is not found in the user
	// data.
	ErrProcessNotFound = fmt.Errorf("process not found")
	// ErrBadInputs is returned if the inputs provided to the function are
	// invalid.
	ErrBadInputs = fmt.Errorf("bad inputs")
	// ErrProcessAlreadyConsumed is returned if the process has already been
	// consumed by the user.
	ErrProcessAlreadyConsumed = fmt.Errorf("token already consumed")
	// ErrTokenNoVerified is returned if the token has not been verified.
	ErrTokenNoVerified = fmt.Errorf("token not verified")
)

type MongoConfig struct {
	Client *mongo.Client
	DBName string
}

// MongoStorage uses an external MongoDB service for stoting the user data of the smshandler.
type MongoStorage struct {
	conf     *MongoConfig
	keysLock sync.RWMutex

	// new collections for refactored CSP
	cspTokens       *mongo.Collection
	cspTokensStatus *mongo.Collection
}

// Init initializes the MongoDB storage with the provided configuration.
// The configuration must be a pointer to a MongoConfig struct, which must
// include a valid MongoDB client and the name of the database to use.
// If the database name is not provided, it will use the DefaultDatabase
// constant.
// The Init function will also create the necessary indexes in the database.
// If the CSP_RESET_DB environment variable is set, it will drop the database
// and recreate the indexes.
func (ms *MongoStorage) Init(rawConf any) error {
	conf, ok := rawConf.(*MongoConfig)
	if !ok {
		return fmt.Errorf("invalid configuration provided")
	}
	if conf.Client == nil {
		return fmt.Errorf("invalid mongo client provided")
	}
	if conf.DBName == "" {
		conf.DBName = DefaultDatabase
	}
	log.Infof("connecting to mongodb database: %s", conf.DBName)
	// shutdown database connection when SIGTERM received
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		<-c
		log.Warnf("received SIGTERM, disconnecting mongo database")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := conf.Client.Disconnect(ctx); err != nil {
			log.Warn(err)
		}
	}()
	// check the connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conf.Client.Ping(ctx, readpref.Primary()); err != nil {
		return fmt.Errorf("cannot connect to mongodb: %w", err)
	}
	// set the config and collections
	ms.conf = conf
	ms.cspTokens = conf.Client.Database(conf.DBName).Collection("cspTokens")
	ms.cspTokensStatus = conf.Client.Database(conf.DBName).Collection("cspTokensStatus")
	// if reset flag is enabled, drop the database documents and recreates
	// indexes, otherwise just create the indexes
	if reset := os.Getenv("CSP_RESET_DB"); reset != "" {
		if err := ms.Reset(); err != nil {
			return err
		}
	} else {
		if err := ms.createIndexes(); err != nil {
			return err
		}
	}
	return nil
}

// Reset clears the storage content by dropping the users collection and
// recreating the necessary indexes.
func (ms *MongoStorage) Reset() error {
	log.Infof("resetting database")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// new collections for refactored CSP
	if err := ms.cspTokens.Drop(ctx); err != nil {
		return err
	}
	if err := ms.cspTokensStatus.Drop(ctx); err != nil {
		return err
	}
	if err := ms.createIndexes(); err != nil {
		return err
	}
	return nil
}

// createIndexes creates the necessary indexes in the MongoDB database.
func (ms *MongoStorage) createIndexes() error {
	// Create text index on `extraData` for finding user data
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// new indexes for refactored CSP
	if _, err := ms.cspTokensStatus.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "userid", Value: 1},
			{Key: "processid", Value: 1},
		},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return err
	}
	return nil
}

// dynamicUpdateDocument creates a BSON update document from a struct,
// including only non-zero fields. It uses reflection to iterate over the
// struct fields and create the update document. The struct fields must have
// a bson tag to be included in the update document. The _id field is skipped.
func dynamicUpdateDocument(item any, alwaysUpdateTags []string) (bson.M, error) {
	val := reflect.ValueOf(item)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	if !val.IsValid() || val.Kind() != reflect.Struct {
		return nil, fmt.Errorf("input must be a valid struct")
	}
	updateSet := bson.M{}
	updateUnset := bson.M{}
	typ := val.Type()
	// Ensure quick lookup for always-updated fields
	alwaysUpdateMap := make(map[string]bool, len(alwaysUpdateTags))
	for _, tag := range alwaysUpdateTags {
		alwaysUpdateMap[tag] = true
	}
	var _id any
	for i := range val.NumField() {
		field := val.Field(i)
		if !field.CanInterface() {
			continue
		}
		fieldType := typ.Field(i)
		tag := fieldType.Tag.Get("bson")
		if tag == "" || tag == "-" {
			continue
		}
		if tag == "_id" {
			_id = field.Interface()
			continue
		}
		// Handle nil values using $unset
		if (field.Kind() == reflect.Ptr || field.Kind() == reflect.Slice || field.Kind() == reflect.Map) && field.IsNil() {
			updateUnset[tag] = 1 // Explicitly remove the field
		} else {
			updateSet[tag] = field.Interface()
		}
	}
	// Build the final update document
	update := bson.M{}
	// Always include at least an empty $set to ensure upsert triggers
	if len(updateSet) == 0 {
		updateSet["__forceUpsert"] = true // MongoDB ignores this key but processes the update
	}
	update["$set"] = updateSet
	// Apply $unset if necessary
	if len(updateUnset) > 0 {
		update["$unset"] = updateUnset
	}
	// Ensure _id is set on insert
	if _id != nil {
		update["$setOnInsert"] = bson.M{"_id": _id}
	}
	return update, nil
}
