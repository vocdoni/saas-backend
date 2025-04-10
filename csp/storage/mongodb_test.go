package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/vocdoni/saas-backend/test"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

var testDB *MongoStorage

func TestMain(m *testing.M) {
	ctx := context.Background()
	// start a MongoDB container for testing
	dbContainer, err := test.StartMongoContainer(ctx)
	if err != nil {
		panic(fmt.Sprintf("failed to start MongoDB container: %v", err))
	}
	// get the MongoDB connection string with replica set name
	mongoURI, err := test.GetMongoURIWithReplicaSet(ctx, dbContainer)
	if err != nil {
		panic(fmt.Sprintf("failed to get MongoDB endpoint: %v", err))
	}
	// preparing connection
	opts := options.Client()
	opts.ApplyURI(mongoURI)
	opts.SetMaxConnecting(200)
	timeout := time.Second * 10
	opts.ConnectTimeout = &timeout
	// create a new client with the connection options
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		panic(fmt.Errorf("cannot connect to mongodb: %w", err))
	}
	// check if the connection is successful
	ctx, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	// try to ping the database
	if err = client.Ping(ctx, readpref.Primary()); err != nil {
		panic(fmt.Errorf("cannot ping to mongodb: %w", err))
	}
	testDB = new(MongoStorage)
	if err := testDB.Init(&MongoConfig{Client: client}); err != nil {
		panic(fmt.Sprintf("failed to create new MongoDB connection: %v", err))
	}
	// reset the database
	if err := testDB.Reset(); err != nil {
		panic(fmt.Sprintf("failed to close MongoDB connection: %v", err))
	}
	// ensure the database is reset when the test finishes
	defer func() {
		if err := testDB.Reset(); err != nil {
			panic(fmt.Sprintf("failed to close MongoDB connection: %v", err))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.Disconnect(ctx); err != nil {
			panic(fmt.Sprintf("failed to close MongoDB connection: %v", err))
		}
		if err := dbContainer.Terminate(ctx); err != nil {
			panic(fmt.Sprintf("failed to terminate MongoDB container: %v", err))
		}
	}()
	os.Exit(m.Run())
}
