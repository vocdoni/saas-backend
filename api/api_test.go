package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

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

// testURL helper function returns the full URL for the given path using the
// test host and port.
func testURL(path string) string {
	return fmt.Sprintf("http://%s:%d%s", testHost, testPort, path)
}

// mustMarshall helper function marshalls the input interface into a byte slice.
// It panics if the marshalling fails.
func mustMarshall(i any) []byte {
	b, err := json.Marshal(i)
	if err != nil {
		panic(err)
	}
	return b
}

// pingAPI helper function pings the API endpoint and retries the request
// if it fails until the retries limit is reached. It returns an error if the
// request fails or the status code is not 200 as many times as the retries
// limit.
func pingAPI(endpoint string, retries int) error {
	// create a new ping request
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	// try to ping the API
	var pingErr error
	for i := 0; i < retries; i++ {
		var resp *http.Response
		if resp, pingErr = http.DefaultClient.Do(req); pingErr == nil {
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			pingErr = fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		}
		time.Sleep(time.Second)
	}
	return pingErr
}

// TestMain function starts the MongoDB container, the Voconed container, and
// the API server before running the tests. It also creates a new MongoDB
// connection with a random database name, a new Voconed API client, and a new
// account with the Voconed private key and the API container endpoint. It
// starts the API server and waits for it to start before running the tests.
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
	// wait for the API to start
	if err := pingAPI(testURL(pingEndpoint), 5); err != nil {
		panic(err)
	}
	// run the tests
	os.Exit(m.Run())
}
