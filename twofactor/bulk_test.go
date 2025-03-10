package twofactor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/test"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestJSONStorageBulkAddUser(t *testing.T) {
	c := qt.New(t)
	// Create a temporary directory for the test
	tempDir, err := os.MkdirTemp("", "twofactor-test-*")
	c.Assert(err, qt.IsNil)
	defer os.RemoveAll(tempDir) // nolint: errcheck
	// Initialize the storage
	js := &JSONstorage{}
	err = js.Init(filepath.Join(tempDir, "jsonstorage"), DefaultMaxSMSattempts, DefaultSMScoolDownTime)
	c.Assert(err, qt.IsNil)
	// Create a large number of users (more than 1000 to test batching)
	numUsers := 2500
	users := make([]UserData, numUsers)
	for i := range numUsers {
		userID := make([]byte, 32)
		// Create a unique user ID for each user
		copy(userID, []byte(uuid.New().String()))
		// Create a user with some test data
		users[i] = UserData{
			UserID:    userID,
			ExtraData: "test data",
			Phone:     "+1234567890",
			Mail:      "test@example.com",
			Elections: map[string]UserElection{
				"election1": {
					ElectionID:        []byte("election1"),
					RemainingAttempts: DefaultMaxSMSattempts,
					Consumed:          false,
				},
			},
		}
	}
	// Add users in bulk
	c.Assert(js.BulkAddUser(users), qt.IsNil)
	// Verify that all users were added
	for i := range numUsers {
		c.Assert(js.Exists(users[i].UserID), qt.IsTrue, qt.Commentf("User %d should exist", i))
	}
}

func TestMongoStorageBulkAddUser(t *testing.T) {
	c := qt.New(t)
	// Skip this test if no MongoDB connection is available
	mongoURI := os.Getenv("MONGO_URI")
	if mongoURI == "" {
		ctx := context.Background()
		// start a MongoDB container for testing
		container, err := test.StartMongoContainer(ctx)
		if err != nil {
			t.Skip("MongoDB container not available")
		}
		// ensure the container is stopped when the test finishes
		defer func() { _ = container.Terminate(ctx) }()
		// get the MongoDB connection string
		mongoURI, err = container.Endpoint(ctx, "mongodb")
		if err != nil {
			t.Skip("MongoDB container not available")
		}
	}
	opts := options.Client()
	opts.ApplyURI(mongoURI)
	opts.SetMaxConnecting(200)
	timeout := time.Second * 10
	opts.ConnectTimeout = &timeout
	// create a new client with the connection options
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	client, err := mongo.Connect(ctx, opts)
	c.Assert(err, qt.IsNil)
	err = client.Database("twofactor").Drop(ctx)
	c.Assert(err, qt.IsNil)
	// Initialize the storage
	ms := &MongoStorage{}
	err = ms.Init(client, DefaultMaxSMSattempts, DefaultSMScoolDownTime)
	c.Assert(err, qt.IsNil)
	// Create a unique collection name for this test
	testID, _ := uuid.NewRandom()
	// Create a large number of users (more than 1000 to test batching)
	numUsers := 2500
	users := make([]UserData, numUsers)
	for i := range numUsers {
		// Create a unique user ID for each user
		randUUID, _ := uuid.NewRandom()
		// Create a user with some test data
		users[i] = UserData{
			UserID:    append(testID[:], randUUID[:]...),
			ExtraData: "test data",
			Phone:     "+1234567890",
			Mail:      "test@example.com",
			Elections: map[string]UserElection{
				"election1": {
					ElectionID:        []byte("election1"),
					RemainingAttempts: DefaultMaxSMSattempts,
					Consumed:          false,
				},
			},
		}
	}
	// Add users in bulk
	err = ms.BulkAddUser(users)
	c.Assert(err, qt.IsNil)
	// Verify that all users were added
	for i := range numUsers {
		c.Assert(ms.Exists(users[i].UserID), qt.IsTrue, qt.Commentf("User %d should exist", i))
	}
	// Clean up - delete all test users
	for i := range numUsers {
		c.Assert(ms.DelUser(users[i].UserID), qt.IsNil)
	}
}
