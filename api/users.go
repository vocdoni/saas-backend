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
	// check the first name is not empty
	if userInfo.FirstName == "" {
		ErrMalformedBody.Withf("first name is empty").Write(w)
		return
	}
	// check the last name is not empty
	if userInfo.LastName == "" {
		ErrMalformedBody.Withf("last name is empty").Write(w)
		return
	}
	// hash the password
	hPassword := hashPassword(userInfo.Password)
	// add the user to the database
	userID, err := a.db.SetUser(&db.User{
		Email:     userInfo.Email,
		FirstName: userInfo.FirstName,
		LastName:  userInfo.LastName,
		Password:  hex.EncodeToString(hPassword),
	})
	if err != nil {
		log.Warnw("could not create user", "error", err)
		ErrGenericInternalServerError.Write(w)
		return
	}
	// compose the new user and send the verification code
	newUser := &db.User{
		ID:        userID,
		Email:     userInfo.Email,
		FirstName: userInfo.FirstName,
		LastName:  userInfo.LastName,
	}
	if err := a.sendUserCode(r.Context(), newUser, db.CodeTypeAccountVerification); err != nil {
		log.Warnw("could not send verification code", "error", err)
		ErrGenericInternalServerError.Write(w)
		return
	}
	// send the token back to the user
	httpWriteOK(w)
}

// verifyUserAccountHandler handles the request to verify the user account. It
// requires the user email and the verification code to be provided. If the
// verification code is correct, the user account is verified and a new token is
// generated and sent back to the user. If the verification code is incorrect,
// an error is returned.
func (a *API) verifyUserAccountHandler(w http.ResponseWriter, r *http.Request) {
	verification := &UserVerification{}
	if err := json.NewDecoder(r.Body).Decode(verification); err != nil {
		ErrMalformedBody.Write(w)
		return
	}
	hashCode := hashVerificationCode(verification.Email, verification.Code)
	user, err := a.db.UserByVerificationCode(hashCode, db.CodeTypeAccountVerification)
	if err != nil {
		if err == db.ErrNotFound {
			ErrUnauthorized.Write(w)
			return
		}
		ErrGenericInternalServerError.Write(w)
		return
	}
	if err := a.db.VerifyUserAccount(user); err != nil {
		ErrGenericInternalServerError.Write(w)
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

// userInfoHandler handles the request to get the information of the current
// authenticated user.
func (a *API) userInfoHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok {
		ErrUnauthorized.Write(w)
		return
	}
	// get the user organizations information from the database if any
	userOrgs := make([]*UserOrganization, 0)
	for _, orgInfo := range user.Organizations {
		org, parent, err := a.db.Organization(orgInfo.Address, true)
		if err != nil {
			if err == db.ErrNotFound {
				continue
			}
			ErrGenericInternalServerError.Write(w)
			return
		}
		userOrgs = append(userOrgs, &UserOrganization{
			Role:         string(orgInfo.Role),
			Organization: organizationFromDB(org, parent),
		})
	}
	// return the user information
	httpWriteJSON(w, UserInfo{
		Email:         user.Email,
		FirstName:     user.FirstName,
		LastName:      user.LastName,
		Verified:      user.Verified,
		Organizations: userOrgs,
	})
}

// updateUserInfoHandler handles the request to update the information of the
// current authenticated user.
func (a *API) updateUserInfoHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok {
		ErrUnauthorized.Write(w)
		return
	}
	userInfo := &UserInfo{}
	if err := json.NewDecoder(r.Body).Decode(userInfo); err != nil {
		ErrMalformedBody.Write(w)
		return
	}
	// create a flag to check if the user information has changed and needs to
	// be updated and store the current email to check if it has changed
	// specifically
	updateUser := false
	currentEmail := user.Email
	// check the email is correct format if it is not empty
	if userInfo.Email != "" {
		if !isEmailValid(userInfo.Email) {
			ErrEmailMalformed.Write(w)
			return
		}
		// update the user email and set the flag to true to update the user
		// info
		user.Email = userInfo.Email
		updateUser = true
	}
	// check the first name is not empty
	if userInfo.FirstName != "" {
		// update the user first name and set the flag to true to update the
		// user info
		user.FirstName = userInfo.FirstName
		updateUser = true
	}
	// check the last name is not empty
	if userInfo.LastName != "" {
		// update the user last name and set the flag to true to update the
		// user info
		user.LastName = userInfo.LastName
		updateUser = true
	}
	// update the user information if needed
	if updateUser {
		if _, err := a.db.SetUser(user); err != nil {
			log.Warnw("could not update user", "error", err)
			ErrGenericInternalServerError.Write(w)
			return
		}
		// if user email has changed, update the creator email in the
		// organizations where the user is creator
		if user.Email != currentEmail {
			if err := a.db.ReplaceCreatorEmail(currentEmail, user.Email); err != nil {
				// revert the user update if the creator email update fails
				user.Email = currentEmail
				if _, err := a.db.SetUser(user); err != nil {
					log.Warnw("could not revert user update", "error", err)
				}
				// return an error
				ErrGenericInternalServerError.Write(w)
				return
			}
		}
	}
	// generate a new token with the new user email as the subject
	res, err := a.buildLoginResponse(user.Email)
	if err != nil {
		ErrGenericInternalServerError.Write(w)
		return
	}
	httpWriteJSON(w, res)
}

// updateUserPasswordHandler handles the request to update the password of the
// current authenticated user. It requires the old password to be provided to
// compare it with the stored one before updating the password to the new one.
func (a *API) updateUserPasswordHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok {
		ErrUnauthorized.Write(w)
		return
	}
	userPasswords := &UserPasswordUpdate{}
	if err := json.NewDecoder(r.Body).Decode(userPasswords); err != nil {
		ErrMalformedBody.Write(w)
		return
	}
	// check the password is correct format
	if len(userPasswords.NewPassword) < 8 {
		ErrPasswordTooShort.Write(w)
		return
	}
	// hash the password the old password to compare it with the stored one
	hOldPassword := hex.EncodeToString(hashPassword(userPasswords.OldPassword))
	if hOldPassword != user.Password {
		ErrUnauthorized.Withf("old password does not match").Write(w)
		return
	}
	// hash and update the new password
	user.Password = hex.EncodeToString(hashPassword(userPasswords.NewPassword))
	if _, err := a.db.SetUser(user); err != nil {
		log.Warnw("could not update user password", "error", err)
		ErrGenericInternalServerError.Write(w)
		return
	}
	httpWriteOK(w)
}

// recoveryUserPasswordHandler handles the request to recover the password of a
// user. It requires the user email to be provided. If the email is correct, a
// new verification code is generated and sent to the user email. If the email
// is incorrect, an error is returned.
func (a *API) recoverUserPasswordHandler(w http.ResponseWriter, r *http.Request) {
	// get the user info from the request body
	userInfo := &UserInfo{}
	if err := json.NewDecoder(r.Body).Decode(userInfo); err != nil {
		ErrMalformedBody.Write(w)
		return
	}
	// get the user information from the database by email
	user, err := a.db.UserByEmail(userInfo.Email)
	if err != nil {
		if err == db.ErrNotFound {
			ErrUnauthorized.Write(w)
			return
		}
		ErrGenericInternalServerError.Write(w)
		return
	}
	// check the user is verified
	if !user.Verified {
		ErrUnauthorized.With("user not verified").Write(w)
		return
	}
	// generate a new verification code
	if err := a.sendUserCode(r.Context(), user, db.CodeTypePasswordReset); err != nil {
		log.Warnw("could not send verification code", "error", err)
		ErrGenericInternalServerError.Write(w)
		return
	}
	httpWriteOK(w)
}

// resetUserPasswordHandler handles the request to reset the password of a user.
// It requires the user email, the verification code and the new password to be
// provided. If the verification code is correct, the user password is updated
// to the new one. If the verification code is incorrect, an error is returned.
func (a *API) resetUserPasswordHandler(w http.ResponseWriter, r *http.Request) {
	userPasswords := &UserPasswordReset{}
	if err := json.NewDecoder(r.Body).Decode(userPasswords); err != nil {
		ErrMalformedBody.Write(w)
		return
	}
	// check the password is correct format
	if len(userPasswords.NewPassword) < 8 {
		ErrPasswordTooShort.Write(w)
		return
	}
	// get the user information from the database by the verification code
	hashCode := hashVerificationCode(userPasswords.Email, userPasswords.Code)
	user, err := a.db.UserByVerificationCode(hashCode, db.CodeTypePasswordReset)
	if err != nil {
		if err == db.ErrNotFound {
			ErrUnauthorized.Write(w)
			return
		}
		ErrGenericInternalServerError.Write(w)
		return
	}
	// hash and update the new password
	user.Password = hex.EncodeToString(hashPassword(userPasswords.NewPassword))
	if _, err := a.db.SetUser(user); err != nil {
		log.Warnw("could not update user password", "error", err)
		ErrGenericInternalServerError.Write(w)
		return
	}
	httpWriteOK(w)
}
