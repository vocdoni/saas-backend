package api

import (
	"fmt"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/internal"
)

// registerVerifiedUser registers and verifies a user with a known email and
// password (unlike testCreateUser, which hides the generated credentials), then
// logs in and returns the email and a valid JWT. It is used by the session
// invalidation tests, which need to log in again after a credential change.
func registerVerifiedUser(t *testing.T, password string) (email, token string) {
	t.Helper()
	n := internal.RandomInt(100000000000)
	email = fmt.Sprintf("%d%s", n, testEmail)

	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "", &apicommon.UserInfo{
		Email:     email,
		Password:  password,
		FirstName: fmt.Sprintf("%d%s", n, testFirstName),
		LastName:  fmt.Sprintf("%d%s", n, testLastName),
	}, usersEndpoint)

	mailCode := apiTestVerificationCodeRgx.FindStringSubmatch(waitForEmail(t, email))
	qt.Assert(t, len(mailCode) > 1, qt.IsTrue)
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "", &apicommon.UserVerification{
		Email: email,
		Code:  mailCode[1],
	}, verifyUserEndpoint)

	login := requestAndParse[apicommon.LoginResponse](t, http.MethodPost, "",
		&apicommon.UserInfo{Email: email, Password: password}, authLoginEndpoint)
	qt.Assert(t, login.Token, qt.Not(qt.Equals), "")
	return email, login.Token
}

// TestSessionInvalidatedOnPasswordChange verifies that changing the password
// through PUT /users/password revokes every previously issued JWT (M1): the
// token version embedded in the old token no longer matches the bumped stored
// value, so the old token is rejected while a freshly minted one works.
func TestSessionInvalidatedOnPasswordChange(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()

	email, oldToken := registerVerifiedUser(t, testPass)

	// The token works before the credential change.
	requestAndAssertCode(http.StatusOK, t, http.MethodGet, oldToken, nil, usersMeEndpoint)

	// Change the password using the still-valid token.
	newPass := "newpassword123"
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, oldToken,
		&apicommon.UserPasswordUpdate{OldPassword: testPass, NewPassword: newPass}, usersPasswordEndpoint)

	// The old token is now rejected: its embedded token version is stale.
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodGet, oldToken, nil, usersMeEndpoint)

	// Logging in with the new password mints a token carrying the new version.
	login := requestAndParse[apicommon.LoginResponse](t, http.MethodPost, "",
		&apicommon.UserInfo{Email: email, Password: newPass}, authLoginEndpoint)
	c.Assert(login.Token, qt.Not(qt.Equals), "")
	requestAndAssertCode(http.StatusOK, t, http.MethodGet, login.Token, nil, usersMeEndpoint)
}

// TestSessionInvalidatedOnPasswordReset verifies that completing a password
// reset (POST /users/password/reset) revokes outstanding JWTs (M1): this is the
// account-takeover-recovery path, so a token an attacker may hold must stop
// working once the legitimate owner resets.
func TestSessionInvalidatedOnPasswordReset(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()

	email, oldToken := registerVerifiedUser(t, testPass)
	requestAndAssertCode(http.StatusOK, t, http.MethodGet, oldToken, nil, usersMeEndpoint)

	// Request a password reset code; it is delivered by email.
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: email}, usersRecoveryPasswordEndpoint)
	resetCode := apiTestVerificationCodeRgx.FindStringSubmatch(waitForEmail(t, email))
	c.Assert(len(resetCode) > 1, qt.IsTrue)

	// Reset the password with the emailed code.
	newPass := "resetpassword123"
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "", &apicommon.UserPasswordReset{
		Email:       email,
		Code:        resetCode[1],
		NewPassword: newPass,
	}, usersResetPasswordEndpoint)

	// The token held before the reset is now rejected.
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodGet, oldToken, nil, usersMeEndpoint)

	// The new password produces a working token.
	login := requestAndParse[apicommon.LoginResponse](t, http.MethodPost, "",
		&apicommon.UserInfo{Email: email, Password: newPass}, authLoginEndpoint)
	c.Assert(login.Token, qt.Not(qt.Equals), "")
	requestAndAssertCode(http.StatusOK, t, http.MethodGet, login.Token, nil, usersMeEndpoint)
}
