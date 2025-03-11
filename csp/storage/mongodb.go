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

	"github.com/google/uuid"
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
	// ErrPrepareUser is returned if the update document cannot be created. It
	// is a previous step before setting or updating the user data.
	ErrPrepareUser = fmt.Errorf("cannot create update document")
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

// UserByToken returns the full information of a user, including the election
// list, by using the token index. It returns an error if the user is not found
// in the database.
func (ms *MongoStorage) UserByToken(token *uuid.UUID) (*UserData, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// get the user ID from the token index
	var index AuthTokenIndex
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := ms.tokenIndex.FindOne(ctx, bson.M{"_id": token})
	if err := result.Decode(&index); err != nil {
		return nil, errors.Join(ErrDecodeUser, err)
	}
	// get the user data from the user ID
	user, err := ms.userByID(index.UserID)
	if err != nil {
		return nil, err
	}
	return user, nil
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

// SetUserProcesses sets the list of processes for a user. It will create the
// user if it does not exist. The attempts parameter is the number of attempts
// allowed for each process.
func (ms *MongoStorage) SetUserProcesses(userID internal.HexBytes, attempts int, pIDs ...internal.HexBytes) error {
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
	// if the user has no elections, create the map
	if user.Processes == nil {
		user.Processes = make(map[string]UserProcess, len(pIDs))
	}
	// add the elections to the user data
	for _, pid := range pIDs {
		user.Processes[pid.String()] = UserProcess{
			ID:                pid,
			RemainingAttempts: attempts,
		}
	}
	// set the user data back to the storage
	if err := ms.setUser(*user); err != nil {
		return err
	}
	return nil
}

// SetUserBundle sets the list of processes for a process bundle for a user.
// It will create the user if it does not exist. The attempts parameter is the
// number of attempts allowed for each process.
func (ms *MongoStorage) SetUserBundle(userID, bundleID internal.HexBytes, attempts int, pIDs ...internal.HexBytes) error {
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
		user.Bundles = make(map[string]UserBundle, 1)
	}
	// initialize the bundle in the user data if it does not exist
	if _, ok := user.Bundles[bundleID.String()]; !ok {
		user.Bundles[bundleID.String()] = UserBundle{
			ID:        bundleID,
			Processes: make(map[string]UserProcess, len(pIDs)),
		}
	}
	// include the elections in the bundle
	for _, pid := range pIDs {
		user.Bundles[bundleID.String()].Processes[pid.String()] = UserProcess{
			ID:                pid,
			RemainingAttempts: attempts,
		}
	}
	// set the user data back to the storage
	if err := ms.setUser(*user); err != nil {
		return err
	}
	return nil
}

// SetProcessAttempts sets the number of attempts for a process. It returns an
// error if the user does not exists, the process has not been registered for
// the user or something fails updating the data.
func (ms *MongoStorage) SetProcessAttempts(userID, processID internal.HexBytes,
	attempts int,
) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// get user data from the storage
	user, err := ms.userByID(userID)
	if err != nil {
		return err
	}
	// if the user has no elections, create the map
	if user.Processes == nil {
		return ErrProcessNotFound
	}
	// set the attempts for the process
	process, ok := user.Processes[processID.String()]
	if !ok {
		return ErrProcessNotFound
	}
	process.RemainingAttempts = attempts
	user.Processes[processID.String()] = process
	return ms.setUser(*user)
}

// SetBundleProcessAttempts sets the number of attempts for a process in a
// bundle. It returns an error if the user does not exists, the bundle has not
// been registered for the user, the process has not been registered in the
// bundle or something fails updating the data.
func (ms *MongoStorage) SetBundleProcessAttempts(userID, bundleID,
	processID internal.HexBytes, attempts int,
) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// get user data from the storage
	user, err := ms.userByID(userID)
	if err != nil {
		return err
	}
	// if the user has no bundles, create the map
	if user.Bundles == nil {
		return ErrBundleNotFound
	}
	// get the bundle from the user data
	bundle, ok := user.Bundles[bundleID.String()]
	if !ok {
		return ErrBundleNotFound
	}
	// set the attempts for the process
	process, ok := bundle.Processes[processID.String()]
	if !ok {
		return ErrProcessNotFound
	}
	process.RemainingAttempts = attempts
	bundle.Processes[processID.String()] = process
	user.Bundles[bundleID.String()] = bundle
	return ms.setUser(*user)
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
func (ms *MongoStorage) IndexToken(userID internal.HexBytes, token *uuid.UUID) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create the index document
	index := AuthTokenIndex{
		UserID:    userID,
		AuthToken: token,
	}
	// insert the index document
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opts := options.Update().SetUpsert(true)
	if _, err := ms.tokenIndex.UpdateOne(ctx, bson.M{"_id": token}, bson.M{"$set": index}, opts); err != nil {
		return errors.Join(ErrIndexToken, err)
	}
	return nil
}

// createIndexes creates the necessary indexes in the MongoDB database.
func (ms *MongoStorage) createIndexes() error {
	// Create text index on `extraData` for finding user data
	ctx, cancel3 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel3()
	index := mongo.IndexModel{
		Keys: bson.D{
			{Key: "extradata", Value: "text"},
		},
	}
	_, err := ms.users.Indexes().CreateOne(ctx, index)
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
	updateUser, err := dynamicUpdateDocument(user, nil)
	if err != nil {
		return errors.Join(ErrPrepareUser, err)
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opts := options.Update().SetUpsert(true)
	if _, err := ms.users.UpdateOne(ctx, bson.M{"_id": user.ID}, updateUser, opts); err != nil {
		return errors.Join(ErrUpdateUser, err)
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
	for i := range val.NumField() {
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
