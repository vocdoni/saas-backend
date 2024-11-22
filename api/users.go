package api

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
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
	if !internal.ValidEmail(userInfo.Email) {
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
	hPassword := internal.HexHashPassword(passwordSalt, userInfo.Password)
	// add the user to the database
	userID, err := a.db.SetUser(&db.User{
		Email:     userInfo.Email,
		FirstName: userInfo.FirstName,
		LastName:  userInfo.LastName,
		Password:  hPassword,
	})
	if err != nil {
		if err == db.ErrAlreadyExists {
			ErrDuplicateConflict.With("user already exists").Write(w)
			return
		}
		log.Warnw("could not create user", "error", err)
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	// compose the new user and send the verification code
	newUser := &db.User{
		ID:        userID,
		Email:     userInfo.Email,
		FirstName: userInfo.FirstName,
		LastName:  userInfo.LastName,
	}
	// generate a new verification code
	code, link, err := a.generateVerificationCodeAndLink(newUser, db.CodeTypeVerifyAccount)
	if err != nil {
		log.Warnw("could not generate verification code", "error", err)
		ErrGenericInternalServerError.Write(w)
		return
	}
	// send the verification mail to the user email with the verification code
	// and the verification link
	if err := a.sendMail(r.Context(), userInfo.Email,
		mailtemplates.VerifyAccountNotification, struct {
			Code string
			Link string
		}{code, link},
	); err != nil {
		log.Warnw("could not send verification code", "error", err)
		ErrGenericInternalServerError.Write(w)
		return
	}
	// send the token back to the user
	httpWriteOK(w)
}

// verifyUserAccountHandler handles the request to verify the user account. It
// requires the user email and the verification code to be provided. It checks
// if the user has not been verified yet, if the verification code is not
// expired and if the verification code is correct. If all the checks are
// correct, the user account is verified and a new token is generated and sent
// back to the user. If the user is already verified, an error is returned. If
// the verification code is expired, an error is returned. If the verification
// code is incorrect, an error is returned and the number of attempts to verify
// it is increased. If any other error occurs, a generic error is returned.
func (a *API) verifyUserAccountHandler(w http.ResponseWriter, r *http.Request) {
	verification := &UserVerification{}
	if err := json.NewDecoder(r.Body).Decode(verification); err != nil {
		ErrMalformedBody.Write(w)
		return
	}
	// check the email and verification code are not empty only if the mail
	// service is available
	if a.mail != nil && (verification.Code == "" || verification.Email == "") {
		ErrInvalidUserData.With("no verification code or email provided").Write(w)
		return
	}
	// get the user information from the database by email
	user, err := a.db.UserByEmail(verification.Email)
	if err != nil {
		if err == db.ErrNotFound {
			ErrUnauthorized.Write(w)
			return
		}
		ErrGenericInternalServerError.Write(w)
		return
	}
	// check the user is not already verified
	if user.Verified {
		ErrUserAlreadyVerified.Write(w)
		return
	}
	// get the verification code from the database
	code, err := a.db.UserVerificationCode(user, db.CodeTypeVerifyAccount)
	if err != nil {
		if err != db.ErrNotFound {
			log.Warnw("could not get verification code", "error", err)
		}
		ErrUnauthorized.Write(w)
		return
	}
	// check the verification code is not expired
	if code.Expiration.Before(time.Now()) {
		ErrVerificationCodeExpired.Write(w)
		return
	}
	// check the verification code is correct
	hashCode := internal.HashVerificationCode(verification.Email, verification.Code)
	if code.Code != hashCode {
		ErrUnauthorized.Write(w)
		return
	}
	// verify the user account if the current verification code is valid and
	// matches with the provided one
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

// userVerificationCodeInfoHandler handles the request to get the verification
// code information of a user. It requires the user email to be provided. It
// returns the user email, the verification code, the code expiration and if
// the code is valid (not expired and has not reached the maximum number of
// attempts). If the user is already verified, an error is returned. If the
// user is not found, an error is returned. If the verification code is not
// found, an error is returned. If any other error occurs, a generic error is
// returned.
func (a *API) userVerificationCodeInfoHandler(w http.ResponseWriter, r *http.Request) {
	// get the user email of the user from the request query
	userEmail := r.URL.Query().Get("email")
	// check the email is not empty
	if userEmail == "" {
		ErrInvalidUserData.With("no email provided").Write(w)
		return
	}
	var err error
	var user *db.User
	// get the user information from the database by email
	user, err = a.db.UserByEmail(userEmail)
	if err != nil {
		if err == db.ErrNotFound {
			ErrUserNotFound.Write(w)
			return
		}
		ErrGenericInternalServerError.Write(w)
		return
	}
	// check if the user is already verified
	if user.Verified {
		ErrUserAlreadyVerified.Write(w)
		return
	}
	// get the verification code from the database
	code, err := a.db.UserVerificationCode(user, db.CodeTypeVerifyAccount)
	if err != nil {
		if err != db.ErrNotFound {
			log.Warnw("could not get verification code", "error", err)
		}
		ErrUnauthorized.Write(w)
		return
	}
	// return the verification code information
	httpWriteJSON(w, UserVerification{
		Email:      user.Email,
		Expiration: code.Expiration,
		Valid:      code.Expiration.After(time.Now()),
	})
}

// resendUserVerificationCodeHandler handles the request to resend the user
// verification code. It requires the user email to be provided. If the user is
// not found, an error is returned. If the user is already verified, an error is
// returned. If the verification code is not expired, an error is returned. If
// the verification code is found and expired, a new verification code is sent
// to the user email. If any other error occurs, a generic error is returned.
func (a *API) resendUserVerificationCodeHandler(w http.ResponseWriter, r *http.Request) {
	verification := &UserVerification{}
	if err := json.NewDecoder(r.Body).Decode(verification); err != nil {
		ErrMalformedBody.Write(w)
		return
	}
	// check the email is not empty
	if verification.Email == "" {
		ErrInvalidUserData.With("no email provided").Write(w)
		return
	}
	// get the user information from the database by email
	user, err := a.db.UserByEmail(verification.Email)
	if err != nil {
		if err == db.ErrNotFound {
			ErrUnauthorized.Write(w)
			return
		}
		ErrGenericInternalServerError.Write(w)
		return
	}
	// check the user is not already verified
	if user.Verified {
		ErrUserAlreadyVerified.Write(w)
		return
	}
	// get the verification code from the database
	code, err := a.db.UserVerificationCode(user, db.CodeTypeVerifyAccount)
	if err != nil {
		if err != db.ErrNotFound {
			log.Warnw("could not get verification code", "error", err)
		}
		ErrUnauthorized.Write(w)
		return
	}
	// if the verification code is not expired, return an error
	if code.Expiration.After(time.Now()) {
		ErrVerificationCodeValid.Write(w)
		return
	}
	// generate a new verification code
	newCode, link, err := a.generateVerificationCodeAndLink(user, db.CodeTypeVerifyAccount)
	if err != nil {
		log.Warnw("could not generate verification code", "error", err)
		ErrGenericInternalServerError.Write(w)
		return
	}
	// send the verification mail to the user email with the verification code
	// and the verification link
	if err := a.sendMail(r.Context(), user.Email, mailtemplates.VerifyAccountNotification,
		struct {
			Code string
			Link string
		}{newCode, link},
	); err != nil {
		log.Warnw("could not send verification code", "error", err)
		ErrGenericInternalServerError.Write(w)
		return
	}
	httpWriteOK(w)
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
		if !internal.ValidEmail(userInfo.Email) {
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
	hOldPassword := internal.HexHashPassword(passwordSalt, userPasswords.OldPassword)
	if hOldPassword != user.Password {
		ErrUnauthorized.Withf("old password does not match").Write(w)
		return
	}
	// hash and update the new password
	user.Password = internal.HexHashPassword(passwordSalt, userPasswords.NewPassword)
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
			// do not return an error if the user is not found to avoid
			// information leakage
			httpWriteOK(w)
			return
		}
		ErrGenericInternalServerError.Write(w)
		return
	}
	// check the user is verified
	if user.Verified {
		// generate a new verification code
		code, link, err := a.generateVerificationCodeAndLink(user, db.CodeTypePasswordReset)
		if err != nil {
			log.Warnw("could not generate verification code", "error", err)
			ErrGenericInternalServerError.Write(w)
			return
		}
		// send the password reset mail to the user email with the verification
		// code and the verification link
		if err := a.sendMail(r.Context(), user.Email, mailtemplates.PasswordResetNotification,
			struct {
				Code string
				Link string
			}{code, link},
		); err != nil {
			log.Warnw("could not send reset passworod code", "error", err)
			ErrGenericInternalServerError.Write(w)
			return
		}
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
	hashCode := internal.HashVerificationCode(userPasswords.Email, userPasswords.Code)
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
	user.Password = internal.HexHashPassword(passwordSalt, userPasswords.NewPassword)
	if _, err := a.db.SetUser(user); err != nil {
		log.Warnw("could not update user password", "error", err)
		ErrGenericInternalServerError.Write(w)
		return
	}
	httpWriteOK(w)
}
