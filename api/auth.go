package api

import (
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/vocdoni/saas-backend/db"
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
	hPassword := hashPassword(loginInfo.Password)
	if hex.EncodeToString(hPassword) != user.Password {
		ErrUnauthorized.Write(w)
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
