package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications/smtp"
	"github.com/vocdoni/saas-backend/subscriptions"
	"github.com/vocdoni/saas-backend/test"
	"go.vocdoni.io/dvote/apiclient"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// Removed unused type

const (
	testSecret    = "super-secret"
	testEmail     = "user@test.com"
	testPass      = "password123"
	testFirstName = "test"
	testLastName  = "user"
	testHost      = "0.0.0.0"

	adminEmail = "admin@test.com"
	adminUser  = "admin"
	adminPass  = "admin123"
)

// testPort is the port used for the API tests.
var testPort int

// testDB is the MongoDB storage for the tests. Make it global so it can be
// accessed by the tests directly.
var testDB *db.MongoStorage

// testMailService is the test mail service for the tests. Make it global so it
// can be accessed by the tests directly.
var testMailService *smtp.SMTPEmail

// testAPIEndpoint is the Voconed API endpoint for the tests. Make it global so it can be accessed by the tests directly.
var testAPIEndpoint string

func init() {
	// set the test port
	testPort = 40000 + internal.RandomInt(10000)
}

// testURL helper function returns the full URL for the given path using the
// test host and port.
func testURL(path string) string {
	return fmt.Sprintf("http://%s:%d%s", testHost, testPort, path)
}

// Removed unused function

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
	log.Init("debug", "stdout", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
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
	// set reset db env var to true
	_ = os.Setenv("VOCDONI_MONGO_RESET_DB", "true")
	// create a new MongoDB connection with the test database
	plans, err := db.ReadPlanJSON()
	if err != nil {
		panic(err)
	}
	if testDB, err = db.New(mongoURI, test.RandomDatabaseName(), plans); err != nil {
		panic(err)
	}
	defer testDB.Close()
	// start the voconed container
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
	// set the test API endpoint global variable
	testAPIEndpoint = test.VoconedAPIURL(apiEndpoint)
	// start test mail server
	testMailServer, err := test.StartMailService(ctx)
	if err != nil {
		panic(err)
	}
	// get the host, the SMTP port and the API port
	mailHost, err := testMailServer.Host(ctx)
	if err != nil {
		panic(err)
	}
	smtpPort, err := testMailServer.MappedPort(ctx, test.MailSMTPPort)
	if err != nil {
		panic(err)
	}
	apiPort, err := testMailServer.MappedPort(ctx, test.MailAPIPort)
	if err != nil {
		panic(err)
	}
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
	// create test mail service
	testMailService = new(smtp.SMTPEmail)
	if err := testMailService.New(&smtp.SMTPConfig{
		FromAddress:  adminEmail,
		SMTPUsername: adminUser,
		SMTPPassword: adminPass,
		SMTPServer:   mailHost,
		SMTPPort:     smtpPort.Int(),
		TestAPIPort:  apiPort.Int(),
	}); err != nil {
		panic(err)
	}
	// Initialize the subscriptions service
	subscriptionsService := subscriptions.New(&subscriptions.SubscriptionsConfig{
		DB: testDB,
	})

	// start the API
	New(&APIConfig{
		Host:                testHost,
		Port:                testPort,
		Secret:              testSecret,
		DB:                  testDB,
		Client:              testAPIClient,
		Account:             testAccount,
		MailService:         testMailService,
		FullTransparentMode: false,
		Subscriptions:       subscriptionsService,
	}).Start()
	// wait for the API to start
	if err := pingAPI(testURL(pingEndpoint), 5); err != nil {
		panic(err)
	}
	log.Infow("API server started", "host", testHost, "port", testPort)
	// run the tests
	os.Exit(m.Run())
}

// request sends a request to the API service and returns the response body and status code.
// The body is expected to be a JSON object or null.
// If jwt is not empty, it will be sent as a Bearer token.
func testRequest(t *testing.T, method, jwt string, jsonBody any, urlPath ...string) ([]byte, int) {
	body, err := json.Marshal(jsonBody)
	qt.Assert(t, err, qt.IsNil)
	testURL := fmt.Sprintf("http://%s:%d", testHost, testPort)
	u, err := url.Parse(testURL)
	qt.Assert(t, err, qt.IsNil)
	// Handle the case where the last path component contains query parameters
	lastIndex := len(urlPath) - 1
	if lastIndex >= 0 && strings.Contains(urlPath[lastIndex], "?") {
		parts := strings.SplitN(urlPath[lastIndex], "?", 2)
		urlPath[lastIndex] = parts[0]
		u.Path = path.Join(u.Path, path.Join(urlPath...))
		u.RawQuery = parts[1]
	} else {
		u.Path = path.Join(u.Path, path.Join(urlPath...))
	}
	headers := http.Header{}
	if jwt != "" {
		headers = http.Header{"Authorization": []string{"Bearer " + jwt}}
	}
	t.Logf("requesting %s %s", method, u.String())
	req, err := http.NewRequest(method, u.String(), bytes.NewReader(body))
	qt.Assert(t, err, qt.IsNil)
	req.Header = headers
	if method == http.MethodPost || method == http.MethodPut {
		req.Header.Set("Content-Type", "application/json")
	}
	c := http.DefaultClient
	resp, err := c.Do(req)
	if err != nil {
		t.Logf("http error: %v", err)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Logf("read error: %v", err)
	}
	return data, resp.StatusCode
}

// testCreateUser creates a new user with the given password and returns the JWT token.
func testCreateUser(t *testing.T, password string) string {
	n := internal.RandomInt(10000)
	mail := fmt.Sprintf("%d%s", n, testEmail)

	// Register a new user
	userInfo := &UserInfo{
		Email:     mail,
		Password:  password,
		FirstName: fmt.Sprintf("%d%s", n, testFirstName),
		LastName:  fmt.Sprintf("%d%s", n, testLastName),
	}
	_, status := testRequest(t, http.MethodPost, "", userInfo, usersEndpoint)
	qt.Assert(t, status, qt.Equals, http.StatusOK)

	// Get the verification code from the email
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mailBody, err := testMailService.FindEmail(ctx, mail)
	qt.Assert(t, err, qt.IsNil)

	// Extract the verification code using regex
	mailCode := verificationCodeRgx.FindStringSubmatch(mailBody)
	qt.Assert(t, len(mailCode) > 1, qt.IsTrue)

	// Verify the user account
	verification := &UserVerification{
		Email: mail,
		Code:  mailCode[1],
	}
	_, status = testRequest(t, http.MethodPost, "", verification, verifyUserEndpoint)
	qt.Assert(t, status, qt.Equals, http.StatusOK)

	// Login to get the JWT token
	loginInfo := &UserInfo{
		Email:    mail,
		Password: password,
	}
	respBody, status := testRequest(t, http.MethodPost, "", loginInfo, authLoginEndpoint)
	qt.Assert(t, status, qt.Equals, http.StatusOK)

	// Extract the token from the response
	var loginResp LoginResponse
	err = json.Unmarshal(respBody, &loginResp)
	qt.Assert(t, err, qt.IsNil)
	qt.Assert(t, loginResp.Token, qt.Not(qt.Equals), "")

	return loginResp.Token
}

// testCreateOrganization creates a new organization and returns the address.
func testCreateOrganization(t *testing.T, jwt string) internal.HexBytes {
	orgName := fmt.Sprintf("org-%d", internal.RandomInt(10000))
	orgInfo := &OrganizationInfo{
		Type:    string(db.CompanyType),
		Website: fmt.Sprintf("https://%s.com", orgName),
	}
	respBody, status := testRequest(t, http.MethodPost, jwt, orgInfo, organizationsEndpoint)
	qt.Assert(t, status, qt.Equals, http.StatusOK)

	var orgResp OrganizationInfo
	err := json.Unmarshal(respBody, &orgResp)
	qt.Assert(t, err, qt.IsNil)
	qt.Assert(t, orgResp.Address, qt.Not(qt.Equals), "")

	addr := new(internal.HexBytes).SetString(orgResp.Address)
	return *addr
}

func testNewVocdoniClient(t *testing.T) *apiclient.HTTPclient {
	client, err := apiclient.New(testAPIEndpoint)
	qt.Assert(t, err, qt.IsNil)
	return client
}

// Removed unused function

// sendVocdoniTx sends a transaction to the Voconed API and waits for it to be mined.
// Returns the response data if any.
func sendVocdoniTx(t *testing.T, tx *models.Tx, token string, vocdoniClient *apiclient.HTTPclient,
	orgAddress internal.HexBytes,
) []byte {
	c := qt.New(t)
	txBytes, err := proto.Marshal(tx)
	c.Assert(err, qt.IsNil)
	td := &TransactionData{
		Address:   orgAddress,
		TxPayload: txBytes,
	}

	// sign the transaction using the remote signer from the API
	resp, code := testRequest(t, http.MethodPost, token, td, signTxEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))
	c.Assert(json.Unmarshal(resp, td), qt.IsNil)

	// submit the transaction
	hash, data, err := vocdoniClient.SendTx(td.TxPayload)
	c.Assert(err, qt.IsNil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = vocdoniClient.WaitUntilTxIsMined(ctx, hash)
	c.Assert(err, qt.IsNil)
	return data
}

func fetchVocdoniAccountNonce(t *testing.T, client *apiclient.HTTPclient, address internal.HexBytes) uint32 {
	account, err := client.Account(address.String())
	qt.Assert(t, err, qt.IsNil)
	return account.Nonce
}
