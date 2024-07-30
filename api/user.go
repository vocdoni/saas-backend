package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"go.vocdoni.io/dvote/log"
)

// userMapForTest is a map of user email to hashed password. It is used for testing purposes.
var userMapForTest = make(map[string][]byte)

// addUser adds a new user to the database. It returns an error if the user already exists.
func addUser(u *Register) error {
	log.Debugw("new user", "email", u.Email)
	if _, ok := userMapForTest[u.Email]; ok {
		return fmt.Errorf("user already exists")
	}
	if len(u.Password) < 8 {
		return fmt.Errorf("password too short")
	}
	userMapForTest[u.Email] = hashPassword(u.Password)
	return nil
}

// registerHandler handles the register request. It creates a new user in the database.
func (a *API) registerHandler(w http.ResponseWriter, r *http.Request) {
	userInfo := &Register{}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		ErrMalformedBody.Write(w)
		return
	}
	if err := json.Unmarshal(body, userInfo); err != nil {
		ErrMalformedBody.Write(w)
		return
	}
	// check the email is correct format
	if !isEmailValid(userInfo.Email) {
		ErrEmailMalformed.Write(w)
		return
	}
	// add the user to the database
	if err := addUser(userInfo); err != nil {
		ErrInvalidUserData.WithErr(err).Write(w)
		return
	}
	// Generate a new token with the user name as the subject
	res, err := a.buildLoginResponse(userInfo.Email)
	if err != nil {
		ErrGenericInternalServerError.Write(w)
		return
	}
	// Send the token back to the user
	httpWriteJSON(w, res)
}

// addressHandler handles the address request. It returns the Ethereum address of the user.
func (a *API) addressHandler(w http.ResponseWriter, r *http.Request) {
	// Retrieve the user identifier from the HTTP header
	userID := r.Header.Get("X-User-Id")
	if userID == "" {
		ErrUnauthorized.Write(w)
		return
	}
	// Create a signer for the user
	signer, err := signerFromUserEmail(userID)
	if err != nil {
		ErrGenericInternalServerError.Withf("could not create signer for user: %v", err).Write(w)
		return
	}
	// Send the token back to the user
	httpWriteJSON(w, UserAddressResponse{Address: signer.AddressString()})
}
