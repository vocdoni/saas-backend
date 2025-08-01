package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications/smtp"
	"github.com/vocdoni/saas-backend/subscriptions"
	"github.com/vocdoni/saas-backend/test"
	"go.vocdoni.io/dvote/apiclient"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

type apiTestCase struct {
	name           string
	uri            string
	method         string
	headers        map[string]string
	body           []byte
	expectedStatus int
	expectedBody   []byte
}

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

	testOAuthServiceURL = "http://test-oauth-service"
)

// apiError mimics the anon struct used in errors/errors.go
// TODO: refactor that anon struct into an importable struct somehow
type apiError struct {
	Error string `json:"error"`
	Code  int    `json:"code"`
	Data  any    `json:"data,omitempty"`
}

// testPort is the port used for the API tests.
var testPort int

// testDB is the MongoDB storage for the tests. Make it global so it can be
// accessed by the tests directly.
var testDB *db.MongoStorage

// testMailService is the test mail service for the tests. Make it global so it
// can be accessed by the tests directly.
var testMailService *smtp.Email

// testAPIEndpoint is the Voconed API endpoint for the tests. Make it global so it can be accessed by the tests directly.
var testAPIEndpoint string

// testCSP is the CSP service for the tests. Make it global so it can be accessed by the tests directly.
var testCSP *csp.CSP

// This regex is used in testCreateUser to extract verification codes from emails.
var apiTestVerificationCodeRgx = regexp.MustCompile(`code=([a-zA-Z0-9]+)`)

func init() {
	// set the test port
	testPort = 40000 + internal.RandomInt(10000)
}

// testURL helper function returns the full URL for the given path using the
// test host and port.
//
//revive:disable:import-shadowing
func testURL(path string) string {
	return fmt.Sprintf("http://%s:%d%s", testHost, testPort, path)
}

// mustMarshal helper function marshalls the input interface into a byte slice.
// It panics if the marshalling fails.
func mustMarshal(i any) []byte {
	b, err := json.Marshal(i)
	if err != nil {
		panic(err)
	}
	return b
}

// runAPITestCase helper function runs the given API test case and checks the
// response status code and body against the expected values.
func runAPITestCase(c *qt.C, tc apiTestCase) {
	c.Logf("running api test case: %s", tc.name)
	req, err := http.NewRequest(tc.method, tc.uri, bytes.NewBuffer(tc.body))
	c.Assert(err, qt.IsNil)
	for k, v := range tc.headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	c.Assert(err, qt.IsNil)
	defer func() {
		if err := resp.Body.Close(); err != nil {
			c.Errorf("error closing response body: %v", err)
		}
	}()
	c.Assert(resp.StatusCode, qt.Equals, tc.expectedStatus)
	if tc.expectedBody != nil {
		body, err := io.ReadAll(resp.Body)
		c.Assert(err, qt.IsNil)
		c.Assert(strings.TrimSpace(string(body)), qt.Equals, string(tc.expectedBody))
	}
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
	retries := 3
	for i := range retries {
		if err == nil {
			break
		}
		log.Warnf("failed to create api client (%s), will wait and retry (%d/%d)", err, i, retries)
		time.Sleep(time.Second)
		testAPIClient, err = apiclient.New(testAPIEndpoint)
	}
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
	testMailService = new(smtp.Email)
	if err := testMailService.New(&smtp.Config{
		FromAddress:  adminEmail,
		SMTPUsername: adminUser,
		SMTPPassword: adminPass,
		SMTPServer:   mailHost,
		SMTPPort:     smtpPort.Int(),
		TestAPIPort:  apiPort.Int(),
	}); err != nil {
		panic(err)
	}

	rootKey := new(internal.HexBytes).SetString(test.VoconedFoundedPrivKey)
	testCSP, err = csp.New(ctx, &csp.Config{
		DB:                       testDB,
		MailService:              testMailService,
		NotificationThrottleTime: time.Second,
		NotificationCoolDownTime: time.Second * 5,
		RootKey:                  *rootKey,
	})
	if err != nil {
		panic(err)
	}

	// Initialize the subscriptions service
	subscriptionsService := subscriptions.New(&subscriptions.Config{
		DB: testDB,
	})

	// start the API
	New(&Config{
		Host:                testHost,
		Port:                testPort,
		Secret:              testSecret,
		DB:                  testDB,
		Client:              testAPIClient,
		Account:             testAccount,
		MailService:         testMailService,
		FullTransparentMode: false,
		Subscriptions:       subscriptionsService,
		CSP:                 testCSP,
		OAuthServiceURL:     testOAuthServiceURL,
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
	n := internal.RandomInt(100000000000)
	mail := fmt.Sprintf("%d%s", n, testEmail)

	// Register a new user
	userInfo := &apicommon.UserInfo{
		Email:     mail,
		Password:  password,
		FirstName: fmt.Sprintf("%d%s", n, testFirstName),
		LastName:  fmt.Sprintf("%d%s", n, testLastName),
	}
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "", userInfo, usersEndpoint)

	// Get the verification code from the email
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mailBody, err := testMailService.FindEmail(ctx, mail)
	qt.Assert(t, err, qt.IsNil)

	// Extract the verification code using regex
	mailCode := apiTestVerificationCodeRgx.FindStringSubmatch(mailBody)
	qt.Assert(t, len(mailCode) > 1, qt.IsTrue)

	// Verify the user account
	verification := &apicommon.UserVerification{
		Email: mail,
		Code:  mailCode[1],
	}
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "", verification, verifyUserEndpoint)

	// Login to get the JWT token
	loginInfo := &apicommon.UserInfo{
		Email:    mail,
		Password: password,
	}
	loginResp := requestAndParse[apicommon.LoginResponse](t, http.MethodPost, "", loginInfo, authLoginEndpoint)
	qt.Assert(t, loginResp.Token, qt.Not(qt.Equals), "")

	return loginResp.Token
}

// testCreateOrganization creates a new organization and returns the address.
func testCreateOrganization(t *testing.T, jwt string) common.Address {
	orgName := fmt.Sprintf("org-%d", internal.RandomInt(10000))
	orgInfo := &apicommon.OrganizationInfo{
		Type:    string(db.CompanyType),
		Website: fmt.Sprintf("https://%s.com", orgName),
	}
	orgResp := requestAndParse[apicommon.OrganizationInfo](t, http.MethodPost, jwt, orgInfo, organizationsEndpoint)
	qt.Assert(t, orgResp.Address, qt.Not(qt.Equals), "")

	return orgResp.Address
}

func testNewVocdoniClient(t *testing.T) *apiclient.HTTPclient {
	client, err := apiclient.New(testAPIEndpoint)
	qt.Assert(t, err, qt.IsNil)
	return client
}

// signRemoteSignerAndSendVocdoniTx sends a transaction to the Voconed API and waits for it to be mined.
// It uses the remote signer from the API to sign the transaction.
// Returns the response data if any.
func signRemoteSignerAndSendVocdoniTx(t *testing.T, tx *models.Tx, token string, vocdoniClient *apiclient.HTTPclient,
	orgAddress common.Address,
) (responseData []byte) {
	c := qt.New(t)
	txBytes, err := proto.Marshal(tx)
	c.Assert(err, qt.IsNil)
	td := &apicommon.TransactionData{
		Address:   orgAddress,
		TxPayload: txBytes,
	}

	// sign the transaction using the remote signer from the API
	signedTD := requestAndParse[apicommon.TransactionData](t, http.MethodPost, token, td, signTxEndpoint)

	// submit the transaction
	hash, data, err := vocdoniClient.SendTx(signedTD.TxPayload)
	c.Assert(err, qt.IsNil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = waitUntilTxIsMined(ctx, hash, vocdoniClient)
	c.Assert(err, qt.IsNil)
	return data
}

// signAndSendVocdoniTx signs and sends a transaction to the Voconed API and waits for it to be mined.
// It uses the provided signer to sign the transaction.
// Returns the response data if any.
func signAndSendVocdoniTx(t *testing.T, tx *models.Tx, signer *ethereum.SignKeys, vocdoniClient *apiclient.HTTPclient) []byte {
	c := qt.New(t)
	txBytes, err := proto.Marshal(tx)
	c.Assert(err, qt.IsNil)

	// sign the transaction
	signature1, err := signer.SignVocdoniTx(txBytes, fetchVocdoniChainID(t, vocdoniClient))
	c.Assert(err, qt.IsNil)

	stx, err := proto.Marshal(&models.SignedTx{
		Tx:        txBytes,
		Signature: signature1,
	})
	c.Assert(err, qt.IsNil)

	// submit the transaction
	hash, data, err := vocdoniClient.SendTx(stx)
	c.Assert(err, qt.IsNil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = waitUntilTxIsMined(ctx, hash, vocdoniClient)
	c.Assert(err, qt.IsNil)
	return data
}

// waitUntilTxIsMined waits until the given transaction is mined (included in a block)
func waitUntilTxIsMined(ctx context.Context, txHash []byte, c *apiclient.HTTPclient) error {
	startTime := time.Now()
	for {
		_, err := c.TransactionReference(txHash)
		if err == nil {
			time.Sleep(time.Second * 1) // wait a bit longer to make sure the tx is committed
			log.Infow("transaction mined", "tx",
				hex.EncodeToString(txHash), "duration", time.Since(startTime).String())
			return nil
		}
		select {
		case <-time.After(time.Second * 1):
			continue
		case <-ctx.Done():
			return fmt.Errorf("transaction %s never mined after %s: %w",
				hex.EncodeToString(txHash), time.Since(startTime).String(), ctx.Err())
		}
	}
}

func fetchVocdoniAccountNonce(t *testing.T, client *apiclient.HTTPclient, address common.Address) uint32 {
	acc, err := client.Account(address.String())
	qt.Assert(t, err, qt.IsNil)
	return acc.Nonce
}

func fetchVocdoniChainID(t *testing.T, client *apiclient.HTTPclient) string {
	cid := client.ChainID()
	qt.Assert(t, cid, qt.Not(qt.Equals), "")
	return cid
}

// testCreateCensus creates a new census with the given organization address and census type.
// It returns the census ID.
func testCreateCensus(
	t *testing.T,
	token string,
	orgAddress common.Address,
	authFields db.OrgMemberAuthFields,
	twoFaFields db.OrgMemberTwoFaFields,
) string {
	c := qt.New(t)

	// Create a new census
	censusInfo := &apicommon.OrganizationCensus{
		OrgAddress:  orgAddress,
		Type:        db.CensusTypeSMSorMail,
		AuthFields:  authFields,
		TwoFaFields: twoFaFields,
	}
	createdCensus := requestAndParse[apicommon.CreateCensusResponse](t, http.MethodPost, token, censusInfo, censusEndpoint)
	c.Assert(createdCensus.ID, qt.Not(qt.Equals), "", qt.Commentf("census ID is empty"))

	t.Logf("Created census with ID: %s", createdCensus.ID)
	return createdCensus.ID
}

// testAddMembersToCensus adds members to the given census.
// It returns the number of members added.
func testAddMembersToCensus(t *testing.T, token, censusID string, members []apicommon.OrgMember) uint32 {
	c := qt.New(t)

	// Add members to the census
	membersReq := &apicommon.AddMembersRequest{
		Members: members,
	}
	addedResponse := requestAndParse[apicommon.AddMembersResponse](t, http.MethodPost, token, membersReq,
		censusEndpoint, censusID)
	c.Assert(addedResponse.Added, qt.Equals, uint32(len(members)),
		qt.Commentf("expected %d members, got %d", len(members), addedResponse.Added))

	return addedResponse.Added
}

// testPublishCensus publishes the given census.
// It returns the published census URI and root.
func testPublishCensus(t *testing.T, token, censusID string) (uri string, root string) {
	c := qt.New(t)

	// Publish the census
	publishedCensus := requestAndParse[apicommon.PublishedCensusResponse](t, http.MethodPost, token, nil,
		censusEndpoint, censusID, "publish")
	c.Assert(publishedCensus.URI, qt.Not(qt.Equals), "", qt.Commentf("published census URI is empty"))
	c.Assert(publishedCensus.Root, qt.Not(qt.Equals), "", qt.Commentf("published census root is empty"))

	t.Logf("Published census with URI: %s and Root: %s", publishedCensus.URI, publishedCensus.Root)
	return publishedCensus.URI, publishedCensus.Root.String()
}

// testCreateBundle creates a new process bundle with the given census ID and process IDs.
// It returns the bundle ID and root.
func testCreateBundle(t *testing.T, token, censusID string, processIDs [][]byte) (bundleID string, root string) {
	c := qt.New(t)

	// Convert process IDs to hex strings
	hexProcessIDs := make([]string, len(processIDs))
	for i, pid := range processIDs {
		hexProcessIDs[i] = hex.EncodeToString(pid)
	}

	// Create a new bundle
	bundleReq := &apicommon.CreateProcessBundleRequest{
		CensusID:  censusID,
		Processes: hexProcessIDs,
	}
	bundleResp := requestAndParse[apicommon.CreateProcessBundleResponse](t, http.MethodPost, token, bundleReq, "process", "bundle")
	c.Assert(bundleResp.URI, qt.Not(qt.Equals), "", qt.Commentf("bundle URI is empty"))
	c.Assert(bundleResp.Root, qt.Not(qt.Equals), "", qt.Commentf("bundle root is empty"))

	// Extract the bundle ID from the URI
	bundleURI := bundleResp.URI
	bundleIDStr := bundleURI[len(bundleURI)-len(censusID):]

	t.Logf("Created bundle with ID: %s and Root: %s", bundleIDStr, bundleResp.Root)
	return bundleIDStr, bundleResp.Root.String()
}

// testCSPAuthenticate performs the CSP authentication flow for a member.
// It returns the verified auth token.
func testCSPAuthenticate(t *testing.T, bundleID, participantID, email string) internal.HexBytes {
	c := qt.New(t)

	// Step 1: Initiate authentication (auth/0)
	authReq := &handlers.AuthRequest{
		ParticipantID: participantID,
		Email:         email,
	}
	authResp := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "", authReq, "process", "bundle", bundleID, "auth", "0")
	c.Assert(authResp.AuthToken, qt.Not(qt.Equals), "", qt.Commentf("auth token is empty"))

	t.Logf("Received auth token: %s", authResp.AuthToken.String())

	// Step 2: Get the OTP code from the email with retries
	var mailBody string
	var err error
	maxRetries := 10
	for i := 0; i < maxRetries; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		mailBody, err = testMailService.FindEmail(ctx, email)
		cancel()
		if err == nil {
			break
		}
		t.Logf("Waiting for email, attempt %d/%d...", i+1, maxRetries)
		time.Sleep(500 * time.Millisecond)
	}
	c.Assert(err, qt.IsNil, qt.Commentf("failed to receive email after %d attempts", maxRetries))

	// Extract the OTP code from the email
	otpCode := extractOTPFromEmail(mailBody)
	c.Assert(otpCode, qt.Not(qt.Equals), "", qt.Commentf("failed to extract OTP code from email"))
	t.Logf("Extracted OTP code: %s", otpCode)

	// Step 3: Verify authentication (auth/1)
	authChallengeReq := &handlers.AuthChallengeRequest{
		AuthToken: authResp.AuthToken,
		AuthData:  []string{otpCode},
	}
	verifyResp := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "", authChallengeReq,
		"process", "bundle", bundleID, "auth", "1")
	c.Assert(verifyResp.AuthToken, qt.Not(qt.Equals), "", qt.Commentf("verified auth token is empty"))

	t.Logf("Authentication verified with token: %s", verifyResp.AuthToken.String())
	return verifyResp.AuthToken
}

// testCSPSign signs a payload with the CSP using the given auth token and process ID.
// It returns the signature.
func testCSPSign(t *testing.T, bundleID string, authToken, processID, payload internal.HexBytes) internal.HexBytes {
	c := qt.New(t)

	// Sign with the verified token
	signReq := &handlers.SignRequest{
		AuthToken: authToken,
		ProcessID: processID,
		Payload:   hex.EncodeToString(payload),
	}
	signResp := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "", signReq, "process", "bundle", bundleID, "sign")
	c.Assert(signResp.Signature, qt.Not(qt.Equals), "", qt.Commentf("signature is empty"))

	t.Logf("Received signature: %s", signResp.Signature.String())
	return signResp.Signature
}

// testGenerateVoteProof generates a vote proof with the given signature, process ID, and voter address.
// It returns the proof.
func testGenerateVoteProof(processID, voterAddr, signature internal.HexBytes) *models.Proof {
	return &models.Proof{
		Payload: &models.Proof_Ca{
			Ca: &models.ProofCA{
				Type:      models.ProofCA_ECDSA_PIDSALTED,
				Signature: signature,
				Bundle: &models.CAbundle{
					ProcessId: processID,
					Address:   voterAddr,
				},
			},
		},
	}
}

// testCastVote casts a vote with the given proof and process ID.
// It returns the nullifier.
func testCastVote(t *testing.T, vocdoniClient *apiclient.HTTPclient, signer *ethereum.SignKeys,
	processID internal.HexBytes, proof *models.Proof, votePackage []byte,
) []byte {
	// Create the vote transaction
	tx := models.Tx{
		Payload: &models.Tx_Vote{
			Vote: &models.VoteEnvelope{
				ProcessId:   processID,
				Nonce:       internal.RandomBytes(16),
				Proof:       proof,
				VotePackage: votePackage,
			},
		},
	}

	// Sign and send the transaction
	return signAndSendVocdoniTx(t, &tx, signer, vocdoniClient)
}

// extractOTPFromEmail extracts the OTP code from the email body.
// It returns the OTP code as a string.
func extractOTPFromEmail(mailBody string) string {
	// The OTP code is typically a 6-digit number in the email
	re := regexp.MustCompile(`\b\d{6}\b`)
	matches := re.FindStringSubmatch(mailBody)
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

// testGenerateTestMembers generates a list of test members.
// It returns the list of members.
func testGenerateTestMembers(count int) []apicommon.OrgMember {
	members := make([]apicommon.OrgMember, count)
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("P%03d", i+1)
		members[i] = apicommon.OrgMember{
			MemberNumber: id,
			Name:         fmt.Sprintf("Test User %d", i+1),
			Email:        fmt.Sprintf("%s@example.com", id),
			Phone:        fmt.Sprintf("+346123456%02d", i+1),
		}
	}
	return members
}

// requestAndParse makes a request and parses the JSON response.
// It takes the same parameters as testRequest plus a type parameter for the response.
// Asserts the HTTP Status Code is 200 OK, and returns the parsed response of the specified type.
func requestAndParse[T any](t *testing.T, method, jwt string, jsonBody any, urlPath ...string) T {
	return requestAndParseWithAssertCode[T](http.StatusOK, t, method, jwt, jsonBody, urlPath...)
}

// requestAndParseWithAssertCode makes a request, asserts the expected status code, and parses the JSON response.
// It takes the expected status code as the first parameter, followed by the same parameters as testRequest.
func requestAndParseWithAssertCode[T any](expectedCode int, t *testing.T, method, jwt string, jsonBody any, urlPath ...string) T {
	var result T
	resp, code := testRequest(t, method, jwt, jsonBody, urlPath...)
	qt.Assert(t, code, qt.Equals, expectedCode, qt.Commentf("response: %s", resp))

	err := json.Unmarshal(resp, &result)
	qt.Assert(t, err, qt.IsNil, qt.Commentf("failed to parse response: %s", resp))
	return result
}

// requestAndAssertCode makes a request and asserts the expected status code.
// It takes the expected status code as the first parameter, followed by the same parameters as testRequest.
func requestAndAssertCode(expectedCode int, t *testing.T, method, jwt string, jsonBody any, urlPath ...string) {
	resp, code := testRequest(t, method, jwt, jsonBody, urlPath...)
	qt.Assert(t, code, qt.Equals, expectedCode, qt.Commentf("response: %s", resp))
}
