package db

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.vocdoni.io/dvote/log"
)

// MongoStorage uses an external MongoDB service for stoting the user data and election details.
type MongoStorage struct {
	database string
	client   *mongo.Client
	keysLock sync.RWMutex

	users         *mongo.Collection
	organizations *mongo.Collection
}

type Options struct {
	MongoURL string
	Database string
}

func New(url, database string) (*MongoStorage, error) {
	var err error
	ms := &MongoStorage{}
	if url == "" {
		return nil, fmt.Errorf("mongo URL is not defined")
	}
	if database == "" {
		return nil, fmt.Errorf("mongo database is not defined")
	}
	log.Infow("connecting to mongodb", "url", url, "database", database)
	// preparing connection
	opts := options.Client()
	opts.ApplyURI(url)
	opts.SetMaxConnecting(200)
	timeout := time.Second * 10
	opts.ConnectTimeout = &timeout
	// create a new client with the connection options
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to mongodb: %w", err)
	}
	// check if the connection is successful
	ctx, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	err = client.Ping(ctx, readpref.Primary())
	if err != nil {
		return nil, fmt.Errorf("cannot connect to mongodb: %w", err)
	}
	// init the database client
	ms.client = client
	ms.database = database
	// init the collections
	if err := ms.initCollections(ms.database); err != nil {
		return nil, err
	}
	// if reset flag is enabled, Reset drops the database documents and recreates indexes
	// else, just init collections and create indexes
	if reset := os.Getenv("VOCDONI_MONGO_RESET_DB"); reset != "" {
		if err := ms.Reset(); err != nil {
			return nil, err
		}
	} else {
		// create indexes
		if err := ms.createIndexes(); err != nil {
			return nil, err
		}
	}
	return ms, nil
}

func (ms *MongoStorage) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ms.client.Disconnect(ctx); err != nil {
		log.Warn(err)
	}
}

func (ms *MongoStorage) Reset() error {
	log.Infof("resetting database")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// drop users collection
	if err := ms.users.Drop(ctx); err != nil {
		return err
	}
	// drop organizations collection
	if err := ms.organizations.Drop(ctx); err != nil {
		return err
	}
	// init the collections
	if err := ms.initCollections(ms.database); err != nil {
		return err
	}
	// create indexes
	if err := ms.createIndexes(); err != nil {
		return err
	}
	return nil
}

func (ms *MongoStorage) String() string {
	const contextTimeout = 30 * time.Second
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// get all users
	ctx, cancel := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel()
	userCur, err := ms.users.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warn(err)
		return "{}"
	}
	// append all users to the export data
	ctx, cancel2 := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel2()
	var users UserCollection
	for userCur.Next(ctx) {
		var user User
		err := userCur.Decode(&user)
		if err != nil {
			log.Warn(err)
		}
		users.Users = append(users.Users, user)
	}
	// get all organizations
	ctx, cancel3 := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel3()
	orgCur, err := ms.organizations.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warn(err)
		return "{}"
	}
	// append all organizations to the export data
	ctx, cancel4 := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel4()
	var organizations OrganizationCollection
	for orgCur.Next(ctx) {
		var org Organization
		err := orgCur.Decode(&org)
		if err != nil {
			log.Warn(err)
		}
		organizations.Organizations = append(organizations.Organizations, org)
	}
	// encode the data to JSON and return it
	data, err := json.Marshal(&Collection{users, organizations})
	if err != nil {
		log.Warn(err)
	}
	return string(data)
}

// Import imports a JSON dataset produced by String() into the database.
func (ms *MongoStorage) Import(jsonData []byte) error {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// decode import data
	log.Infof("importing database")
	var collection Collection
	err := json.Unmarshal(jsonData, &collection)
	if err != nil {
		return err
	}
	// create global context to import data
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	// upsert users collection
	log.Infow("importing users", "count", len(collection.Users))
	for _, user := range collection.Users {
		filter := bson.M{"_id": user.ID}
		update := bson.M{"$set": user}
		opts := options.Update().SetUpsert(true)
		_, err := ms.users.UpdateOne(ctx, filter, update, opts)
		if err != nil {
			log.Warnw("error upserting user", "err", err, "user", user.ID)
		}
	}
	// upsert organizations collection
	log.Infow("importing organizations", "count", len(collection.Organizations))
	for _, org := range collection.Organizations {
		filter := bson.M{"_id": org.Address}
		update := bson.M{"$set": org}
		opts := options.Update().SetUpsert(true)
		_, err := ms.organizations.UpdateOne(ctx, filter, update, opts)
		if err != nil {
			log.Warnw("error upserting organization", "err", err, "organization", org.Address)
		}
	}
	log.Infof("imported database!")
	return nil
}
