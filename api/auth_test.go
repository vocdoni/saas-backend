package api

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/crypto/ethereum"
)

// testOAuthServiceURL is the URL of the OAuth service used for testing

// These regexes are used to extract tokens from responses
var (
	authTokenRgx *regexp.Regexp
)

func init() {
	// Initialize any regex patterns or other configurations needed for auth tests
	authTokenRgx = regexp.MustCompile(`"token":"([^"]+)"`)
}

// mockTransport is a custom http.RoundTripper that intercepts requests to the OAuth service
type mockTransport struct {
	mockURL string
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// If the request is to the OAuth service, redirect it to our mock server
	if req.URL.String() == fmt.Sprintf("%s/api/info/getAddress", testOAuthServiceURL) {
		newURL := fmt.Sprintf("%s/getAddress", m.mockURL)
		newReq, err := http.NewRequest(req.Method, newURL, req.Body)
		if err != nil {
			return nil, err
		}
		newReq.Header = req.Header
		return http.DefaultTransport.RoundTrip(newReq)
	}
	// Otherwise, use the default transport
	return http.DefaultTransport.RoundTrip(req)
}

func TestOAuthLoginHandler(t *testing.T) {
	c := qt.New(t)

	// Create a signer for the OAuth service
	oauthSigner := ethereum.NewSignKeys()
	err := oauthSigner.Generate()
	c.Assert(err, qt.IsNil)
	oauthAddress := oauthSigner.Address().Hex()

	// Create a mock OAuth service server that will respond to the getAddress request
	mockOAuthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/getAddress" {
			// Return the OAuth service address
			resp := apicommon.OAuthServiceAddressResponse{
				Address: oauthAddress,
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				http.Error(w, "Failed to encode response", http.StatusInternalServerError)
				return
			}
		} else {
			http.NotFound(w, r)
		}
	}))
	defer mockOAuthServer.Close()

	// Create a signer for the user
	userSigner := ethereum.NewSignKeys()
	err = userSigner.Generate()
	c.Assert(err, qt.IsNil)
	userAddress := userSigner.Address().Hex()

	// Save the original client to restore it later
	originalClient := http.DefaultClient

	// Set up HTTP client with mock transport
	http.DefaultClient = &http.Client{
		Transport: &mockTransport{
			mockURL: mockOAuthServer.URL,
		},
	}
	defer func() { http.DefaultClient = originalClient }()

	// Test invalid body
	resp, code := testRequest(t, http.MethodPost, "", "invalid body", oauthLoginEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(string(resp), qt.Contains, "40004") // ErrMalformedBody

	// Test with invalid OAuth signature
	email := fmt.Sprintf("oauth-user-%d@test.com", internal.RandomInt(10000))
	invalidOAuthSignature := "invalid-signature"
	userOAuthSignatureBytes, err := userSigner.SignEthereum([]byte(invalidOAuthSignature))
	c.Assert(err, qt.IsNil)

	invalidLoginReq := &apicommon.OAuthLoginRequest{
		Email:              email,
		FirstName:          "Test",
		LastName:           "User",
		OAuthSignature:     invalidOAuthSignature,
		UserOAuthSignature: hex.EncodeToString(userOAuthSignatureBytes),
		Address:            userAddress,
	}

	_, code = testRequest(t, http.MethodPost, "", invalidLoginReq, oauthLoginEndpoint)
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test with valid OAuth signature but invalid user signature
	oauthSignatureBytes, err := oauthSigner.SignEthereum([]byte(email))
	c.Assert(err, qt.IsNil)
	oauthSignatureHex := hex.EncodeToString(oauthSignatureBytes)

	invalidUserLoginReq := &apicommon.OAuthLoginRequest{
		Email:              email,
		FirstName:          "Test",
		LastName:           "User",
		OAuthSignature:     oauthSignatureHex,
		UserOAuthSignature: "invalid-user-signature",
		Address:            userAddress,
	}

	_, code = testRequest(t, http.MethodPost, "", invalidUserLoginReq, oauthLoginEndpoint)
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test with valid signatures for a new user (should create the user)
	userOAuthSignatureBytes, err = userSigner.SignEthereum([]byte(oauthSignatureHex))
	c.Assert(err, qt.IsNil)

	validLoginReq := &apicommon.OAuthLoginRequest{
		Email:              email,
		FirstName:          "Test",
		LastName:           "User",
		OAuthSignature:     oauthSignatureHex,
		UserOAuthSignature: hex.EncodeToString(userOAuthSignatureBytes),
		Address:            userAddress,
	}

	resp, code = testRequest(t, http.MethodPost, "", validLoginReq, oauthLoginEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)

	// Verify the response contains a valid token
	var loginResp apicommon.OAuthLoginResponse
	err = json.Unmarshal(resp, &loginResp)
	c.Assert(err, qt.IsNil)
	c.Assert(loginResp.Token, qt.Not(qt.Equals), "")
	// Verify the user is created in the database
	c.Assert(loginResp.Registered, qt.Equals, true)

	// Test login with the same user again (should authenticate the existing user)
	resp, code = testRequest(t, http.MethodPost, "", validLoginReq, oauthLoginEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)

	// Verify the response contains a valid token
	err = json.Unmarshal(resp, &loginResp)
	c.Assert(err, qt.IsNil)
	c.Assert(loginResp.Token, qt.Not(qt.Equals), "")
	// User should not be registered again
	c.Assert(loginResp.Registered, qt.Equals, false)

	// Test with invalid user password (wrong UserOAuthSignature for existing user)
	invalidExistingUserReq := &apicommon.OAuthLoginRequest{
		Email:              email,
		FirstName:          "Test",
		LastName:           "User",
		OAuthSignature:     oauthSignatureHex,
		UserOAuthSignature: "wrong-signature",
		Address:            userAddress,
	}

	_, code = testRequest(t, http.MethodPost, "", invalidExistingUserReq, oauthLoginEndpoint)
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test
}
