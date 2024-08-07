package api

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"

	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/log"
)

// registerHandler handles the register request. It creates a new user in the database.
func (a *API) registerHandler(w http.ResponseWriter, r *http.Request) {
	userInfo := &UserInfo{}
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
	// check the password is correct format
	if len(userInfo.Password) < 8 {
		ErrPasswordTooShort.Write(w)
		return
	}
	// hash the password
	hPassword := hashPassword(userInfo.Password)
	// add the user to the database
	if err := a.db.SetUser(&db.User{
		Email:    userInfo.Email,
		FullName: userInfo.FullName,
		Password: hex.EncodeToString(hPassword),
	}); err != nil {
		log.Warnw("could not create user", "error", err)
		ErrGenericInternalServerError.Write(w)
		return
	}
	// generate a new token with the user name as the subject
	res, err := a.buildLoginResponse(userInfo.Email)
	if err != nil {
		ErrGenericInternalServerError.Write(w)
		return
	}
	// send the token back to the user
	httpWriteJSON(w, res)
}

// userInfoHandler handles the request to get the information of the current
// authenticated user.
func (a *API) userInfoHandler(w http.ResponseWriter, r *http.Request) {
	// retrieve the user identifier from the HTTP header
	userID := r.Header.Get("X-User-Id")
	if userID == "" {
		ErrUnauthorized.Write(w)
		return
	}
	// get user info from the database
	dbUser, err := a.db.UserByEmail(userID)
	if err != nil {
		ErrGenericInternalServerError.Write(w)
		return
	}
	// get the user organizations information from the database if any
	userOrgs := make([]*UserOrganization, 0)
	for _, orgInfo := range dbUser.Organizations {
		org, err := a.db.Organization(orgInfo.Address)
		if err != nil {
			if err == db.ErrNotFound {
				continue
			}
			ErrGenericInternalServerError.Write(w)
			return
		}
		var parent *db.Organization
		if org.Parent != "" {
			if parent, err = a.db.Organization(org.Parent); err != nil {
				ErrGenericInternalServerError.Write(w)
				return
			}
		}
		userOrgs = append(userOrgs, &UserOrganization{
			Role:         string(orgInfo.Role),
			Organization: organizationFromDB(org, parent),
		})
	}
	// return the user information
	httpWriteJSON(w, UserInfo{
		Email:         dbUser.Email,
		FullName:      dbUser.FullName,
		Organizations: userOrgs,
	})
}
