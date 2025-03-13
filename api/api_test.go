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

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/account"
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

// testCSP is the CSP service for the tests. Make it global so it can be accessed by the tests directly.
var testCSP *csp.CSP

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

	rootKey := new(internal.HexBytes).SetString(test.VoconedFoundedPrivKey)
	testCSP, err = csp.New(ctx, &csp.CSPConfig{
		DBName:                   "apiTestCSP",
		MongoClient:              testDB.DBClient,
		MailService:              testMailService,
		NotificationThrottleTime: time.Second,
		NotificationCoolDownTime: time.Second * 5,
		RootKey:                  *rootKey,
	})
	if err != nil {
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
		CSP:                 testCSP,
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

// signRemoteSignerAndSendVocdoniTx sends a transaction to the Voconed API and waits for it to be mined.
// It uses the remote signer from the API to sign the transaction.
// Returns the response data if any.
func signRemoteSignerAndSendVocdoniTx(t *testing.T, tx *models.Tx, token string, vocdoniClient *apiclient.HTTPclient,
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
	_, err = vocdoniClient.WaitUntilTxIsMined(ctx, hash)
	c.Assert(err, qt.IsNil)
	return data
}

func fetchVocdoniAccountNonce(t *testing.T, client *apiclient.HTTPclient, address internal.HexBytes) uint32 {
	account, err := client.Account(address.String())
	qt.Assert(t, err, qt.IsNil)
	return account.Nonce
}

func fetchVocdoniChainID(t *testing.T, client *apiclient.HTTPclient) string {
	cid := client.ChainID()
	qt.Assert(t, cid, qt.Not(qt.Equals), "")
	return cid
}

// testCreateCensus creates a new census with the given organization address and census type.
// It returns the census ID.
func testCreateCensus(t *testing.T, token string, orgAddress internal.HexBytes, censusType string) string {
	c := qt.New(t)

	// Create a new census
	censusInfo := &OrganizationCensus{
		Type:       db.CensusType(censusType),
		OrgAddress: orgAddress.String(),
	}
	resp, code := testRequest(t, http.MethodPost, token, censusInfo, censusEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("failed to create census: %s", resp))

	// Parse the response to get the census ID
	var createdCensus OrganizationCensus
	err := json.Unmarshal(resp, &createdCensus)
	c.Assert(err, qt.IsNil)
	c.Assert(createdCensus.ID, qt.Not(qt.Equals), "", qt.Commentf("census ID is empty"))

	t.Logf("Created census with ID: %s", createdCensus.ID)
	return createdCensus.ID
}

// testAddParticipantsToCensus adds participants to the given census.
// It returns the number of participants added.
func testAddParticipantsToCensus(t *testing.T, token, censusID string, participants []OrgParticipant) uint32 {
	c := qt.New(t)

	// Add participants to the census
	participantsReq := &AddParticipantsRequest{
		Participants: participants,
	}
	resp, code := testRequest(t, http.MethodPost, token, participantsReq, censusEndpoint, censusID)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("failed to add participants: %s", resp))

	// Verify the response contains the number of participants added
	var addedResponse AddParticipantsResponse
	err := json.Unmarshal(resp, &addedResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(addedResponse.ParticipantsNo, qt.Equals, uint32(len(participants)),
		qt.Commentf("expected %d participants, got %d", len(participants), addedResponse.ParticipantsNo))

	return addedResponse.ParticipantsNo
}

// testPublishCensus publishes the given census.
// It returns the published census URI and root.
func testPublishCensus(t *testing.T, token, censusID string) (string, string) {
	c := qt.New(t)

	// Publish the census
	resp, code := testRequest(t, http.MethodPost, token, nil, censusEndpoint, censusID, "publish")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("failed to publish census: %s", resp))

	var publishedCensus PublishedCensusResponse
	err := json.Unmarshal(resp, &publishedCensus)
	c.Assert(err, qt.IsNil)
	c.Assert(publishedCensus.URI, qt.Not(qt.Equals), "", qt.Commentf("published census URI is empty"))
	c.Assert(publishedCensus.Root, qt.Not(qt.Equals), "", qt.Commentf("published census root is empty"))

	t.Logf("Published census with URI: %s and Root: %s", publishedCensus.URI, publishedCensus.Root.String())
	return publishedCensus.URI, publishedCensus.Root.String()
}

// testCreateBundle creates a new process bundle with the given census ID and process IDs.
// It returns the bundle ID and root.
func testCreateBundle(t *testing.T, token, censusID string, processIDs [][]byte) (string, string) {
	c := qt.New(t)

	// Convert process IDs to hex strings
	hexProcessIDs := make([]string, len(processIDs))
	for i, pid := range processIDs {
		hexProcessIDs[i] = hex.EncodeToString(pid)
	}

	// Create a new bundle
	bundleReq := &CreateProcessBundleRequest{
		CensusID:  censusID,
		Processes: hexProcessIDs,
	}
	resp, code := testRequest(t, http.MethodPost, token, bundleReq, "process", "bundle")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("failed to create bundle: %s", resp))

	var bundleResp CreateProcessBundleResponse
	err := json.Unmarshal(resp, &bundleResp)
	c.Assert(err, qt.IsNil)
	c.Assert(bundleResp.URI, qt.Not(qt.Equals), "", qt.Commentf("bundle URI is empty"))
	c.Assert(bundleResp.Root, qt.Not(qt.Equals), "", qt.Commentf("bundle root is empty"))

	// Extract the bundle ID from the URI
	bundleURI := bundleResp.URI
	bundleIDStr := bundleURI[len(bundleURI)-len(censusID):]

	t.Logf("Created bundle with ID: %s and Root: %s", bundleIDStr, bundleResp.Root)
	return bundleIDStr, bundleResp.Root
}

// testCSPAuthenticate performs the CSP authentication flow for a participant.
// It returns the verified auth token.
func testCSPAuthenticate(t *testing.T, bundleID, participantID, email string) internal.HexBytes {
	c := qt.New(t)

	// Step 1: Initiate authentication (auth/0)
	authReq := &handlers.AuthRequest{
		ParticipantNo: participantID,
		Email:         email,
	}
	resp, code := testRequest(t, http.MethodPost, "", authReq, "process", "bundle", bundleID, "auth", "0")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("failed to initiate auth: %s", resp))

	var authResp handlers.AuthResponse
	err := json.Unmarshal(resp, &authResp)
	c.Assert(err, qt.IsNil)
	c.Assert(authResp.AuthToken, qt.Not(qt.Equals), "", qt.Commentf("auth token is empty"))

	t.Logf("Received auth token: %s", authResp.AuthToken.String())

	// Step 2: Get the OTP code from the email with retries
	var mailBody string
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
	resp, code = testRequest(t, http.MethodPost, "", authChallengeReq, "process", "bundle", bundleID, "auth", "1")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("failed to verify auth: %s", resp))

	var verifyResp handlers.AuthResponse
	err = json.Unmarshal(resp, &verifyResp)
	c.Assert(err, qt.IsNil)
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
	resp, code := testRequest(t, http.MethodPost, "", signReq, "process", "bundle", bundleID, "sign")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("failed to sign: %s", resp))

	var signResp handlers.AuthResponse
	err := json.Unmarshal(resp, &signResp)
	c.Assert(err, qt.IsNil)
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
	processID internal.HexBytes, proof *models.Proof, votePackage []byte) []byte {

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

// testGenerateTestParticipants generates a list of test participants.
// It returns the list of participants.
func testGenerateTestParticipants(count int) []OrgParticipant {
	participants := make([]OrgParticipant, count)
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("P%03d", i+1)
		participants[i] = OrgParticipant{
			ParticipantNo: id,
			Name:          fmt.Sprintf("Test User %d", i+1),
			Email:         fmt.Sprintf("%s@example.com", id),
			Phone:         fmt.Sprintf("+346123456%02d", i+1),
		}
	}
	return participants
}
