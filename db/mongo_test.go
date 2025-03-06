package db

import (
	"context"
	"os"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/test"
)

// startTestDB starts a MongoDB container for testing and returns a new MongoStorage instance.
func startTestDB(t *testing.T) *MongoStorage {
	ctx := context.Background()
	// start a MongoDB container for testing
	dbContainer, err := test.StartMongoContainer(ctx)
	if err != nil {
		t.Fatalf("failed to start MongoDB container: %v", err)
	}

	// ensure the container is stopped when the test finishes
	t.Cleanup(func() { _ = dbContainer.Terminate(ctx) })

	// get the MongoDB connection string
	mongoURI, err := dbContainer.Endpoint(ctx, "mongodb")
	if err != nil {
		t.Fatalf("failed to get MongoDB endpoint: %v", err)
	}

	plans, err := ReadPlanJSON()
	if err != nil {
		t.Fatalf("failed to read plan JSON: %v", err)
	}

	testDB, err := New(mongoURI, test.RandomDatabaseName(), plans)
	if err != nil {
		t.Fatalf("failed to create new MongoDB connection: %v", err)
	}

	// ensure the database is closed when the test finishes
	t.Cleanup(func() { testDB.Close() })

	return testDB
}

var db *MongoStorage

func TestMain(m *testing.M) {
	ctx := context.Background()
	// start a MongoDB container for testing
	container, err := test.StartMongoContainer(ctx)
	if err != nil {
		panic(err)
	}
	// ensure the container is stopped when the test finishes
	defer func() { _ = container.Terminate(ctx) }()
	// get the MongoDB connection string
	mongoURI, err := container.Endpoint(ctx, "mongodb")
	if err != nil {
		panic(err)
	}
	// set reset db env var to true
	_ = os.Setenv("VOCDONI_MONGO_RESET_DB", "true")
	// create a new MongoDB connection with the test database
	plans, err := ReadPlanJSON()
	if err != nil {
		panic(err)
	}
	db, err = New(mongoURI, test.RandomDatabaseName(), plans)
	if err != nil {
		panic(err)
	}
	// close the connection when the test finishes
	defer db.Close()
	// run the tests
	os.Exit(m.Run())
}

func resetDB(c *qt.C) {
	c.Assert(db.Reset(), qt.IsNil)
}
