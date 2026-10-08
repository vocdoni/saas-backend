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

// TestRecoveryCooldownKeepsCode guards the recovery anti-flood behavior: once a code has been
// delivered, an immediate second recovery request is silently accepted without replacing the
// stored code, for both the verified (password reset) and the unverified (account verification)
// branches — the code from the first email must keep working.
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

	// --- unverified branch: recovery right after registration must not mint a new
	// verification code (the registration delivery started the cooldown)
	mail := fmt.Sprintf("%d-cooldown@test.com", internal.RandomInt(100000))
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: mail, Password: testPass, FirstName: testFirstName, LastName: testLastName},
		usersEndpoint)
	mailBody := waitForEmail(t, mail)
	verifyCode := verificationCodeRgx.FindStringSubmatch(mailBody)
	c.Assert(len(verifyCode) > 1, qt.IsTrue)

	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: mail}, usersRecoveryPasswordEndpoint)

	// the registration code still verifies the account: it was not replaced
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserVerification{Email: mail, Code: verifyCode[1]}, verifyUserEndpoint)

	// --- verified branch: a second recovery request during the cooldown is silently
	// accepted and the first reset code keeps working
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: mail}, usersRecoveryPasswordEndpoint)
	mailBody = waitForEmail(t, mail)
	resetCode := passwordResetRgx.FindStringSubmatch(mailBody)
	c.Assert(len(resetCode) > 1, qt.IsTrue)

	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: mail}, usersRecoveryPasswordEndpoint)

	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "", &apicommon.UserPasswordReset{
		Email:       mail,
		Code:        resetCode[1],
		NewPassword: "cooldownpass123",
	}, usersResetPasswordEndpoint)
}
