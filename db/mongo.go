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
	// init the collections
	ms.client = client
	if err := ms.initCollections(database); err != nil {
		return nil, err
	}
	// if reset flag is enabled, Reset drops the database documents and recreates indexes
	// else, just createIndexes
	if reset := os.Getenv("VOCDONI_MONGO_RESET_DB"); reset != "" {
		err := ms.Reset()
		if err != nil {
			return nil, err
		}
	} else {
		err := ms.createIndexes()
		if err != nil {
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
		Options: nil,
	}
	if _, err := ms.users.Indexes().CreateOne(ctx, userEmailIndex); err != nil {
		return fmt.Errorf("failed to create index on addresses for users: %w", err)
	}

	return nil
}

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

func (ms *MongoStorage) String() string {
	const contextTimeout = 30 * time.Second
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel()
	cur, err := ms.users.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warn(err)
		return "{}"
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel2()
	var users UserCollection
	for cur.Next(ctx2) {
		var user User
		err := cur.Decode(&user)
		if err != nil {
			log.Warn(err)
		}
		users.Users = append(users.Users, user)
	}

	data, err := json.Marshal(&Collection{users})
	if err != nil {
		log.Warn(err)
	}
	return string(data)
}

// Import imports a JSON dataset produced by String() into the database.
func (ms *MongoStorage) Import(jsonData []byte) error {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()

	log.Infof("importing database")
	var collection Collection
	err := json.Unmarshal(jsonData, &collection)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Upsert Users
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

	log.Infof("imported database!")
	return nil
}
