package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.vocdoni.io/dvote/log"
)

// Supported OAuth providers
const (
	OAuthProviderGoogle   = "google"
	OAuthProviderGitHub   = "github"
	OAuthProviderFacebook = "facebook"
)

// validOAuthProviders is the list of supported OAuth providers
var validOAuthProviders = []string{
	OAuthProviderGoogle,
	OAuthProviderGitHub,
	OAuthProviderFacebook,
}

// isValidOAuthProvider checks if the provider is supported
func isValidOAuthProvider(provider string) bool {
	for _, p := range validOAuthProviders {
		if p == provider {
			return true
		}
	}
	return false
}

// refreshTokenHandler godoc
//
//	@Summary		Refresh JWT token
//	@Description	Refresh the JWT token for an authenticated user
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	apicommon.LoginResponse
//	@Failure		401	{object}	errors.Error
//	@Router			/auth/refresh [post]
func (a *API) refreshTokenHandler(w http.ResponseWriter, r *http.Request) {
	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// generate a new token with the user name as the subject
	res, err := a.buildLoginResponse(user.Email)
	if err != nil {
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	// send the token back to the user
	apicommon.HTTPWriteJSON(w, res)
}

// authLoginHandler godoc
//
//	@Summary		Login to get a JWT token
//	@Description	Authenticate a user and get a JWT token
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			request	body		apicommon.UserInfo	true	"Login credentials"
//	@Success		200		{object}	apicommon.LoginResponse
//	@Failure		400		{object}	errors.Error
//	@Failure		401		{object}	errors.Error
//	@Failure		500		{object}	errors.Error
//	@Failure		503		{object}	errors.Error	"Server busy, retry after the Retry-After delay"
//	@Router			/auth/login [post]
func (a *API) authLoginHandler(w http.ResponseWriter, r *http.Request) {
	// get the user info from the request body
	loginInfo := &apicommon.UserInfo{}
	if apiErr := apicommon.DecodeCappedJSON(w, r, loginInfo, maxCredentialsBodyBytes); apiErr != nil {
		apiErr.Write(w)
		return
	}
	// get the user information from the database by email
	user, err := a.db.UserByEmail(loginInfo.Email)
	if err != nil && err != db.ErrNotFound {
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	// hash the password even when the user does not exist, so the response time does not
	// reveal which emails are registered
	pass, ok := hashPassword(w, r, loginInfo.Password)
	if !ok {
		return
	}
	if user == nil || pass != user.Password {
		errors.ErrInvalidLoginCredentials.Write(w)
		return
	}
	// check if the user is verified
	if !user.Verified {
		errors.ErrUserNoVerified.Write(w)
		return
	}
	// generate a new token with the user name as the subject
	res, err := a.buildLoginResponse(loginInfo.Email)
	if err != nil {
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	// send the token back to the user
	apicommon.HTTPWriteJSON(w, res)
}

// organizationAddressesHandler godoc
//
//	@Summary		Get a list of addresses the user belongs to
//	@Description	Get the list of organization addresses the user belongs to
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	apicommon.OrganizationAddresses
//	@Failure		401	{object}	errors.Error
//	@Failure		404	{object}	errors.Error	"No organizations found"
//	@Router			/auth/addresses [get]
//
// organizationAddressesHandler returns the list of addresses of the
// organizations to which the authenticated user belongs
func (*API) organizationAddressesHandler(w http.ResponseWriter, r *http.Request) {
	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// check if the user has organizations
	if len(user.Organizations) == 0 {
		errors.ErrNoOrganizations.Write(w)
		return
	}
	// get the user organizations information from the database if any
	userAddresses := &apicommon.OrganizationAddresses{
		Addresses: []common.Address{},
	}
	// get the addresses of the organizations where the user has write access
	for _, org := range user.Organizations {
		userAddresses.Addresses = append(userAddresses.Addresses, org.Address)
	}
	// write the response back to the user
	apicommon.HTTPWriteJSON(w, userAddresses)
}

// oauthLoginHandler godoc
//
//	@Summary		Login using OAuth service or classic credentials
//	@Description	Register or authenticate a user (OAuth or email/password) and return a JWT token
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			request	body		apicommon.OAuthLoginRequest	true	"Login credentials; see description for details"
//	@Success		200		{object}	apicommon.OAuthLoginResponse
//	@Failure		400		{object}	errors.Error
//	@Failure		401		{object}	errors.Error
//	@Failure		500		{object}	errors.Error
//	@Failure		503		{object}	errors.Error	"Server busy, retry after the Retry-After delay"
//	@Router			/oauth/login [post]
func (a *API) oauthLoginHandler(w http.ResponseWriter, r *http.Request) {
	// get the user info from the request body
	loginInfo := &apicommon.OAuthLoginRequest{}
	if apiErr := apicommon.DecodeCappedJSON(w, r, loginInfo, maxCredentialsBodyBytes); apiErr != nil {
		apiErr.Write(w)
		return
	}
	// validate provider
	if !isValidOAuthProvider(loginInfo.Provider) {
		errors.ErrInvalidOAuthProvider.Write(w)
		return
	}
	// get the user information from the database by email
	user, err := a.db.UserByEmail(loginInfo.Email)
	if err != nil && err != db.ErrNotFound {
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	res := &apicommon.OAuthLoginResponse{}
	now := time.Now()
	// if the user doesn't exist, do oauth verification and on success create the new user
	if err == db.ErrNotFound {
		// Register the user
		// extract from the external signature the user pubkey and verify matches the provided one
		if err := account.VerifySignature(loginInfo.OAuthSignature, loginInfo.UserOAuthSignature, loginInfo.Address); err != nil {
			errors.ErrUnauthorized.WithErr(err).Write(w)
			return
		}
		// fetch oauth service pubkey or address and verify the internal signature
		result, err := a.oauthServiceAddress(r.Context())
		if err != nil {
			errors.ErrOAuthServerConnectionFailed.WithErr(err).Write(w)
			return
		}

		// verify the signature of the oauth service
		if err := account.VerifySignature(loginInfo.Email, loginInfo.OAuthSignature, result.Address); err != nil {
			errors.ErrUnauthorized.WithErr(err).Write(w)
			return
		}

		signatureHash, ok := hashPassword(w, r, loginInfo.UserOAuthSignature)
		if !ok {
			return
		}
		// create the new user with OAuth credentials
		user = &db.User{
			Email:     loginInfo.Email,
			FirstName: loginInfo.FirstName,
			LastName:  loginInfo.LastName,
			Password:  "", // OAuth-only users have empty password
			OAuth: map[string]db.OAuthProvider{
				loginInfo.Provider: {
					ExternalID:        loginInfo.Address,
					SignatureHash:     signatureHash,
					LinkedAt:          now,
					LastAuthenticated: now,
				},
			},
			Verified: true,
		}
		if _, err := a.db.SetUser(user); err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		res.Registered = true
	} else {
		// Login existing user
		// check that the user has OAuth credentials for this provider
		if user.OAuth == nil {
			user.OAuth = make(map[string]db.OAuthProvider)
		}
		oauthProvider, exists := user.OAuth[loginInfo.Provider]
		if !exists {
			errors.ErrNonOauthAccount.Write(w)
			return
		}
		// verify the signature hash matches
		pass, ok := hashPassword(w, r, loginInfo.UserOAuthSignature)
		if !ok {
			return
		}
		if pass != oauthProvider.SignatureHash {
			errors.ErrUnauthorized.Write(w)
			return
		}
		// update last authenticated timestamp
		oauthProvider.LastAuthenticated = now
		user.OAuth[loginInfo.Provider] = oauthProvider
		if _, err := a.db.SetUser(user); err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
	}
	// generate a new token with the user name as the subject
	login, err := a.buildLoginResponse(loginInfo.Email)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	res.Token = login.Token
	res.Expirity = login.Expirity
	// send the token back to the user
	apicommon.HTTPWriteJSON(w, res)
}

// oauthLinkHandler godoc
//
//	@Summary		Link OAuth provider to account
//	@Description	Link an OAuth provider to an existing authenticated account
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.OAuthLinkRequest	true	"OAuth link information"
//	@Success		200		{string}	string						"OK"
//	@Failure		400		{object}	errors.Error				"Invalid provider or provider already linked"
//	@Failure		401		{object}	errors.Error				"Unauthorized or signature verification failed"
//	@Failure		500		{object}	errors.Error				"Internal server error"
//	@Failure		503		{object}	errors.Error				"Server busy, retry after the Retry-After delay"
//	@Router			/auth/oauth [post]
func (a *API) oauthLinkHandler(w http.ResponseWriter, r *http.Request) {
	// get the authenticated user from context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// get the link info from the request body
	linkInfo := &apicommon.OAuthLinkRequest{}
	if err := json.NewDecoder(r.Body).Decode(linkInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	// validate provider
	if !isValidOAuthProvider(linkInfo.Provider) {
		errors.ErrInvalidOAuthProvider.Write(w)
		return
	}
	// check if provider is already linked to this user
	if user.OAuth == nil {
		user.OAuth = make(map[string]db.OAuthProvider)
	}
	if _, exists := user.OAuth[linkInfo.Provider]; exists {
		errors.ErrProviderAlreadyLinkedToThisAccount.Write(w)
		return
	}
	// verify OAuth signatures
	// first verify the user's signature on the OAuth signature
	if err := account.VerifySignature(linkInfo.OAuthSignature, linkInfo.UserOAuthSignature, linkInfo.Address); err != nil {
		errors.ErrUnauthorized.WithErr(err).Write(w)
		return
	}
	// fetch oauth service address and verify the OAuth service signature
	result, err := a.oauthServiceAddress(r.Context())
	if err != nil {
		errors.ErrOAuthServerConnectionFailed.WithErr(err).Write(w)
		return
	}
	// verify the signature of the oauth service on the user's email
	if err := account.VerifySignature(user.Email, linkInfo.OAuthSignature, result.Address); err != nil {
		errors.ErrUnauthorized.WithErr(err).Write(w)
		return
	}
	// ensure this provider+externalID is not already linked to another account
	existingUser, err := a.db.UserByOAuthProviderExternalID(linkInfo.Provider, linkInfo.Address)
	if err != nil && err != db.ErrNotFound {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if err == nil && existingUser.ID != user.ID {
		errors.ErrProviderAlreadyLinkedToAnotherAccount.Write(w)
		return
	}
	// all checks passed, link the provider
	signatureHash, ok := hashPassword(w, r, linkInfo.UserOAuthSignature)
	if !ok {
		return
	}
	now := time.Now()
	user.OAuth[linkInfo.Provider] = db.OAuthProvider{
		ExternalID:        linkInfo.Address,
		SignatureHash:     signatureHash,
		LinkedAt:          now,
		LastAuthenticated: now,
	}
	// save the updated user
	if _, err := a.db.SetUser(user); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	apicommon.HTTPWriteOK(w)
}

// oauthUnlinkHandler godoc
//
//	@Summary		Unlink OAuth provider from account
//	@Description	Unlink an OAuth provider from an authenticated account. Cannot unlink the last authentication method.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			provider	path		string			true	"OAuth provider name (google, github, facebook)"
//	@Success		200			{string}	string			"OK"
//	@Failure		400			{object}	errors.Error	"Invalid provider, provider not linked, or cannot unlink last auth method"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/auth/oauth/{provider} [delete]
func (a *API) oauthUnlinkHandler(w http.ResponseWriter, r *http.Request) {
	// get the authenticated user from context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// get the provider from the URL path
	provider := chi.URLParam(r, "provider")
	if provider == "" {
		errors.ErrMalformedURLParam.With("provider parameter is required").Write(w)
		return
	}
	// validate provider
	if !isValidOAuthProvider(provider) {
		errors.ErrInvalidOAuthProvider.Write(w)
		return
	}
	// check if provider is linked to this user
	if user.OAuth == nil {
		user.OAuth = make(map[string]db.OAuthProvider)
	}
	if _, exists := user.OAuth[provider]; !exists {
		errors.ErrProviderNotLinked.Write(w)
		return
	}
	// security check: prevent unlinking the last authentication method
	// user must have either a password or at least one other OAuth provider
	hasPassword := user.Password != ""
	hasOtherProviders := len(user.OAuth) > 1
	if !hasPassword && !hasOtherProviders {
		errors.ErrCannotUnlinkLastAuthMethod.Write(w)
		return
	}
	// all checks passed, unlink the provider
	delete(user.OAuth, provider)
	// save the updated user
	if _, err := a.db.SetUser(user); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	apicommon.HTTPWriteOK(w)
}

// oauthRequestTimeout bounds the calls to the OAuth service: the default client has no timeout, so
// a stalled service would hold the login handlers (and their request throttle slots) indefinitely.
const oauthRequestTimeout = 10 * time.Second

// maxOAuthResponseBytes caps the OAuth service response, which is a single address.
const maxOAuthResponseBytes = 64 << 10

// oauthServiceAddress fetches the address the OAuth service signs with.
func (a *API) oauthServiceAddress(ctx context.Context) (*apicommon.OAuthServiceAddressResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/api/info/getAddress", a.oauthServiceURL), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("could not build the oauth service request: %w", err)
	}
	// reuse the default client's transport (tests intercept it), only adding the timeout
	client := &http.Client{Transport: http.DefaultClient.Transport, Timeout: oauthRequestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach the oauth service: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Warnw("could not close the oauth service response body", "error", err)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oauth service returned status %d", resp.StatusCode)
	}
	result := &apicommon.OAuthServiceAddressResponse{}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxOAuthResponseBytes)).Decode(result); err != nil {
		return nil, fmt.Errorf("could not decode the oauth service response: %w", err)
	}
	return result, nil
}
