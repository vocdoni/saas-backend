package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"reflect"
	"sync"
	"syscall"
	"time"

	"github.com/vocdoni/saas-backend/internal"
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
	// ErrUserNotFound is returned if the userID is not found in the database.
	ErrUserNotFound = fmt.Errorf("user is not found")
	// ErrUpdateUser is returned if the user data cannot be updated.
	ErrUpdateUser = fmt.Errorf("cannot update user data")
	// ErrDecodeUser is returned if the user data cannot be decoded when it is
	// retrieved from the database.
	ErrDecodeUser = fmt.Errorf("cannot decode user data")
	// ErrTokenNotFound is returned if the token is not found in the database.
	ErrTokenNotFound = fmt.Errorf("token not found")
	// ErrDecodeToken is returned if the token data cannot be decoded when it
	// is retrieved from the database.
	ErrDecodeToken = fmt.Errorf("cannot decode token data")
	// ErrPrepareUpdate is returned if the update document cannot be created.
	// It is a previous step before setting or updating the data.
	ErrPrepareDocument = fmt.Errorf("cannot create update document")
	// ErrIndexToken is returned if the token cannot be indexed. A token index
	// is used to keep track of the user ID associated with a token.
	ErrIndexToken = fmt.Errorf("cannot index token")
	// ErrBulkInsert is returned if the bulk insert operation fails.
	ErrBulkInsert = fmt.Errorf("cannot bulk insert users")
	// ErrProcessNotFound is returned if the process is not found in the user
	// data.
	ErrProcessNotFound = fmt.Errorf("process not found")
	// ErrBundleNotFound is returned if the bundle is not found in the user
	// data.
	ErrBundleNotFound = fmt.Errorf("bundle not found")
)

type MongoConfig struct {
	Client *mongo.Client
	DBName string
}

// MongoStorage uses an external MongoDB service for stoting the user data of the smshandler.
type MongoStorage struct {
	conf *MongoConfig

	users      *mongo.Collection
	tokenIndex *mongo.Collection
	keysLock   sync.RWMutex
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
	ms.users = conf.Client.Database(conf.DBName).Collection("users")
	ms.tokenIndex = conf.Client.Database(conf.DBName).Collection("tokenindex")
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
	if err := ms.users.Drop(ctx); err != nil {
		return err
	}
	if err := ms.tokenIndex.Drop(ctx); err != nil {
		return err
	}
	if err := ms.createIndexes(); err != nil {
		return err
	}
	return nil
}

// User returns the full information of a user, including the election list.
// It returns an error if the user is not found in the database.
func (ms *MongoStorage) User(userID internal.HexBytes) (*UserData, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	return ms.userByID(userID)
}

// SetUser adds a new user to the storage or updates an existing one. It uses
// the user ID as the primary key.
func (ms *MongoStorage) SetUser(user UserData) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	if err := ms.setUser(user); err != nil {
		return err
	}
	return nil
}

// SetUserBundle sets the list of processes for a process bundle for a user.
// It will create the user if it does not exist. The attempts parameter is the
// number of attempts allowed for each process.
func (ms *MongoStorage) SetUserBundle(userID, bundleID internal.HexBytes, pIDs ...internal.HexBytes) error {
	// if there are no processes, do nothing
	if len(pIDs) == 0 {
		return nil
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// get user data from the storage
	user, err := ms.userByID(userID)
	if err != nil {
		return err
	}
	// if the user has no bundles, create the map
	if user.Bundles == nil {
		user.Bundles = make(map[string]BundleData, 1)
	}
	// initialize the bundle in the user data if it does not exist
	if bundle, ok := user.Bundles[bundleID.String()]; !ok {
		user.Bundles[bundleID.String()] = BundleData{
			ID:   bundleID,
			PIDs: pIDs,
		}
	} else {
		// update the processes in the bundle
		bundle.PIDs = append(bundle.PIDs, pIDs...)
		user.Bundles[bundleID.String()] = bundle
	}
	// set the user data back to the storage
	if err := ms.setUser(*user); err != nil {
		return err
	}
	return nil
}

// AddUsers adds multiple users to the storage in batches of 1000 entries.
func (ms *MongoStorage) AddUsers(users []UserData) error {
	// if there are no users, do nothing
	if len(users) == 0 {
		return nil
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// Process users in batches of 1000
	batchSize := 1000
	for i := 0; i < len(users); i += batchSize {
		// Calculate end index for current batch
		end := min(i+batchSize, len(users))
		// Create documents for this batch
		batchDocuments := make([]any, end-i)
		for j, user := range users[i:end] {
			batchDocuments[j] = user
		}
		// Insert this batch
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := ms.users.InsertMany(ctx, batchDocuments)
		cancel()
		if err != nil {
			return errors.Join(ErrBulkInsert, err)
		}
	}
	return nil
}

// IndexToken indexes a token with its associated user ID. This index is used
// to quickly find the user data by token. It will create the index if it does
// not exist or update it if it does.
func (ms *MongoStorage) IndexAuthToken(uID, bID, token internal.HexBytes) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// check if the user already exists and has a bundle with the id provided
	user, err := ms.userByID(uID)
	if err != nil {
		return err
	}
	if _, ok := user.Bundles[bID.String()]; !ok {
		return ErrBundleNotFound
	}
	// create the context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// insert the token in the token index
	if _, err := ms.tokenIndex.InsertOne(ctx, AuthToken{
		Token:     token,
		UserID:    uID,
		BundleID:  bID,
		CreatedAt: time.Now(),
	}); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil
		}
		return errors.Join(ErrIndexToken, err)
	}
	return nil
}

// UserByToken returns the full information of a user, including the election
// list, by using the token index. It returns an error if the user is not found
// in the database.
func (ms *MongoStorage) UserAuthToken(token internal.HexBytes) (*AuthToken, *UserData, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// get the auth token from the token index
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := ms.tokenIndex.FindOne(ctx, bson.M{"_id": token})
	// decode the auth token
	authToken := new(AuthToken)
	if err := result.Decode(authToken); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil, ErrTokenNotFound
		}
		return nil, nil, errors.Join(ErrDecodeToken, err)
	}
	// get the user data from the user ID
	user, err := ms.userByID(authToken.UserID)
	if err != nil {
		return nil, nil, err
	}
	return authToken, user, nil
}

func (ms *MongoStorage) VerifyAuthToken(token internal.HexBytes) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create the context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// check if the token exists
	filter := bson.M{"_id": token}
	count, err := ms.tokenIndex.CountDocuments(ctx, filter)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return ErrTokenNotFound
		}
		return errors.Join(ErrDecodeToken, err)
	}
	if count == 0 {
		return ErrTokenNotFound
	}
	// update the token as verified
	update := bson.M{"$set": bson.M{"verified": true}}
	if _, err := ms.tokenIndex.UpdateOne(ctx, filter, update); err != nil {
		return errors.Join(ErrIndexToken, err)
	}
	return nil
}

// createIndexes creates the necessary indexes in the MongoDB database.
func (ms *MongoStorage) createIndexes() error {
	// Create text index on `extraData` for finding user data
	ctx, cancel3 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel3()
	userExtraDataIdx := mongo.IndexModel{
		Keys: bson.D{
			{Key: "extradata", Value: "text"},
		},
	}
	_, err := ms.users.Indexes().CreateOne(ctx, userExtraDataIdx)
	if err != nil {
		return err
	}
	tokenBundleUserIdx := mongo.IndexModel{
		Keys: bson.D{
			{Key: "userid", Value: 1},
			{Key: "bundleid", Value: 1},
			{Key: "authtoken", Value: 1},
		},
		Options: options.Index().SetUnique(true),
	}
	_, err = ms.tokenIndex.Indexes().CreateOne(ctx, tokenBundleUserIdx)
	if err != nil {
		return err
	}
	return nil
}

// userByID retrieves the user data from the database by user ID. It does not
// lock the keysLock, so it should be called from a function that already has
// the lock.
func (ms *MongoStorage) userByID(userID internal.HexBytes) (*UserData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := ms.users.FindOne(ctx, bson.M{"_id": userID})
	var user UserData
	if err := result.Decode(&user); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, errors.Join(ErrUserNotFound, err)
		}
		return nil, errors.Join(ErrDecodeUser, err)
	}
	return &user, nil
}

// setUser updates the user data in the database. It does not lock the keysLock,
// so it should be called from a function that already has the lock.
func (ms *MongoStorage) setUser(user UserData) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	filter := bson.M{"_id": user.ID}
	// update := bson.M{"$set": user}
	opts := options.Update().SetUpsert(true)
	// generate update document with $set and $unset handling
	update, err := dynamicUpdateDocument(user, nil)
	if err != nil {
		return errors.Join(ErrPrepareDocument, err)
	}
	if _, err := ms.users.UpdateOne(ctx, filter, update, opts); err != nil {
		return errors.Join(ErrUpdateUser, err)
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
