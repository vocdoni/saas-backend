package twofactor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/vocdoni/saas-backend/test"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestJSONStorageBulkAddUser(t *testing.T) {
	// Create a temporary directory for the test
	tempDir, err := os.MkdirTemp("", "twofactor-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir) // nolint: errcheck

	// Initialize the storage
	js := &JSONstorage{}
	err = js.Init(filepath.Join(tempDir, "jsonstorage"), DefaultMaxSMSattempts, DefaultSMScoolDownTime)
	require.NoError(t, err)

	// Create a large number of users (more than 1000 to test batching)
	numUsers := 2500
	users := make([]UserData, numUsers)

	for i := 0; i < numUsers; i++ {
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
	err = js.BulkAddUser(users)
	require.NoError(t, err)

	// Verify that all users were added
	for i := 0; i < numUsers; i++ {
		exists := js.Exists(users[i].UserID)
		require.True(t, exists, "User %d should exist", i)
	}
}

func TestMongoStorageBulkAddUser(t *testing.T) {
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
	require.NoError(t, err)
	client.Database("twofactor").Drop(ctx)

	// Initialize the storage
	ms := &MongoStorage{}
	err = ms.Init(client, DefaultMaxSMSattempts, DefaultSMScoolDownTime)
	require.NoError(t, err)

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
	require.NoError(t, err)

	// Verify that all users were added
	for i := range numUsers {
		exists := ms.Exists(users[i].UserID)
		require.True(t, exists, "User %d should exist", i)
	}

	// Clean up - delete all test users
	for i := range numUsers {
		err = ms.DelUser(users[i].UserID)
		require.NoError(t, err)
	}
}
