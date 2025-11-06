package api

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
	"go.vocdoni.io/dvote/log"
)

// registerHandler godoc
//
//	@Summary		Register a new user
//	@Description	Register a new user with email, password, and personal information
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Param			request	body		apicommon.UserInfo	true	"User registration information"
//	@Success		200		{string}	string				"OK"
//	@Failure		400		{object}	errors.Error		"Invalid input data"
//	@Failure		409		{object}	errors.Error		"User already exists"
//	@Failure		500		{object}	errors.Error		"Internal server error"
//	@Router			/users [post]
func (a *API) registerHandler(w http.ResponseWriter, r *http.Request) {
	userInfo := &apicommon.UserInfo{}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	if err := json.Unmarshal(body, userInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	// check the email is correct format
	if !internal.ValidEmail(userInfo.Email) {
		errors.ErrEmailMalformed.Write(w)
		return
	}
	// check the password is correct format
	if len(userInfo.Password) < 8 {
		errors.ErrPasswordTooShort.Write(w)
		return
	}
	// check the first name is not empty
	if userInfo.FirstName == "" {
		errors.ErrMalformedBody.Withf("first name is empty").Write(w)
		return
	}
	// check the last name is not empty
	if userInfo.LastName == "" {
		errors.ErrMalformedBody.Withf("last name is empty").Write(w)
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
			errors.ErrDuplicateConflict.With("user already exists").Write(w)
			return
		}
		log.Warnw("could not create user", "error", err)
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
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
		errors.ErrGenericInternalServerError.Write(w)
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
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	// send the token back to the user
	apicommon.HTTPWriteOK(w)
}

// verifyUserAccountHandler godoc
//
//	@Summary		Verify user account
//	@Description	Verify a user account with the verification code
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Param			request	body		apicommon.UserVerification	true	"Verification information"
//	@Success		200		{object}	apicommon.LoginResponse
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		409		{object}	errors.Error	"User already verified"
//	@Failure		410		{object}	errors.Error	"Verification code expired"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/users/verify [post]
func (a *API) verifyUserAccountHandler(w http.ResponseWriter, r *http.Request) {
	verification := &apicommon.UserVerification{}
	if err := json.NewDecoder(r.Body).Decode(verification); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	// check the email and verification code are not empty only if the mail
	// service is available
	if a.mail != nil && (verification.Code == "" || verification.Email == "") {
		errors.ErrInvalidUserData.With("no verification code or email provided").Write(w)
		return
	}
	// get the user information from the database by email
	user, err := a.db.UserByEmail(verification.Email)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrUnauthorized.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	// check the user is not already verified
	if user.Verified {
		errors.ErrUserAlreadyVerified.Write(w)
		return
	}
	// get the verification code from the database
	code, err := a.db.UserVerificationCode(user, db.CodeTypeVerifyAccount)
	if err != nil {
		if err != db.ErrNotFound {
			log.Warnw("could not get verification code", "error", err)
		}
		errors.ErrUnauthorized.Write(w)
		return
	}
	// check the verification code is not expired
	if code.Expiration.Before(time.Now()) {
		errors.ErrVerificationCodeExpired.Write(w)
		return
	}
	// check the verification code is correct
	hashCode := internal.HashVerificationCode(verification.Email, verification.Code)
	if code.Code != hashCode {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// verify the user account if the current verification code is valid and
	// matches with the provided one
	if err := a.db.VerifyUserAccount(user); err != nil {
		errors.ErrGenericInternalServerError.Write(w)
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

// userVerificationCodeInfoHandler godoc
//
//	@Summary		Get verification code information
//	@Description	Get information about a user's verification code
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Param			email	query		string	true	"User email"
//	@Success		200		{object}	apicommon.UserVerification
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		404		{object}	errors.Error	"User not found"
//	@Failure		409		{object}	errors.Error	"User already verified"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/users/verify/code [get]
func (a *API) userVerificationCodeInfoHandler(w http.ResponseWriter, r *http.Request) {
	// get the user email of the user from the request query
	userEmail := r.URL.Query().Get("email")
	// check the email is not empty
	if userEmail == "" {
		errors.ErrInvalidUserData.With("no email provided").Write(w)
		return
	}
	var err error
	var user *db.User
	// get the user information from the database by email
	user, err = a.db.UserByEmail(userEmail)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrUserNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	// check if the user is already verified
	if user.Verified {
		errors.ErrUserAlreadyVerified.Write(w)
		return
	}
	// get the verification code from the database
	code, err := a.db.UserVerificationCode(user, db.CodeTypeVerifyAccount)
	if err != nil {
		if err != db.ErrNotFound {
			log.Warnw("could not get verification code", "error", err)
		}
		errors.ErrUnauthorized.Write(w)
		return
	}
	// return the verification code information
	apicommon.HTTPWriteJSON(w, apicommon.UserVerification{
		Email:      user.Email,
		Expiration: code.Expiration,
		Valid:      code.Expiration.After(time.Now()),
	})
}

// resendUserVerificationCodeHandler godoc
//
//	@Summary		Resend verification code
//	@Description	Resend a verification code to the user's email
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Param			request	body		apicommon.UserVerification	true	"User email information"
//	@Success		200		{string}	string						"OK"
//	@Failure		400		{object}	errors.Error				"Invalid input data, user already verified, or max resend attempts reached"
//	@Failure		401		{object}	errors.Error				"Unauthorized"
//	@Failure		500		{object}	errors.Error				"Internal server error"
//	@Router			/users/verify/code [post]
func (a *API) resendUserVerificationCodeHandler(w http.ResponseWriter, r *http.Request) {
	verification := &apicommon.UserVerification{}
	if err := json.NewDecoder(r.Body).Decode(verification); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	// check the email is not empty
	if verification.Email == "" {
		errors.ErrInvalidUserData.With("no email provided").Write(w)
		return
	}
	// get the user information from the database by email
	user, err := a.db.UserByEmail(verification.Email)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrUnauthorized.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	// check the user is not already verified
	if user.Verified {
		errors.ErrUserAlreadyVerified.Write(w)
		return
	}
	// get the verification code from the database
	code, err := a.db.UserVerificationCode(user, db.CodeTypeVerifyAccount)
	if err != nil {
		if err != db.ErrNotFound {
			log.Warnw("could not get verification code", "error", err)
		}
		errors.ErrUnauthorized.Write(w)
		return
	}
	// if the verification code is not expired
	if code.Expiration.After(time.Now()) {
		// check if the maximum number of attempts has been reached for resending
		if code.Attempts >= apicommon.VerificationCodeMaxAttempts {
			errors.ErrVerificationMaxAttempts.WithData(apicommon.UserVerification{
				Expiration: code.Expiration,
			}).Write(w)
			return
		}
		link, err := a.generateVerificationLink(user, code.Code)
		if err != nil {
			log.Warnw("could not generate verification link", "error", err)
			errors.ErrGenericInternalServerError.Write(w)
			return
		}
		// resend the existing verification code
		if err := a.sendMail(r.Context(), user.Email, mailtemplates.VerifyAccountNotification,
			struct {
				Code string
				Link string
			}{code.Code, link},
		); err != nil {
			log.Warnw("could not resend verification code", "error", err)
			errors.ErrGenericInternalServerError.Write(w)
			return
		}
		if err = a.db.VerificationCodeIncrementAttempts(code.Code, db.CodeTypeVerifyAccount); err != nil {
			log.Warnw("could not increment verification code attempts", "error", err)
			errors.ErrGenericInternalServerError.Write(w)
			return
		}
		// return the verification code information
		apicommon.HTTPWriteJSON(w, apicommon.UserVerification{
			Expiration: code.Expiration,
		})
		return
	}

	// generate a new verification code
	newCode, link, err := a.generateVerificationCodeAndLink(user, db.CodeTypeVerifyAccount)
	if err != nil {
		log.Warnw("could not generate verification code", "error", err)
		errors.ErrGenericInternalServerError.Write(w)
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
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	apicommon.HTTPWriteOK(w)
}

// userInfoHandler godoc
//
//	@Summary		Get user information
//	@Description	Get information about the authenticated user
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	apicommon.UserInfo
//	@Failure		401	{object}	errors.Error	"Unauthorized"
//	@Failure		500	{object}	errors.Error	"Internal server error"
//	@Router			/users/me [get]
func (a *API) userInfoHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// get the user organizations information from the database if any
	userOrgs := make([]*apicommon.UserOrganization, 0)
	for _, orgInfo := range user.Organizations {
		org, parent, err := a.db.OrganizationWithParent(orgInfo.Address)
		if err != nil {
			if err == db.ErrNotFound {
				continue
			}
			errors.ErrGenericInternalServerError.Write(w)
			return
		}
		userOrgs = append(userOrgs, &apicommon.UserOrganization{
			Role:         string(orgInfo.Role),
			Organization: apicommon.OrganizationFromDB(org, parent),
		})
	}
	// return the user information
	apicommon.HTTPWriteJSON(w, apicommon.UserInfo{
		ID:            user.ID,
		Email:         user.Email,
		FirstName:     user.FirstName,
		LastName:      user.LastName,
		Verified:      user.Verified,
		Organizations: userOrgs,
	})
}

// updateUserInfoHandler godoc
//
//	@Summary		Update user information
//	@Description	Update information for the authenticated user
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.UserInfo	true	"User information to update"
//	@Success		200		{object}	apicommon.LoginResponse
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/users/me [put]
func (a *API) updateUserInfoHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	userInfo := &apicommon.UserInfo{}
	if err := json.NewDecoder(r.Body).Decode(userInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
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
			errors.ErrEmailMalformed.Write(w)
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
			errors.ErrGenericInternalServerError.Write(w)
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
				errors.ErrGenericInternalServerError.Write(w)
				return
			}
		}
	}
	// generate a new token with the new user email as the subject
	res, err := a.buildLoginResponse(user.Email)
	if err != nil {
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	apicommon.HTTPWriteJSON(w, res)
}

// updateUserPasswordHandler godoc
//
//	@Summary		Update user password
//	@Description	Update the password for the authenticated user
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.UserPasswordUpdate	true	"Password update information"
//	@Success		200		{string}	string							"OK"
//	@Failure		400		{object}	errors.Error					"Invalid input data"
//	@Failure		401		{object}	errors.Error					"Unauthorized or old password does not match"
//	@Failure		500		{object}	errors.Error					"Internal server error"
//	@Router			/users/password [put]
func (a *API) updateUserPasswordHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	userPasswords := &apicommon.UserPasswordUpdate{}
	if err := json.NewDecoder(r.Body).Decode(userPasswords); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	// check the password is correct format
	if len(userPasswords.NewPassword) < 8 {
		errors.ErrPasswordTooShort.Write(w)
		return
	}
	// hash the password the old password to compare it with the stored one
	hOldPassword := internal.HexHashPassword(passwordSalt, userPasswords.OldPassword)
	if hOldPassword != user.Password {
		errors.ErrUnauthorized.Withf("old password does not match").Write(w)
		return
	}
	// hash and update the new password
	user.Password = internal.HexHashPassword(passwordSalt, userPasswords.NewPassword)
	if _, err := a.db.SetUser(user); err != nil {
		log.Warnw("could not update user password", "error", err)
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	apicommon.HTTPWriteOK(w)
}

// recoverUserPasswordHandler godoc
//
//	@Summary		Recover user password
//	@Description	Request a password recovery code for a user
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Param			request	body		apicommon.UserInfo	true	"User email information"
//	@Success		200		{string}	string				"OK"
//	@Failure		400		{object}	errors.Error		"Invalid input data"
//	@Failure		500		{object}	errors.Error		"Internal server error"
//	@Router			/users/recovery [post]
func (a *API) recoverUserPasswordHandler(w http.ResponseWriter, r *http.Request) {
	// get the user info from the request body
	userInfo := &apicommon.UserInfo{}
	if err := json.NewDecoder(r.Body).Decode(userInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	// get the user information from the database by email
	user, err := a.db.UserByEmail(userInfo.Email)
	if err != nil {
		if err == db.ErrNotFound {
			// do not return an error if the user is not found to avoid
			// information leakage
			apicommon.HTTPWriteOK(w)
			return
		}
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	// check the user is verified
	if user.Verified {
		// generate a new verification code
		code, link, err := a.generateVerificationCodeAndLink(user, db.CodeTypePasswordReset)
		if err != nil {
			log.Warnw("could not generate verification code", "error", err)
			errors.ErrGenericInternalServerError.Write(w)
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
			errors.ErrGenericInternalServerError.Write(w)
			return
		}
	}
	apicommon.HTTPWriteOK(w)
}

// resetUserPasswordHandler godoc
//
//	@Summary		Reset user password
//	@Description	Reset a user's password using a verification code
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Param			request	body		apicommon.UserPasswordReset	true	"Password reset information"
//	@Success		200		{string}	string						"OK"
//	@Failure		400		{object}	errors.Error				"Invalid input data"
//	@Failure		401		{object}	errors.Error				"Unauthorized or invalid verification code"
//	@Failure		500		{object}	errors.Error				"Internal server error"
//	@Router			/users/reset [post]
func (a *API) resetUserPasswordHandler(w http.ResponseWriter, r *http.Request) {
	userPasswords := &apicommon.UserPasswordReset{}
	if err := json.NewDecoder(r.Body).Decode(userPasswords); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	// check the password is correct format
	if len(userPasswords.NewPassword) < 8 {
		errors.ErrPasswordTooShort.Write(w)
		return
	}
	// get the user information from the database by the verification code
	hashCode := internal.HashVerificationCode(userPasswords.Email, userPasswords.Code)
	user, err := a.db.UserByVerificationCode(hashCode, db.CodeTypePasswordReset)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrUnauthorized.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	// hash and update the new password
	user.Password = internal.HexHashPassword(passwordSalt, userPasswords.NewPassword)
	if _, err := a.db.SetUser(user); err != nil {
		log.Warnw("could not update user password", "error", err)
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	apicommon.HTTPWriteOK(w)
}
