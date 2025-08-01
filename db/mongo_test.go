package db

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/test"
)

var testDB *MongoStorage

// Common test constants
var (
	testOrgAddress        = common.Address{0x01, 0x23, 0x45, 0x67, 0x89}
	testAnotherOrgAddress = common.Address{0x10, 0x11, 0x12, 0x13, 0x14}
	testThirdOrgAddress   = common.Address{0xca, 0xfe, 0x03}
	testFourthOrgAddress  = common.Address{0xca, 0xfe, 0x04}
	testNonExistentOrg    = common.Address{0x01, 0x02, 0x03, 0x04}
	testPhone             = NewPhone("+34678909090")
)

const (
	testDBUserEmail  = "test@example.com"
	testDBUserPass   = "testpass123"
	testDBFirstName  = "Test"
	testDBLastName   = "User"
	testMemberNumber = "member123"
	testMemberEmail  = "member@test.com"
	testName         = "Test Member"
	testPassword     = "testpass123"
	testSalt         = "testSalt"
	invitationCode   = "abc123"
	newUserEmail     = "inviteme@email.com"
	testURI          = "test_uri"
	testRoot         = "test_root"
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
