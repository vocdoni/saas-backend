package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
)

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
//	@Router			/auth/login [post]
func (a *API) authLoginHandler(w http.ResponseWriter, r *http.Request) {
	// het the user info from the request body
	loginInfo := &apicommon.UserInfo{}
	if err := json.NewDecoder(r.Body).Decode(loginInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	// get the user information from the database by email
	user, err := a.db.UserByEmail(loginInfo.Email)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrUnauthorized.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	// check the password
	if pass := internal.HexHashPassword(passwordSalt, loginInfo.Password); pass != user.Password {
		errors.ErrUnauthorized.Write(w)
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

// writableOrganizationAddressesHandler godoc
//
//	@Summary		Get writable organization addresses
//	@Description	Get the list of organization addresses where the user has write access
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	apicommon.OrganizationAddresses
//	@Failure		401	{object}	errors.Error
//	@Failure		404	{object}	errors.Error	"No organizations found"
//	@Router			/auth/addresses [get]
//
// writableOrganizationAddressesHandler returns the list of addresses of the
// organizations where the user has write access.
func (*API) writableOrganizationAddressesHandler(w http.ResponseWriter, r *http.Request) {
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
		Addresses: []internal.HexBytes{},
	}
	// get the addresses of the organizations where the user has write access
	for _, org := range user.Organizations {
		// check if the user has write access to the organization based on the
		// role of the user in the organization
		if db.HasWriteAccess(org.Role) {
			userAddresses.Addresses = append(userAddresses.Addresses, org.Address)
		}
	}
	// write the response back to the user
	apicommon.HTTPWriteJSON(w, userAddresses)
}

// oauthLoginHandler godoc
//
//	@Summary		Login using OAuth service
//	@Description	Register/Authenticate a user and get a JWT token
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			request	body		apicommon.UserInfo	true	"Login credentials"
//	@Success		200		{object}	apicommon.OAuthLoginResponse
//	@Failure		400		{object}	errors.Error
//	@Failure		401		{object}	errors.Error
//	@Failure		500		{object}	errors.Error
//	@Router			/oauth/login [post]
func (a *API) oauthLoginHandler(w http.ResponseWriter, r *http.Request) {
	// get the user info from the request body
	loginInfo := &apicommon.OAuthLoginRequest{}
	if err := json.NewDecoder(r.Body).Decode(loginInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	// get the user information from the database by email
	user, err := a.db.UserByEmail(loginInfo.Email)
	if err != nil && err != db.ErrNotFound {
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	res := &apicommon.OAuthLoginResponse{}
	// if the user doesn't exist, do oauth verification and on success create the new user
	if err == db.ErrNotFound {
		// Register the user
		// extract from the external signature the user pubkey and verify matches the provided one
		if err := account.VerifySignature(loginInfo.OAuthSignature, loginInfo.UserOAuthSignature, loginInfo.Address); err != nil {
			errors.ErrUnauthorized.WithErr(err).Write(w)
			return
		}
		// fetch oauth service pubkey or address and verify the internal signature
		resp, err := http.Get(fmt.Sprintf("%s/api/info/getAddress", a.oauthServiceURL))
		defer func() {
			if err := resp.Body.Close(); err != nil {
				// handle the error, for example log it
				fmt.Println("Error closing response body:", err)
			}
		}()
		if err != nil {
			errors.ErrOAuthServerConnectionFailed.WithErr(err).Write(w)
			return
		}
		var result apicommon.OAuthServiceAddressResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}

		// verify the signature of the oauth service
		if err := account.VerifySignature(loginInfo.Email, loginInfo.OAuthSignature, result.Address); err != nil {
			errors.ErrUnauthorized.WithErr(err).Write(w)
			return
		}

		// genareate the new user and password and store it in the database
		user = &db.User{
			Email:     loginInfo.Email,
			FirstName: loginInfo.FirstName,
			LastName:  loginInfo.LastName,
			Password:  internal.HexHashPassword(passwordSalt, loginInfo.UserOAuthSignature),
			Verified:  true,
		}
		if _, err := a.db.SetUser(user); err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		}
		res.Registered = true
	}
	// Login
	// check that the address generated password matches the one in the database
	if pass := internal.HexHashPassword(passwordSalt, loginInfo.UserOAuthSignature); pass != user.Password {
		errors.ErrUnauthorized.Write(w)
		return
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
