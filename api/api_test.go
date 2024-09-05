package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/test"
	"go.vocdoni.io/dvote/apiclient"
)

type apiTestCase struct {
	uri            string
	method         string
	body           []byte
	expectedStatus int
	expectedBody   []byte
}

const (
	testSecret = "super-secret"
	testEmail  = "admin@test.com"
	testPass   = "password123"
	testHost   = "0.0.0.0"
	testPort   = 7788
)

func testURL(path string) string {
	return fmt.Sprintf("http://%s:%d%s", testHost, testPort, path)
}

func mustMarshall(i any) []byte {
	b, err := json.Marshal(i)
	if err != nil {
		panic(err)
	}
	return b
}

func TestMain(m *testing.M) {
	ctx := context.Background()
	// start a MongoDB container for testing
	dbContainer, err := test.StartMongoContainer(ctx)
	if err != nil {
		panic(err)
	}
	// ensure the container is stopped when the test finishes
	defer func() { _ = dbContainer.Terminate(ctx) }()
	// get the MongoDB connection string
	mongoURI, err := dbContainer.Endpoint(ctx, "mongodb")
	if err != nil {
		panic(err)
	}
	// start the faucet and voconed containers
	// faucetContainer, err := test.StartVocfaucetContainer(ctx)
	// if err != nil {
	// 	panic(err)
	// }
	// defer func() { _ = faucetContainer.Terminate(ctx) }()
	apiContainer, err := test.StartVoconedContainer(ctx)
	if err != nil {
		panic(err)
	}
	defer func() { _ = apiContainer.Terminate(ctx) }()
	// get the API endpoint
	apiEndpoint, err := apiContainer.Endpoint(ctx, "http")
	if err != nil {
		panic(err)
	}
	testAPIEndpoint := test.VoconedAPIURL(apiEndpoint)
	// set reset db env var to true
	_ = os.Setenv("VOCDONI_MONGO_RESET_DB", "true")
	// create a new MongoDB connection with the test database
	testDB, err := db.New(mongoURI, test.RandomDatabaseName())
	if err != nil {
		panic(err)
	}
	defer testDB.Close()
	// create the remote test API client
	testAPIClient, err := apiclient.New(testAPIEndpoint)
	if err != nil {
		panic(err)
	}
	// create the test account with the Voconed private key and the API
	// container endpoint
	testAccount, err := account.New(test.VoconedFoundedPrivKey, testAPIEndpoint)
	if err != nil {
		panic(err)
	}
	// start the API
	New(&APIConfig{
		Host:                testHost,
		Port:                testPort,
		Secret:              testSecret,
		DB:                  testDB,
		Client:              testAPIClient,
		Account:             testAccount,
		FullTransparentMode: false,
	}).Start()
	// run the tests
	os.Exit(m.Run())
}
