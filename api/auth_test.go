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
	"github.com/vocdoni/saas-backend/errors"
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
	invalidBodyResp := requestAndParseWithAssertCode[errors.Error](http.StatusBadRequest, t, http.MethodPost, "", "invalid body",
		oauthLoginEndpoint)
	c.Assert(invalidBodyResp.Code, qt.Equals, errors.ErrMalformedBody.Code)

	// Test with invalid provider
	email := fmt.Sprintf("oauth-user-%d@test.com", internal.RandomInt(10000))

	invalidProviderReq := &apicommon.OAuthLoginRequest{
		Email:              email,
		FirstName:          "Test",
		LastName:           "User",
		Provider:           "invalid-provider",
		OAuthSignature:     "some-signature",
		UserOAuthSignature: "some-user-signature",
		Address:            userAddress,
	}

	invalidProviderResp := requestAndParseWithAssertCode[errors.Error](
		http.StatusBadRequest,
		t,
		http.MethodPost,
		"",
		invalidProviderReq,
		oauthLoginEndpoint,
	)
	c.Assert(invalidProviderResp.Code, qt.Equals, errors.ErrInvalidOAuthProvider.Code)

	// Test with invalid OAuth signature
	invalidOAuthSignature := "invalid-signature"
	userOAuthSignatureBytes, err := userSigner.SignEthereum([]byte(invalidOAuthSignature))
	c.Assert(err, qt.IsNil)

	invalidLoginReq := &apicommon.OAuthLoginRequest{
		Email:              email,
		FirstName:          "Test",
		LastName:           "User",
		Provider:           "google",
		OAuthSignature:     invalidOAuthSignature,
		UserOAuthSignature: hex.EncodeToString(userOAuthSignatureBytes),
		Address:            userAddress,
	}

	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "", invalidLoginReq, oauthLoginEndpoint)

	// Test with valid OAuth signature but invalid user signature
	oauthSignatureBytes, err := oauthSigner.SignEthereum([]byte(email))
	c.Assert(err, qt.IsNil)
	oauthSignatureHex := hex.EncodeToString(oauthSignatureBytes)

	invalidUserLoginReq := &apicommon.OAuthLoginRequest{
		Email:              email,
		FirstName:          "Test",
		LastName:           "User",
		Provider:           "google",
		OAuthSignature:     oauthSignatureHex,
		UserOAuthSignature: "invalid-user-signature",
		Address:            userAddress,
	}

	_, code := testRequest(t, http.MethodPost, "", invalidUserLoginReq, oauthLoginEndpoint)
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test with valid signatures for a new user (should create the user)
	userOAuthSignatureBytes, err = userSigner.SignEthereum([]byte(oauthSignatureHex))
	c.Assert(err, qt.IsNil)

	validLoginReq := &apicommon.OAuthLoginRequest{
		Email:              email,
		FirstName:          "Test",
		LastName:           "User",
		Provider:           "google",
		OAuthSignature:     oauthSignatureHex,
		UserOAuthSignature: hex.EncodeToString(userOAuthSignatureBytes),
		Address:            userAddress,
	}

	resp, code := testRequest(t, http.MethodPost, "", validLoginReq, oauthLoginEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Verify the response contains a valid token
	var loginResp apicommon.OAuthLoginResponse
	err = json.Unmarshal(resp, &loginResp)
	c.Assert(err, qt.IsNil)
	c.Assert(loginResp.Token, qt.Not(qt.Equals), "")
	// Verify the user is created in the database
	c.Assert(loginResp.Registered, qt.IsTrue)

	// Test login with the same user again (should authenticate the existing user)
	resp, code = testRequest(t, http.MethodPost, "", validLoginReq, oauthLoginEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Verify the response contains a valid token
	err = json.Unmarshal(resp, &loginResp)
	c.Assert(err, qt.IsNil)
	c.Assert(loginResp.Token, qt.Not(qt.Equals), "")
	// User should not be registered again
	c.Assert(loginResp.Registered, qt.IsFalse)

	// Test with invalid user password (wrong UserOAuthSignature for existing user)
	invalidExistingUserReq := &apicommon.OAuthLoginRequest{
		Email:              email,
		FirstName:          "Test",
		LastName:           "User",
		Provider:           "google",
		OAuthSignature:     oauthSignatureHex,
		UserOAuthSignature: "wrong-signature",
		Address:            userAddress,
	}

	_, code = testRequest(t, http.MethodPost, "", invalidExistingUserReq, oauthLoginEndpoint)
	c.Assert(code, qt.Equals, http.StatusUnauthorized)
}

func TestOAuthLinkUnlinkHandler(t *testing.T) {
	c := qt.New(t)

	// Create a signer for the OAuth service
	oauthSigner := ethereum.NewSignKeys()
	err := oauthSigner.Generate()
	c.Assert(err, qt.IsNil)
	oauthAddress := oauthSigner.Address().Hex()

	// Create a mock OAuth service server
	mockOAuthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/getAddress" {
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

	// Set up HTTP client with mock transport
	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{
		Transport: &mockTransport{
			mockURL: mockOAuthServer.URL,
		},
	}
	defer func() { http.DefaultClient = originalClient }()

	// Create a regular user with password (not OAuth)
	email := fmt.Sprintf("link-test-%d@test.com", internal.RandomInt(10000))
	userInfo := &apicommon.UserInfo{
		Email:     email,
		Password:  "password123",
		FirstName: "Link",
		LastName:  "Test",
	}
	_, code := testRequest(t, http.MethodPost, "", userInfo, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)

	// Verify the user to allow login
	user, err := testDB.UserByEmail(email)
	c.Assert(err, qt.IsNil)
	err = testDB.VerifyUserAccount(user)
	c.Assert(err, qt.IsNil)

	// Login to get a token
	loginResp := requestAndParse[apicommon.LoginResponse](t, http.MethodPost, "", userInfo, authLoginEndpoint)
	token := loginResp.Token

	// Create a signer for linking OAuth
	userSigner := ethereum.NewSignKeys()
	err = userSigner.Generate()
	c.Assert(err, qt.IsNil)
	userAddress := userSigner.Address().Hex()

	// Test linking a Google OAuth provider
	oauthSignatureBytes, err := oauthSigner.SignEthereum([]byte(email))
	c.Assert(err, qt.IsNil)
	oauthSignatureHex := hex.EncodeToString(oauthSignatureBytes)

	userOAuthSignatureBytes, err := userSigner.SignEthereum([]byte(oauthSignatureHex))
	c.Assert(err, qt.IsNil)

	linkReq := &apicommon.OAuthLinkRequest{
		Provider:           "google",
		OAuthSignature:     oauthSignatureHex,
		UserOAuthSignature: hex.EncodeToString(userOAuthSignatureBytes),
		Address:            userAddress,
	}

	// Link the provider
	_, code = testRequest(t, http.MethodPost, token, linkReq, oauthLinkEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("failed to link Google provider"))

	// Verify the provider was linked
	user, err = testDB.UserByEmail(email)
	c.Assert(err, qt.IsNil)
	c.Assert(user.OAuth, qt.HasLen, 1)
	c.Assert(user.OAuth["google"].ExternalID, qt.Equals, userAddress)

	// Test linking the same provider+externalID to a different user (should fail)
	email2 := fmt.Sprintf("link-test-2-%d@test.com", internal.RandomInt(10000))
	userInfo2 := &apicommon.UserInfo{
		Email:     email2,
		Password:  "password123",
		FirstName: "Link",
		LastName:  "Test",
	}
	_, code = testRequest(t, http.MethodPost, "", userInfo2, usersEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)

	user2, err := testDB.UserByEmail(email2)
	c.Assert(err, qt.IsNil)
	err = testDB.VerifyUserAccount(user2)
	c.Assert(err, qt.IsNil)

	loginResp2 := requestAndParse[apicommon.LoginResponse](t, http.MethodPost, "", userInfo2, authLoginEndpoint)
	token2 := loginResp2.Token

	oauthSignatureBytesForUser2, err := oauthSigner.SignEthereum([]byte(email2))
	c.Assert(err, qt.IsNil)
	oauthSignatureHexForUser2 := hex.EncodeToString(oauthSignatureBytesForUser2)

	userOAuthSignatureBytesForUser2, err := userSigner.SignEthereum([]byte(oauthSignatureHexForUser2))
	c.Assert(err, qt.IsNil)

	linkReqForUser2 := &apicommon.OAuthLinkRequest{
		Provider:           "google",
		OAuthSignature:     oauthSignatureHexForUser2,
		UserOAuthSignature: hex.EncodeToString(userOAuthSignatureBytesForUser2),
		Address:            userAddress,
	}

	_, code = testRequest(t, http.MethodPost, token2, linkReqForUser2, oauthLinkEndpoint)
	c.Assert(
		code,
		qt.Equals,
		http.StatusBadRequest,
		qt.Commentf("should not allow linking same provider+externalID to another user"),
	)

	// Test linking the same provider again (should fail)
	_, code = testRequest(t, http.MethodPost, token, linkReq, oauthLinkEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest, qt.Commentf("should not allow linking same provider twice"))

	// Test linking a second provider (GitHub)
	userSigner2 := ethereum.NewSignKeys()
	err = userSigner2.Generate()
	c.Assert(err, qt.IsNil)
	userAddress2 := userSigner2.Address().Hex()

	oauthSignatureBytes2, err := oauthSigner.SignEthereum([]byte(email))
	c.Assert(err, qt.IsNil)
	oauthSignatureHex2 := hex.EncodeToString(oauthSignatureBytes2)

	userOAuthSignatureBytes2, err := userSigner2.SignEthereum([]byte(oauthSignatureHex2))
	c.Assert(err, qt.IsNil)

	linkReq2 := &apicommon.OAuthLinkRequest{
		Provider:           "github",
		OAuthSignature:     oauthSignatureHex2,
		UserOAuthSignature: hex.EncodeToString(userOAuthSignatureBytes2),
		Address:            userAddress2,
	}

	_, code = testRequest(t, http.MethodPost, token, linkReq2, oauthLinkEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("failed to link GitHub provider"))

	// Verify both providers are linked
	user, err = testDB.UserByEmail(email)
	c.Assert(err, qt.IsNil)
	c.Assert(user.OAuth, qt.HasLen, 2)

	// Test unlinking with invalid provider
	invalidUnlinkEndpoint := "/auth/oauth/invalid-provider"
	_, code = testRequest(t, http.MethodDelete, token, nil, invalidUnlinkEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)

	// Test unlinking a provider that isn't linked
	notLinkedEndpoint := "/auth/oauth/facebook"
	_, code = testRequest(t, http.MethodDelete, token, nil, notLinkedEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)

	// Test unlinking Google provider (should succeed)
	unlinkGoogleEndpoint := "/auth/oauth/google"
	_, code = testRequest(t, http.MethodDelete, token, nil, unlinkGoogleEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)

	// Verify Google was unlinked
	user, err = testDB.UserByEmail(email)
	c.Assert(err, qt.IsNil)
	c.Assert(user.OAuth, qt.HasLen, 1)
	_, hasGoogle := user.OAuth["google"]
	c.Assert(hasGoogle, qt.IsFalse)

	// Test unlinking the last OAuth provider when user has password (should succeed)
	unlinkGithubEndpoint := "/auth/oauth/github"
	_, code = testRequest(t, http.MethodDelete, token, nil, unlinkGithubEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)

	// Verify GitHub was unlinked
	user, err = testDB.UserByEmail(email)
	c.Assert(err, qt.IsNil)
	c.Assert(user.OAuth, qt.HasLen, 0)

	// Now test the security constraint: create an OAuth-only user and try to unlink the last provider
	oauthOnlyEmail := fmt.Sprintf("oauth-only-%d@test.com", internal.RandomInt(10000))
	oauthSigner3 := ethereum.NewSignKeys()
	err = oauthSigner3.Generate()
	c.Assert(err, qt.IsNil)
	userAddress3 := oauthSigner3.Address().Hex()

	oauthSignatureBytes3, err := oauthSigner.SignEthereum([]byte(oauthOnlyEmail))
	c.Assert(err, qt.IsNil)
	oauthSignatureHex3 := hex.EncodeToString(oauthSignatureBytes3)

	userOAuthSignatureBytes3, err := oauthSigner3.SignEthereum([]byte(oauthSignatureHex3))
	c.Assert(err, qt.IsNil)

	oauthOnlyLoginReq := &apicommon.OAuthLoginRequest{
		Email:              oauthOnlyEmail,
		FirstName:          "OAuth",
		LastName:           "Only",
		Provider:           "google",
		OAuthSignature:     oauthSignatureHex3,
		UserOAuthSignature: hex.EncodeToString(userOAuthSignatureBytes3),
		Address:            userAddress3,
	}

	resp, code := testRequest(t, http.MethodPost, "", oauthOnlyLoginReq, oauthLoginEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)

	var oauthLoginResp apicommon.OAuthLoginResponse
	err = json.Unmarshal(resp, &oauthLoginResp)
	c.Assert(err, qt.IsNil)
	oauthOnlyToken := oauthLoginResp.Token

	// Try to unlink the only OAuth provider (should fail)
	unlinkOnlyProviderEndpoint := "/auth/oauth/google"
	_, code = testRequest(t, http.MethodDelete, oauthOnlyToken, nil, unlinkOnlyProviderEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest, qt.Commentf("should not allow unlinking last auth method"))

	// Verify the provider is still linked
	user, err = testDB.UserByEmail(oauthOnlyEmail)
	c.Assert(err, qt.IsNil)
	c.Assert(user.OAuth, qt.HasLen, 1)
}
