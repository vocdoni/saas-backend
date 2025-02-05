package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/log"
)

// refresh handles the refresh request. It returns a new JWT token.
func (a *API) refreshTokenHandler(w http.ResponseWriter, r *http.Request) {
	// get the user from the request context
	user, ok := userFromContext(r.Context())
	if !ok {
		ErrUnauthorized.Write(w)
		return
	}
	// generate a new token with the user name as the subject
	res, err := a.buildLoginResponse(user.Email)
	if err != nil {
		ErrGenericInternalServerError.Write(w)
		return
	}
	// send the token back to the user
	httpWriteJSON(w, res)
}

// login handles the login request. It returns a JWT token if the login is successful.
func (a *API) authLoginHandler(w http.ResponseWriter, r *http.Request) {
	// het the user info from the request body
	loginInfo := &UserInfo{}
	if err := json.NewDecoder(r.Body).Decode(loginInfo); err != nil {
		ErrMalformedBody.Write(w)
		return
	}
	// get the user information from the database by email
	user, err := a.db.UserByEmail(loginInfo.Email)
	if err != nil {
		if err == db.ErrNotFound {
			ErrUnauthorized.Write(w)
			return
		}
		ErrGenericInternalServerError.Write(w)
		return
	}
	// check the password
	if pass := internal.HexHashPassword(passwordSalt, loginInfo.Password); pass != user.Password {
		ErrUnauthorized.Write(w)
		return
	}
	// check if the user is verified
	if !user.Verified {
		ErrUserNoVerified.Write(w)
		return
	}
	// generate a new token with the user name as the subject
	res, err := a.buildLoginResponse(loginInfo.Email)
	if err != nil {
		ErrGenericInternalServerError.Write(w)
		return
	}
	// send the token back to the user
	httpWriteJSON(w, res)
}

// login handles the login request. It returns a JWT token if the login is successful.
func (a *API) oauthLoginHandler(w http.ResponseWriter, r *http.Request) {
	// het the user info from the request body
	loginInfo := &OauthLogin{}
	if err := json.NewDecoder(r.Body).Decode(loginInfo); err != nil {
		ErrMalformedBody.Write(w)
		return
	}
	log.Debugf("%v", loginInfo)
	// get the user information from the database by email
	user, err := a.db.UserByEmail(loginInfo.Email)
	if err != nil && err != db.ErrNotFound {
		ErrGenericInternalServerError.Write(w)
		return
	}
	// if the user doesn't exist, do oauth verifictaion and on success create the new user
	if err == db.ErrNotFound {
		// 1. extract from the external signature the user publey and verify matches the provided one
		if err := account.VerifySignature(loginInfo.OauthSignature, loginInfo.UserOauthSignature, loginInfo.Address); err != nil {
			ErrUnauthorized.WithErr(err).Write(w)
			return
		}
		// 2. fetch oath service pubkey or address and verify the internal signature
		resp, err := http.Get(fmt.Sprintf("%s/getAddress", OauthServiceURL))
		if err != nil {
			// TODO create new error for connection with the external service
			ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				// handle the error, for example log it
				fmt.Println("Error closing response body:", err)
			}
		}()
		var result OauthServiceResposne
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			// TODO create new error for connection with the external service
			ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		if err := account.VerifySignature(loginInfo.Email, loginInfo.OauthSignature, result.Address); err != nil {
			ErrUnauthorized.WithErr(err).Write(w)
			return
		}
		// genareate the new user and password and store it in the database
		user = &db.User{
			Email:    loginInfo.Email,
			Password: internal.HexHashPassword(passwordSalt, loginInfo.UserOauthSignature),
			Verified: true,
		}
		if _, err := a.db.SetUser(user); err != nil {
			ErrGenericInternalServerError.WithErr(err).Write(w)
		}
	} else {
		// check that the address generated password matches the one in the database
		// check the password
		if pass := internal.HexHashPassword(passwordSalt, loginInfo.UserOauthSignature); pass != user.Password {
			ErrUnauthorized.Write(w)
			return
		}
	}
	// generate a new token with the user name as the subject
	res, err := a.buildLoginResponse(loginInfo.Email)
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	// send the token back to the user
	httpWriteJSON(w, res)
}

// writableOrganizationAddressesHandler returns the list of addresses of the
// organizations where the user has write access.
func (a *API) writableOrganizationAddressesHandler(w http.ResponseWriter, r *http.Request) {
	// get the user from the request context
	user, ok := userFromContext(r.Context())
	if !ok {
		ErrUnauthorized.Write(w)
		return
	}
	// check if the user has organizations
	if len(user.Organizations) == 0 {
		ErrNoOrganizations.Write(w)
		return
	}
	// get the user organizations information from the database if any
	userAddresses := &OrganizationAddresses{
		Addresses: []string{},
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
	httpWriteJSON(w, userAddresses)
}
