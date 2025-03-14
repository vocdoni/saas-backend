package api

import (
	"encoding/json"
	"net/http"

	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
)

// refreshTokenHandler godoc
// @Summary Refresh JWT token
// @Description Refresh the JWT token for an authenticated user
// @Tags auth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 200 {object} apicommon.LoginResponse
// @Failure 401 {object} errors.Error
// @Router /auth/refresh [post]
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
	apicommon.HttpWriteJSON(w, res)
}

// authLoginHandler godoc
// @Summary Login to get a JWT token
// @Description Authenticate a user and get a JWT token
// @Tags auth
// @Accept json
// @Produce json
// @Param request body apicommon.UserInfo true "Login credentials"
// @Success 200 {object} apicommon.LoginResponse
// @Failure 400 {object} errors.Error
// @Failure 401 {object} errors.Error
// @Router /auth/login [post]
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
	apicommon.HttpWriteJSON(w, res)
}

// writableOrganizationAddressesHandler godoc
// @Summary Get writable organization addresses
// @Description Get the list of organization addresses where the user has write access
// @Tags auth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 200 {object} apicommon.OrganizationAddresses
// @Failure 401 {object} errors.Error
// @Failure 404 {object} errors.Error "No organizations found"
// @Router /auth/addresses [get]
func (a *API) writableOrganizationAddressesHandler(w http.ResponseWriter, r *http.Request) {
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
	apicommon.HttpWriteJSON(w, userAddresses)
}
