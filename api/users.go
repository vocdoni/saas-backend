package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/dvote/util"
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
//	@Failure		503		{object}	errors.Error		"Server busy, retry after the Retry-After delay"
//	@Router			/users [post]
func (a *API) registerHandler(w http.ResponseWriter, r *http.Request) {
	userInfo := &apicommon.UserInfo{}
	if apiErr := apicommon.DecodeCappedJSON(w, r, userInfo, maxCredentialsBodyBytes); apiErr != nil {
		apiErr.Write(w)
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
	// reject an already registered email before hashing, so repeated registrations of the same
	// address do not each cost a password hash (SetUser still enforces uniqueness on insert)
	if _, err := a.db.UserByEmail(userInfo.Email); err == nil {
		errors.ErrDuplicateConflict.With("user already exists").Write(w)
		return
	} else if err != db.ErrNotFound {
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	// hash the password
	hPassword, ok := hashPassword(w, r, userInfo.Password)
	if !ok {
		return
	}
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
	if err := a.sendMail(r.Context(), nil, userInfo.Email,
		mailtemplates.VerifyAccountNotification, struct {
			Code string
			Link string
		}{code, link},
		time.Now().Add(a.otpExpiry),
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
	// get the userVerification from the database
	userVerification, err := a.db.UserVerificationCode(user, db.CodeTypeVerifyAccount)
	if err != nil {
		if err != db.ErrNotFound {
			log.Warnw("could not get verification code", "error", err)
		}
		errors.ErrUnauthorized.Write(w)
		return
	}
	// check the verification code is not expired
	if userVerification.Expiration.Before(time.Now()) {
		errors.ErrVerificationCodeExpired.Write(w)
		return
	}
	// bound brute-force: atomically spend one verification attempt, failing closed once the
	// per-code cap is reached. Done before comparing the code so an exhausted code is locked
	// regardless of the submitted value (no per-guess unlimited retries).
	recorded, err := a.db.VerificationCodeCheckAndAddAttempt(user, db.CodeTypeVerifyAccount, apicommon.VerificationCodeMaxAttempts)
	if err != nil {
		if err != db.ErrNotFound {
			log.Warnw("could not record verification attempt", "error", err)
		}
		errors.ErrUnauthorized.Write(w)
		return
	}
	if !recorded {
		errors.ErrVerificationMaxAttempts.Write(w)
		return
	}
	// check the verification code is correct
	code, err := internal.OpenToken(userVerification.SealedCode, verification.Email, a.secret)
	if err != nil {
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	if code != verification.Code {
		errors.ErrUnauthorized.With("code mismatch").Write(w)
		return
	}
	// verify the user account if the current verification code is valid and
	// matches with the provided one
	if err := a.db.VerifyUserAccount(user); err != nil {
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	// generate a new token bound to the user's ID and current session version
	res, err := a.buildLoginResponse(user)
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
	// the endpoint is public and must not reveal whether an account exists or is verified:
	// every outcome (unknown email, already verified, no pending code) gets the same 200
	// response shape, with Valid=true and the expiry only when a verification challenge is
	// actually pending — which the legitimate owner knows, having just requested it
	response := apicommon.UserVerification{Email: userEmail, Valid: false}
	user, err := a.db.UserByEmail(userEmail)
	if err != nil && err != db.ErrNotFound {
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	if err == nil && !user.Verified {
		userVerification, err := a.db.UserVerificationCode(user, db.CodeTypeVerifyAccount)
		switch {
		case err == nil:
			response.Expiration = userVerification.Expiration
			response.Valid = userVerification.Expiration.After(time.Now())
		case err != db.ErrNotFound:
			log.Warnw("could not get verification code", "error", err)
			errors.ErrGenericInternalServerError.Write(w)
			return
		default:
			// no pending challenge: keep the neutral response
		}
	}
	// return the verification code information
	apicommon.HTTPWriteJSON(w, response)
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
	// get the verification userVerification from the database
	userVerification, err := a.db.UserVerificationCode(user, db.CodeTypeVerifyAccount)
	if err != nil {
		if err != db.ErrNotFound {
			log.Warnw("could not get verification code", "error", err)
		}
		errors.ErrUnauthorized.Write(w)
		return
	}
	// if the verification code is not expired, resend the same code instead of replacing it
	if userVerification.Expiration.After(time.Now()) {
		// spend one delivery from the send budget, which is separate from the guess-attempt
		// counter so resending the code never eats into the brute-force budget. The update is
		// a single conditional write, so concurrent resends cannot exceed the cap.
		sent, err := a.db.VerificationCodeTrySend(user, db.CodeTypeVerifyAccount, 0, apicommon.VerificationCodeMaxSends)
		if err != nil {
			if err != db.ErrNotFound {
				log.Warnw("could not record verification code send", "error", err)
			}
			errors.ErrUnauthorized.Write(w)
			return
		}
		if !sent {
			errors.ErrVerificationMaxAttempts.WithData(apicommon.UserVerification{
				Expiration: userVerification.Expiration,
			}).Write(w)
			return
		}
		code, err := internal.OpenToken(userVerification.SealedCode, user.Email, a.secret)
		if err != nil {
			errors.ErrGenericInternalServerError.Write(w)
			return
		}
		link, err := a.generateVerificationLink(user, db.CodeTypeVerifyAccount, code)
		if err != nil {
			log.Warnw("could not generate verification link", "error", err)
			errors.ErrGenericInternalServerError.Write(w)
			return
		}
		// resend the existing verification code
		if err := a.sendMail(r.Context(), nil, user.Email, mailtemplates.VerifyAccountNotification,
			struct {
				Code string
				Link string
			}{code, link},
			userVerification.Expiration,
		); err != nil {
			log.Warnw("could not resend verification code", "error", err)
			errors.ErrGenericInternalServerError.Write(w)
			return
		}
		// return the verification code information
		apicommon.HTTPWriteJSON(w, apicommon.UserVerification{
			Expiration: userVerification.Expiration,
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
	if err := a.sendMail(r.Context(), nil, user.Email, mailtemplates.VerifyAccountNotification,
		struct {
			Code string
			Link string
		}{newCode, link},
		time.Now().Add(a.otpExpiry),
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
		// flag integrator status per organization so multi-org users can tell
		// which of their organizations is the integrator one
		userOrgs = append(userOrgs, &apicommon.UserOrganization{
			Role:         string(orgInfo.Role),
			Organization: apicommon.OrganizationFromDB(org, parent),
			IsIntegrator: a.subscriptions.IsIntegrator(org),
		})
	}
	// extract the list of linked OAuth providers
	providers := make([]string, 0, len(user.OAuth))
	for provider := range user.OAuth {
		providers = append(providers, provider)
	}
	// return the user information
	apicommon.HTTPWriteJSON(w, apicommon.UserInfo{
		ID:            user.ID,
		Email:         user.Email,
		FirstName:     user.FirstName,
		LastName:      user.LastName,
		Verified:      user.Verified,
		HasPassword:   user.Password != "",
		Providers:     providers,
		Organizations: userOrgs,
	})
}

// updateUserInfoHandler godoc
//
//	@Summary		Update user information
//	@Description	Update information for the authenticated user. Changing the email does not take
//	@Description	effect immediately: a verification code is sent to the requested address and the
//	@Description	email is switched only once the code is confirmed via POST /users/me/email/verify.
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.UserInfo	true	"User information to update"
//	@Success		200		{object}	apicommon.LoginResponse
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		409		{object}	errors.Error	"Email already in use"
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
	// an email change must prove ownership of the new mailbox: it is recorded as a pending
	// email and only applied once the code sent to it is confirmed
	pendingEmail := ""
	if userInfo.Email != "" && userInfo.Email != user.Email {
		if !internal.ValidEmail(userInfo.Email) {
			errors.ErrEmailMalformed.Write(w)
			return
		}
		// refuse an address already bound to another account (the unique index would reject
		// the switch later anyway; failing now gives the owner a clear answer)
		if _, err := a.db.UserByEmail(userInfo.Email); err == nil {
			errors.ErrDuplicateConflict.With("email already in use").Write(w)
			return
		} else if err != db.ErrNotFound {
			errors.ErrGenericInternalServerError.Write(w)
			return
		}
		pendingEmail = userInfo.Email
	} else if userInfo.Email != "" && !internal.ValidEmail(userInfo.Email) {
		errors.ErrEmailMalformed.Write(w)
		return
	}
	// update the profile fields with a field-specific write: the user snapshot in the context
	// was loaded at authentication time, and writing it back whole could restore memberships
	// an admin revoked (or a password that changed) while this request was in flight
	if userInfo.FirstName != "" || userInfo.LastName != "" {
		if err := a.db.UpdateUserProfile(user.ID, userInfo.FirstName, userInfo.LastName); err != nil {
			log.Warnw("could not update user", "error", err)
			errors.ErrGenericInternalServerError.Write(w)
			return
		}
	}
	// store the pending email change and send the verification code to the new address
	if pendingEmail != "" {
		if err := a.sendEmailUpdateCode(r.Context(), user, pendingEmail); err != nil {
			log.Warnw("could not send email update code", "error", err)
			errors.ErrGenericInternalServerError.Write(w)
			return
		}
	}
	// the identity (user ID) is unchanged, so the current token stays valid; return a fresh
	// one anyway to keep the previous response contract
	res, err := a.buildLoginResponse(user)
	if err != nil {
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	apicommon.HTTPWriteJSON(w, res)
}

// sendEmailUpdateCode generates an email-update verification code for the user, stores it
// (sealed to the pending address) and mails it to that address. The user's email is only
// switched once the code is confirmed by updateUserEmailVerifyHandler.
func (a *API) sendEmailUpdateCode(ctx context.Context, user *db.User, pendingEmail string) error {
	// generate the code only when the mail service is available, mirroring
	// generateVerificationCodeAndLink's mocked verification for test setups
	code := ""
	if a.mail != nil {
		code = util.RandomHex(apicommon.VerificationCodeLength)
	}
	// seal the code to the pending address, so it only opens for that exact email
	sealedCode, err := internal.SealToken(code, pendingEmail, a.secret)
	if err != nil {
		return fmt.Errorf("could not seal email update code: %w", err)
	}
	expiration := time.Now().Add(a.otpExpiry)
	if err := a.db.SetVerificationCode(&db.UserVerification{
		UserID:       user.ID,
		SealedCode:   sealedCode,
		Type:         db.CodeTypeUpdateEmail,
		PendingEmail: pendingEmail,
		Expiration:   expiration,
	}); err != nil {
		return fmt.Errorf("could not store email update code: %w", err)
	}
	link, err := a.buildWebAppURL(mailtemplates.VerifyAccountNotification.WebAppURI, map[string]any{
		"email": pendingEmail,
		"code":  code,
	})
	if err != nil {
		return fmt.Errorf("could not build email update link: %w", err)
	}
	if err := a.sendMail(ctx, nil, pendingEmail, mailtemplates.VerifyAccountNotification,
		struct {
			Code string
			Link string
		}{code, link},
		expiration,
	); err != nil {
		return fmt.Errorf("could not send email update code: %w", err)
	}
	return nil
}

// updateUserEmailVerifyHandler godoc
//
//	@Summary		Confirm a pending email change
//	@Description	Confirm the email change requested via PUT /users/me with the code sent to the
//	@Description	new address. On success the account email is switched, every existing session is
//	@Description	revoked and a fresh token is returned.
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.UserVerification	true	"Verification code"
//	@Success		200		{object}	apicommon.LoginResponse
//	@Failure		400		{object}	errors.Error	"Invalid input data or max attempts reached"
//	@Failure		401		{object}	errors.Error	"Unauthorized or code mismatch"
//	@Failure		409		{object}	errors.Error	"Email already in use"
//	@Failure		410		{object}	errors.Error	"Verification code expired"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/users/me/email/verify [post]
func (a *API) updateUserEmailVerifyHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	verification := &apicommon.UserVerification{}
	if err := json.NewDecoder(r.Body).Decode(verification); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	if a.mail != nil && verification.Code == "" {
		errors.ErrInvalidUserData.With("no verification code provided").Write(w)
		return
	}
	// load the pending email change
	userVerification, err := a.db.UserVerificationCode(user, db.CodeTypeUpdateEmail)
	if err != nil {
		if err != db.ErrNotFound {
			log.Warnw("could not get email update code", "error", err)
		}
		errors.ErrUnauthorized.Write(w)
		return
	}
	// check the verification code is not expired
	if userVerification.Expiration.Before(time.Now()) {
		errors.ErrVerificationCodeExpired.Write(w)
		return
	}
	// bound brute-force: atomically spend one verification attempt, failing closed once the
	// per-code cap is reached (same guard as account verification and password reset)
	recorded, err := a.db.VerificationCodeCheckAndAddAttempt(user, db.CodeTypeUpdateEmail, apicommon.VerificationCodeMaxAttempts)
	if err != nil {
		if err != db.ErrNotFound {
			log.Warnw("could not record email update attempt", "error", err)
		}
		errors.ErrUnauthorized.Write(w)
		return
	}
	if !recorded {
		errors.ErrVerificationMaxAttempts.Write(w)
		return
	}
	// check the verification code is correct (sealed to the pending address)
	code, err := internal.OpenToken(userVerification.SealedCode, userVerification.PendingEmail, a.secret)
	if err != nil {
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	if code != verification.Code {
		errors.ErrUnauthorized.With("code mismatch").Write(w)
		return
	}
	// consume the exact code before switching the email so it can never be redeemed twice,
	// even by concurrent requests: exactly one delete wins
	if err := a.db.ConsumeVerificationCode(user, db.CodeTypeUpdateEmail, userVerification.SealedCode); err != nil {
		if err != db.ErrNotFound {
			log.Warnw("could not consume email update code", "error", err)
			errors.ErrGenericInternalServerError.Write(w)
			return
		}
		errors.ErrUnauthorized.Write(w)
		return
	}
	// switch the email; the same atomic update bumps the session version, revoking every
	// outstanding session (they were bound to an account reachable through the old address)
	if err := a.db.UpdateUserEmail(user.ID, userVerification.PendingEmail); err != nil {
		if err == db.ErrAlreadyExists {
			errors.ErrDuplicateConflict.With("email already in use").Write(w)
			return
		}
		log.Warnw("could not update user email", "error", err)
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	// keep the organizations the user created pointing at their current address. The signing key
	// no longer depends on it (it is derived from the immutable signer seed)
	if err := a.db.ReplaceCreatorEmail(user.Email, userVerification.PendingEmail); err != nil {
		log.Errorw(err, "could not update the creator email of the user's organizations")
	}
	// reload the user to mint a token for the new session version
	updated, err := a.db.User(user.ID)
	if err != nil {
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	res, err := a.buildLoginResponse(updated)
	if err != nil {
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	apicommon.HTTPWriteJSON(w, res)
}

// updateUserPasswordHandler godoc
//
//	@Summary		Update user password
//	@Description	Update the password for the authenticated user. Every existing session is revoked;
//	@Description	a fresh token for the current client is returned in the response.
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.UserPasswordUpdate	true	"Password update information"
//	@Success		200		{object}	apicommon.LoginResponse
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized or old password does not match"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Failure		503		{object}	errors.Error	"Server busy, retry after the Retry-After delay"
//	@Router			/users/password [put]
func (a *API) updateUserPasswordHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// check if user is OAuth-only (no password set)
	if user.Password == "" {
		errors.ErrOAuthUserCannotUsePasswordRecovery.Write(w)
		return
	}
	userPasswords := &apicommon.UserPasswordUpdate{}
	if apiErr := apicommon.DecodeCappedJSON(w, r, userPasswords, maxCredentialsBodyBytes); apiErr != nil {
		apiErr.Write(w)
		return
	}
	// check the password is correct format
	if len(userPasswords.NewPassword) < 8 {
		errors.ErrPasswordTooShort.Write(w)
		return
	}
	// hash the password the old password to compare it with the stored one
	hOldPassword, ok := hashPassword(w, r, userPasswords.OldPassword)
	if !ok {
		return
	}
	if hOldPassword != user.Password {
		errors.ErrUnauthorized.Withf("old password does not match").Write(w)
		return
	}
	// hash the new password
	newPassword, ok := hashPassword(w, r, userPasswords.NewPassword)
	if !ok {
		return
	}
	// field-specific write: only the password is updated, and the same atomic update bumps the
	// session version, revoking every outstanding session (including the one making this request)
	if err := a.db.UpdateUserPassword(user.ID, newPassword); err != nil {
		log.Warnw("could not update user password", "error", err)
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	// reload the user to mint a token for the new session version, so the client changing the
	// password stays logged in while every other session is revoked
	updated, err := a.db.User(user.ID)
	if err != nil {
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	res, err := a.buildLoginResponse(updated)
	if err != nil {
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	apicommon.HTTPWriteJSON(w, res)
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
//	@Router			/users/password/recovery [post]
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
	// pick the challenge the account actually needs: an unverified account gets the account
	// verification code instead, so it can complete registration and then log in
	codeType := db.CodeTypePasswordReset
	template := mailtemplates.PasswordResetNotification
	if !user.Verified {
		codeType = db.CodeTypeVerifyAccount
		template = mailtemplates.VerifyAccountNotification
	}
	// a single cooldown guards both branches: a code delivered too recently means we silently
	// skip sending another, preventing email flooding without leaking whether the account exists
	var code, link string
	expiration := time.Now().Add(a.otpExpiry)
	existing, err := a.db.UserVerificationCode(user, codeType)
	switch {
	case err == nil && time.Since(existing.LastSentAt) < a.otpCooldown:
		apicommon.HTTPWriteOK(w)
		return
	case err == nil && existing.Expiration.After(time.Now()):
		// a still-valid code exists: resend it rather than replace it, so repeated recovery
		// requests cannot invalidate the code the user is about to type. The delivery is spent
		// atomically against the send budget; when exhausted (or raced), silently skip.
		sent, err := a.db.VerificationCodeTrySend(user, codeType, a.otpCooldown, apicommon.VerificationCodeMaxSends)
		if err != nil || !sent {
			if err != nil && !errors.Is(err, db.ErrNotFound) {
				log.Warnw("could not record recovery code resend", "error", err)
			}
			apicommon.HTTPWriteOK(w)
			return
		}
		if code, err = internal.OpenToken(existing.SealedCode, user.Email, a.secret); err != nil {
			errors.ErrGenericInternalServerError.Write(w)
			return
		}
		if link, err = a.generateVerificationLink(user, codeType, code); err != nil {
			errors.ErrGenericInternalServerError.Write(w)
			return
		}
		expiration = existing.Expiration
	case err == nil || errors.Is(err, db.ErrNotFound):
		// no pending code, or only an expired one: generate and store a fresh code
		if code, link, err = a.generateVerificationCodeAndLink(user, codeType); err != nil {
			log.Warnw("could not generate verification code", "error", err)
			errors.ErrGenericInternalServerError.Write(w)
			return
		}
	default:
		// unexpected DB error: treat conservatively as cooldown-active so a transient storage
		// failure cannot be exploited to bypass rate limiting
		log.Warnw("could not check password recovery cooldown", "error", err)
		apicommon.HTTPWriteOK(w)
		return
	}
	// send the code and the verification link to the user email
	if err := a.sendMail(r.Context(), nil, user.Email, template,
		struct {
			Code string
			Link string
		}{code, link},
		expiration,
	); err != nil {
		log.Warnw("could not send recovery code", "error", err)
		errors.ErrGenericInternalServerError.Write(w)
		return
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
//	@Failure		404		{object}	errors.Error				"User not found"
//	@Failure		410		{object}	errors.Error				"Verification code expired"
//	@Failure		500		{object}	errors.Error				"Internal server error"
//	@Failure		503		{object}	errors.Error				"Server busy, retry after the Retry-After delay"
//	@Router			/users/password/reset [post]
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

	// get the user information from the database by email
	user, err := a.db.UserByEmail(userPasswords.Email)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrUserNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Write(w)
		return
	}

	userVerification, err := a.db.UserVerificationCode(user, db.CodeTypePasswordReset)
	if err != nil {
		if err != db.ErrNotFound {
			log.Warnw("could not get verification code", "error", err)
		}
		errors.ErrUnauthorized.Write(w)
		return
	}

	// check the verification code is not expired
	if userVerification.Expiration.Before(time.Now()) {
		errors.ErrVerificationCodeExpired.Write(w)
		return
	}

	// bound brute-force: atomically spend one attempt, failing closed once the per-code cap is
	// reached. Done before comparing the code so an exhausted reset code is locked regardless of
	// the submitted value, closing the online guessing vector against the short OTP.
	recorded, err := a.db.VerificationCodeCheckAndAddAttempt(user, db.CodeTypePasswordReset, apicommon.VerificationCodeMaxAttempts)
	if err != nil {
		if err != db.ErrNotFound {
			log.Warnw("could not record password reset attempt", "error", err)
		}
		errors.ErrUnauthorized.Write(w)
		return
	}
	if !recorded {
		errors.ErrVerificationMaxAttempts.Write(w)
		return
	}

	code, err := internal.OpenToken(userVerification.SealedCode, userPasswords.Email, a.secret)
	if err != nil {
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	if code != userPasswords.Code {
		errors.ErrUnauthorized.With("code mismatch").Write(w)
		return
	}

	// hash the new password before consuming the code: hashing can be refused when the server is
	// busy, and that must leave the code usable for a retry
	newPassword, ok := hashPassword(w, r, userPasswords.NewPassword)
	if !ok {
		return
	}

	// consume the exact reset code before updating the password so it is atomically single-use:
	// of two concurrent requests holding a valid code, exactly one deletion succeeds and only
	// that request resets the password. fail closed on any other error — reporting success with
	// the code still stored would leave it replayable.
	if err := a.db.ConsumeVerificationCode(user, db.CodeTypePasswordReset, userVerification.SealedCode); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			errors.ErrUnauthorized.Write(w)
			return
		}
		log.Warnw("could not consume password reset code", "error", err)
		errors.ErrGenericInternalServerError.Write(w)
		return
	}

	// field-specific password write; the same atomic update bumps the session version, revoking
	// every session minted with the old credentials
	if err := a.db.UpdateUserPassword(user.ID, newPassword); err != nil {
		log.Warnw("could not update user password", "error", err)
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	apicommon.HTTPWriteOK(w)
}
