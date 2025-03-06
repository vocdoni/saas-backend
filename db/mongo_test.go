package db

import (
	"context"
	"testing"

	"github.com/vocdoni/saas-backend/test"
)

// Common test constants
const (
	testOrgAddress       = "0x123456789"
	testDBUserEmail      = "test@example.com"
	testDBUserPass       = "testpass123"
	testDBFirstName      = "Test"
	testDBLastName       = "User"
	testParticipantNo    = "participant123"
	testParticipantEmail = "participant@test.com"
	testPhone            = "+1234567890"
	testName             = "Test Participant"
	testPassword         = "testpass123"
	testSalt             = "testSalt"
	invitationCode       = "abc123"
	newMemberEmail       = "inviteme@email.com"
	testURI              = "test_uri"
	testRoot             = "test_root"
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
