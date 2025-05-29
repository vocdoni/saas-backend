package db

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/vocdoni/saas-backend/test"
)

var testDB *MongoStorage

// Common test constants
const (
	testOrgAddress  = "0x123456789"
	testDBUserEmail = "test@example.com"
	testDBUserPass  = "testpass123"
	testDBFirstName = "Test"
	testDBLastName  = "User"
	testMemberNo    = "member123"
	testMemberEmail = "member@test.com"
	testPhone       = "+34678909090"
	testName        = "Test Member"
	testPassword    = "testpass123"
	testSalt        = "testSalt"
	invitationCode  = "abc123"
	newUserEmail    = "inviteme@email.com"
	testURI         = "test_uri"
	testRoot        = "test_root"
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	// start a MongoDB container for testing
	dbContainer, err := test.StartMongoContainer(ctx)
	if err != nil {
		panic(fmt.Sprintf("failed to start MongoDB container: %v", err))
	}

	// get the MongoDB connection string
	mongoURI, err := dbContainer.Endpoint(ctx, "mongodb")
	if err != nil {
		panic(fmt.Sprintf("failed to get MongoDB endpoint: %v", err))
	}

	plans, err := ReadPlanJSON()
	if err != nil {
		panic(fmt.Sprintf("failed to read plan JSON: %v", err))
	}

	testDB, err = New(mongoURI, test.RandomDatabaseName(), plans)
	if err != nil {
		panic(fmt.Sprintf("failed to create new MongoDB connection: %v", err))
	}

	code := m.Run()

	// close the database connection
	testDB.Close()

	// stop the MongoDB container
	if err := dbContainer.Terminate(ctx); err != nil {
		panic(fmt.Sprintf("failed to stop MongoDB container: %v", err))
	}

	os.Exit(code)
}
