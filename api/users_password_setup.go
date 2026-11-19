package api

import (
	"net/http"
	"time"

	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
	"go.vocdoni.io/dvote/log"
)

// requestPasswordSetupHandler godoc
//
//	@Summary		Request a password setup code
//	@Description	Mail a code that lets the authenticated account, which has no password (it signs in with
//	@Description	an OAuth provider), set its first password via POST /users/me/password/setup/verify. A
//	@Description	still-valid code is resent rather than replaced; a new send is accepted once per OTP
//	@Description	cooldown. The email links to the web app page /account/password/setup?code=<code>.
//	@Tags			users
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{string}	string			"OK"
//	@Failure		400	{object}	errors.Error	"Code sent recently, or no mail service"
//	@Failure		401	{object}	errors.Error	"Unauthorized"
//	@Failure		409	{object}	errors.Error	"User already has a password"
//	@Failure		500	{object}	errors.Error	"Internal server error"
//	@Router			/users/me/password/setup [post]
func (a *API) requestPasswordSetupHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	if user.Password != "" {
		errors.ErrUserAlreadyHasPassword.Write(w)
		return
	}
	// the code proves ownership of the mailbox, so without a mail service it could never arrive
	if a.mail == nil {
		errors.ErrNotSupported.With("password setup needs the mail service").Write(w)
		return
	}
	// the session is authenticated, so unlike recovery a blocked send can be refused explicitly
	if err := a.mailUserCode(r.Context(), user, db.CodeTypePasswordSetup,
		mailtemplates.PasswordSetupNotification); err != nil {
		if errors.Is(err, errCodeSendBlocked) {
			errors.ErrVerificationMaxAttempts.With("a password setup code was sent recently, retry later").Write(w)
			return
		}
		log.Warnw("could not send password setup code", "error", err)
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	apicommon.HTTPWriteOK(w)
}

// verifyPasswordSetupHandler godoc
//
//	@Summary		Set the first password with a setup code
//	@Description	Set the first password of the authenticated account, which has no password, with the code
//	@Description	mailed by POST /users/me/password/setup. On success every existing session is revoked and
//	@Description	a fresh token is returned.
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.UserPasswordSetup	true	"Setup code and new password"
//	@Success		200		{object}	apicommon.LoginResponse
//	@Failure		400		{object}	errors.Error	"Invalid input data or max attempts reached"
//	@Failure		401		{object}	errors.Error	"Unauthorized, code mismatch or code expired"
//	@Failure		409		{object}	errors.Error	"User already has a password"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Failure		503		{object}	errors.Error	"Server busy, retry after the Retry-After delay"
//	@Router			/users/me/password/setup/verify [post]
func (a *API) verifyPasswordSetupHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	if user.Password != "" {
		errors.ErrUserAlreadyHasPassword.Write(w)
		return
	}
	setup := &apicommon.UserPasswordSetup{}
	if apiErr := apicommon.DecodeCappedJSON(w, r, setup, maxCredentialsBodyBytes); apiErr != nil {
		apiErr.Write(w)
		return
	}
	if len(setup.NewPassword) < 8 {
		errors.ErrPasswordTooShort.Write(w)
		return
	}
	userVerification, err := a.db.UserVerificationCode(user, db.CodeTypePasswordSetup)
	if err != nil {
		if err != db.ErrNotFound {
			log.Warnw("could not get password setup code", "error", err)
		}
		errors.ErrUnauthorized.Write(w)
		return
	}
	if userVerification.Expiration.Before(time.Now()) {
		errors.ErrVerificationCodeExpired.Write(w)
		return
	}
	// bound brute force: atomically spend one attempt before comparing, failing closed once the
	// per-code cap is reached (same guard as password reset and email change)
	recorded, err := a.db.VerificationCodeCheckAndAddAttempt(user, db.CodeTypePasswordSetup,
		apicommon.VerificationCodeMaxAttempts)
	if err != nil {
		if err != db.ErrNotFound {
			log.Warnw("could not record password setup attempt", "error", err)
		}
		errors.ErrUnauthorized.Write(w)
		return
	}
	if !recorded {
		errors.ErrVerificationMaxAttempts.Write(w)
		return
	}
	code, err := internal.OpenToken(userVerification.SealedCode, user.Email, a.secret)
	if err != nil {
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	if code != setup.Code {
		errors.ErrUnauthorized.With("code mismatch").Write(w)
		return
	}
	// hash before consuming: a busy server refuses the hash, and that must leave the code usable
	newPassword, ok := hashPassword(w, r, setup.NewPassword)
	if !ok {
		return
	}
	// consume the exact code before writing so it is single use even under concurrent requests
	if err := a.db.ConsumeVerificationCode(user, db.CodeTypePasswordSetup, userVerification.SealedCode); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			errors.ErrUnauthorized.Write(w)
			return
		}
		log.Warnw("could not consume password setup code", "error", err)
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	// the write only matches while no password is stored, closing the race between concurrent
	// setups; the same update bumps the session version, revoking every older session
	if err := a.db.SetUserInitialPassword(user.ID, newPassword); err != nil {
		if errors.Is(err, db.ErrConflict) {
			errors.ErrUserAlreadyHasPassword.Write(w)
			return
		}
		log.Warnw("could not set initial user password", "error", err)
		errors.ErrGenericInternalServerError.Write(w)
		return
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
