package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/go-chi/jwtauth/v5"
	"github.com/lestrrat-go/jwx/v3/jwt"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
)

// TestLegacyAndTamperedTokensRejected guards the JWT identity binding: tokens must carry the
// user's immutable numeric ID plus the current session version. Tokens minted before that change
// (email subject, no session version) and tokens with a stale session version are all rejected,
// because an email can be freed and re-registered by someone else, and a credential change must
// revoke outstanding sessions.
func TestLegacyAndTamperedTokensRejected(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()

	token := testCreateUser(t, testPass)
	me := requestAndParse[apicommon.UserInfo](t, http.MethodGet, token, nil, usersMeEndpoint)

	// read the current (valid) claims off the real token
	valid, err := jwtauth.VerifyToken(testAPI.auth, token)
	c.Assert(err, qt.IsNil)
	var subject, sessionVersion string
	c.Assert(valid.Get("userId", &subject), qt.IsNil)
	c.Assert(valid.Get("sessionVersion", &sessionVersion), qt.IsNil)

	mint := func(claims map[string]any) string {
		claims[string(jwt.ExpirationKey)] = time.Now().Add(time.Hour)
		_, minted, err := testAPI.auth.Encode(claims)
		c.Assert(err, qt.IsNil)
		return minted
	}

	// a legacy token carrying the email as subject is rejected
	legacy := mint(map[string]any{"userId": me.Email, "sessionVersion": sessionVersion})
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodGet, legacy, nil, usersMeEndpoint)

	// a token without a session version is rejected
	noVersion := mint(map[string]any{"userId": subject})
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodGet, noVersion, nil, usersMeEndpoint)

	// a token with a stale session version is rejected
	staleVersion := mint(map[string]any{"userId": subject, "sessionVersion": sessionVersion + "1"})
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodGet, staleVersion, nil, usersMeEndpoint)

	// sanity: a re-minted token with the correct claims is accepted
	good := mint(map[string]any{"userId": subject, "sessionVersion": sessionVersion})
	requestAndAssertCode(http.StatusOK, t, http.MethodGet, good, nil, usersMeEndpoint)
}

// TestRecoveryCooldownKeepsCode guards the recovery anti-flood behavior: once a reset code has been
// delivered, an immediate second recovery request is silently accepted without replacing it, for
// unverified and verified accounts alike, so the code from the first email keeps working.
func TestRecoveryCooldownKeepsCode(t *testing.T) {
	c := qt.New(t)
	originalCooldown := testAPI.otpCooldown
	testAPI.otpCooldown = time.Hour
	defer func() {
		testAPI.otpCooldown = originalCooldown
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()

	// --- unverified account: recovery mails a reset code right after registration (the
	// registration mail carries the verify-account code, which has its own cooldown)
	mail := fmt.Sprintf("%d-cooldown@test.com", internal.RandomInt(100000))
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: mail, Password: testPass, FirstName: testFirstName, LastName: testLastName},
		usersEndpoint)
	verifyCode := verificationCodeRgx.FindStringSubmatch(waitForEmail(t, mail))
	c.Assert(len(verifyCode) > 1, qt.IsTrue)

	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: mail}, usersRecoveryPasswordEndpoint)
	resetCode := passwordResetRgx.FindStringSubmatch(waitForEmail(t, mail))
	c.Assert(len(resetCode) > 1, qt.IsTrue)

	// a second request inside the cooldown is accepted but keeps the first reset code
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: mail}, usersRecoveryPasswordEndpoint)

	// the registration code still verifies the account: codes of different types never replace each other
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserVerification{Email: mail, Code: verifyCode[1]}, verifyUserEndpoint)

	// --- verified account: more requests inside the cooldown and the first reset code still works
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: mail}, usersRecoveryPasswordEndpoint)
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "", &apicommon.UserPasswordReset{
		Email:       mail,
		Code:        resetCode[1],
		NewPassword: "cooldownpass123",
	}, usersResetPasswordEndpoint)
}

// TestExhaustedCodeIsReplaced guards against locking an account owner out by spending a code's
// guess budget: once a password-reset or account-verification code has used all its attempts,
// a new recovery or resend request issues a fresh code instead of re-sending the dead one.
func TestExhaustedCodeIsReplaced(t *testing.T) {
	c := qt.New(t)
	originalCooldown := testAPI.otpCooldown
	testAPI.otpCooldown = 0
	defer func() {
		testAPI.otpCooldown = originalCooldown
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()

	mail := fmt.Sprintf("%d-exhausted@test.com", internal.RandomInt(100000))
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: mail, Password: testPass, FirstName: testFirstName, LastName: testLastName},
		usersEndpoint)
	verifyCode := verificationCodeRgx.FindStringSubmatch(waitForEmail(t, mail))
	c.Assert(len(verifyCode) > 1, qt.IsTrue)

	// --- account verification: burn the registration code, then resend
	for range apicommon.VerificationCodeMaxAttempts {
		requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "",
			&apicommon.UserVerification{Email: mail, Code: verifyCode[1] + "x"}, verifyUserEndpoint)
	}
	requestAndAssertError(errors.ErrVerificationMaxAttempts, t, http.MethodPost, "",
		&apicommon.UserVerification{Email: mail, Code: verifyCode[1]}, verifyUserEndpoint)
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserVerification{Email: mail}, verifyUserCodeEndpoint)
	freshVerify := verificationCodeRgx.FindStringSubmatch(waitForEmail(t, mail))
	c.Assert(len(freshVerify) > 1, qt.IsTrue)
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserVerification{Email: mail, Code: freshVerify[1]}, verifyUserEndpoint)

	// --- password reset: burn the reset code, then request recovery again
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: mail}, usersRecoveryPasswordEndpoint)
	resetCode := passwordResetRgx.FindStringSubmatch(waitForEmail(t, mail))
	c.Assert(len(resetCode) > 1, qt.IsTrue)
	for range apicommon.VerificationCodeMaxAttempts {
		requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "", &apicommon.UserPasswordReset{
			Email: mail, Code: resetCode[1] + "x", NewPassword: "otherpassword123",
		}, usersResetPasswordEndpoint)
	}
	requestAndAssertError(errors.ErrVerificationMaxAttempts, t, http.MethodPost, "", &apicommon.UserPasswordReset{
		Email: mail, Code: resetCode[1], NewPassword: "otherpassword123",
	}, usersResetPasswordEndpoint)
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: mail}, usersRecoveryPasswordEndpoint)
	freshReset := passwordResetRgx.FindStringSubmatch(waitForEmail(t, mail))
	c.Assert(len(freshReset) > 1, qt.IsTrue)
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "", &apicommon.UserPasswordReset{
		Email: mail, Code: freshReset[1], NewPassword: "otherpassword123",
	}, usersResetPasswordEndpoint)
}

// TestRecoveryUnverifiedAccountGetsReset covers #713: recovery for an unverified account mails the
// password-reset code, never the verification one, and redeeming it replaces the registration
// password, verifies the account and drops the pending verification code.
func TestRecoveryUnverifiedAccountGetsReset(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()

	mail := fmt.Sprintf("%d-unverified-reset@test.com", internal.RandomInt(100000))
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: mail, Password: testPass, FirstName: testFirstName, LastName: testLastName},
		usersEndpoint)
	waitForEmail(t, mail) // the registration (verification) mail

	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: mail}, usersRecoveryPasswordEndpoint)
	mailBody := waitForEmail(t, mail)
	c.Assert(verificationCodeRgx.FindStringSubmatch(mailBody), qt.IsNil)
	resetCode := passwordResetRgx.FindStringSubmatch(mailBody)
	c.Assert(len(resetCode) > 1, qt.IsTrue)

	// the account cannot sign in before the reset
	requestAndAssertError(errors.ErrUserNoVerified, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: mail, Password: testPass}, authLoginEndpoint)

	newPassword := "unverifiedreset123"
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "", &apicommon.UserPasswordReset{
		Email:       mail,
		Code:        resetCode[1],
		NewPassword: newPassword,
	}, usersResetPasswordEndpoint)

	user, err := testDB.UserByEmail(mail)
	c.Assert(err, qt.IsNil)
	c.Assert(user.Verified, qt.IsTrue)
	_, err = testDB.UserVerificationCode(user, db.CodeTypeVerifyAccount)
	c.Assert(err, qt.Equals, db.ErrNotFound)

	// the registration password is gone, the new one signs in
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: mail, Password: testPass}, authLoginEndpoint)
	login := requestAndParse[apicommon.LoginResponse](t, http.MethodPost, "",
		&apicommon.UserInfo{Email: mail, Password: newPassword}, authLoginEndpoint)
	c.Assert(login.Token, qt.Not(qt.Equals), "")
}

// TestOAuthAccountWithPasswordRecovers checks an account with both a password and a linked OAuth
// provider keeps the normal recovery and reset flow.
func TestOAuthAccountWithPasswordRecovers(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()

	token := testCreateUser(t, testPass)
	me := requestAndParse[apicommon.UserInfo](t, http.MethodGet, token, nil, usersMeEndpoint)
	c.Assert(testDB.SetUserOAuthProvider(me.ID, "github",
		db.OAuthProvider{ExternalID: fmt.Sprintf("0x%d", internal.RandomInt(1000000)), LinkedAt: time.Now()}), qt.IsNil)

	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: me.Email}, usersRecoveryPasswordEndpoint)
	resetCode := passwordResetRgx.FindStringSubmatch(waitForEmail(t, me.Email))
	c.Assert(len(resetCode) > 1, qt.IsTrue)

	newPassword := "oauthlinkedreset123"
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "", &apicommon.UserPasswordReset{
		Email:       me.Email,
		Code:        resetCode[1],
		NewPassword: newPassword,
	}, usersResetPasswordEndpoint)
	login := requestAndParse[apicommon.LoginResponse](t, http.MethodPost, "",
		&apicommon.UserInfo{Email: me.Email, Password: newPassword}, authLoginEndpoint)
	c.Assert(login.Token, qt.Not(qt.Equals), "")
	user, err := testDB.User(me.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(user.OAuth, qt.HasLen, 1)
}
