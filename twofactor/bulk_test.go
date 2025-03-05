package twofactor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
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
		t.Skip("Skipping MongoDB test: MONGO_URI environment variable not set")
	}

	// Initialize the storage
	ms := &MongoStorage{}
	err := ms.Init(mongoURI, DefaultMaxSMSattempts, DefaultSMScoolDownTime)
	require.NoError(t, err)

	// Create a unique collection name for this test
	testID := uuid.New().String()

	// Create a large number of users (more than 1000 to test batching)
	numUsers := 2500
	users := make([]UserData, numUsers)

	for i := 0; i < numUsers; i++ {
		userID := make([]byte, 32)
		// Create a unique user ID for each user
		copy(userID, []byte(testID+uuid.New().String()))

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
	err = ms.BulkAddUser(users)
	require.NoError(t, err)

	// Verify that all users were added
	for i := 0; i < numUsers; i++ {
		exists := ms.Exists(users[i].UserID)
		require.True(t, exists, "User %d should exist", i)
	}

	// Clean up - delete all test users
	for i := 0; i < numUsers; i++ {
		err = ms.DelUser(users[i].UserID)
		require.NoError(t, err)
	}
}
