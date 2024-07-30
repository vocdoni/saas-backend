package api

import (
	"bytes"
	"encoding/json"
	"net/http"
)

// refresh handles the refresh request. It returns a new JWT token.
func (a *API) refreshTokenHandler(w http.ResponseWriter, r *http.Request) {
	// Retrieve the user identifier from the HTTP header
	userID := r.Header.Get("X-User-Id")
	if userID == "" {
		ErrUnauthorized.Write(w)
		return
	}
	// Generate a new token with the user name as the subject
	res, err := a.buildLoginResponse(userID)
	if err != nil {
		ErrGenericInternalServerError.Write(w)
		return
	}
	// Send the token back to the user
	httpWriteJSON(w, res)
}

// login handles the login request. It returns a JWT token if the login is successful.
func (a *API) authLoginHandler(w http.ResponseWriter, r *http.Request) {
	// Get the user name from the request body
	loginInfo := &Login{}
	if err := json.NewDecoder(r.Body).Decode(loginInfo); err != nil {
		ErrMalformedBody.Write(w)
		return
	}
	// Retrieve the user from the database
	if _, ok := userMapForTest[loginInfo.Email]; !ok {
		ErrUnauthorized.Write(w)
		return
	}
	// Check the password
	if !bytes.Equal(userMapForTest[loginInfo.Email], hashPassword(loginInfo.Password)) {
		ErrUnauthorized.Write(w)
		return
	}
	// Generate a new token with the user name as the subject
	res, err := a.buildLoginResponse(loginInfo.Email)
	if err != nil {
		ErrGenericInternalServerError.Write(w)
		return
	}
	// Send the token back to the user
	httpWriteJSON(w, res)
}
