package api

import (
	"fmt"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
)

// TestUpdateUserInfo covers the updateUserInfoHandler (PUT /users/me) and the
// updateUserEmailVerifyHandler (POST /users/me/email/verify): updating names, the
// pending-email confirmation flow (including session revocation and single-use codes),
// malformed-email rejection, and the unauthenticated case.
func TestUpdateUserInfo(t *testing.T) {
	token := testCreateUser(t, testPass)
	c := qt.New(t)
	defer func() {
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()

	// Update first and last name; a fresh token is returned on success.
	res := requestAndParse[apicommon.LoginResponse](t, http.MethodPut, token,
		&apicommon.UserInfo{FirstName: "Renamed", LastName: "Person"}, usersMeEndpoint)
	c.Assert(res.Token, qt.Not(qt.Equals), "")

	me := requestAndParse[apicommon.UserInfo](t, http.MethodGet, res.Token, nil, usersMeEndpoint)
	c.Assert(me.FirstName, qt.Equals, "Renamed")
	c.Assert(me.LastName, qt.Equals, "Person")

	// Requesting an email change does not switch the email: a code is sent to the new
	// address and the change stays pending until that code is confirmed.
	newEmail := fmt.Sprintf("updated-%d@test.com", internal.RandomInt(100000000000))
	res2 := requestAndParse[apicommon.LoginResponse](t, http.MethodPut, res.Token,
		&apicommon.UserInfo{Email: newEmail}, usersMeEndpoint)
	c.Assert(res2.Token, qt.Not(qt.Equals), "")

	me2 := requestAndParse[apicommon.UserInfo](t, http.MethodGet, res2.Token, nil, usersMeEndpoint)
	c.Assert(me2.Email, qt.Equals, me.Email, qt.Commentf("email must not change before confirmation"))

	// The verification code is delivered to the NEW address, proving mailbox ownership.
	mailBody := waitForEmail(t, newEmail)
	mailCode := verificationCodeRgx.FindStringSubmatch(mailBody)
	c.Assert(len(mailCode) > 1, qt.IsTrue)

	// A wrong code must not switch the email.
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, res2.Token,
		&apicommon.UserVerification{Code: mailCode[1] + "x"}, usersMeEmailVerifyEndpoint)

	// Confirming the code switches the email and returns a fresh token.
	res3 := requestAndParse[apicommon.LoginResponse](t, http.MethodPost, res2.Token,
		&apicommon.UserVerification{Code: mailCode[1]}, usersMeEmailVerifyEndpoint)
	me3 := requestAndParse[apicommon.UserInfo](t, http.MethodGet, res3.Token, nil, usersMeEndpoint)
	c.Assert(me3.Email, qt.Equals, newEmail)

	// The email switch revokes every pre-change session.
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodGet, res2.Token, nil, usersMeEndpoint)

	// The consumed code cannot be redeemed again.
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, res3.Token,
		&apicommon.UserVerification{Code: mailCode[1]}, usersMeEmailVerifyEndpoint)

	// A malformed email must be rejected with 400.
	requestAndAssertError(errors.ErrEmailMalformed, t, http.MethodPut, res3.Token,
		&apicommon.UserInfo{Email: "not-an-email"}, usersMeEndpoint)

	// Without a token the endpoint must reject with 401.
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPut, "",
		&apicommon.UserInfo{FirstName: "X"}, usersMeEndpoint)
}

// TestCreatorEmailChangeKeepsSigner checks that an organization creator can change their email:
// the organization follows the new address as its creator, while the signer seed its on-chain
// key is derived from stays the same.
func TestCreatorEmailChangeKeepsSigner(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, testPass)
	orgAddress := testCreateOrganization(t, token)
	before, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(before.SignerSeed, qt.Not(qt.Equals), "")

	newEmail := fmt.Sprintf("creator-%d@test.com", internal.RandomInt(100000000000))
	res := requestAndParse[apicommon.LoginResponse](t, http.MethodPut, token,
		&apicommon.UserInfo{Email: newEmail}, usersMeEndpoint)
	mailCode := verificationCodeRgx.FindStringSubmatch(waitForEmail(t, newEmail))
	c.Assert(len(mailCode) > 1, qt.IsTrue)
	requestAndParse[apicommon.LoginResponse](t, http.MethodPost, res.Token,
		&apicommon.UserVerification{Code: mailCode[1]}, usersMeEmailVerifyEndpoint)

	after, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(after.Creator, qt.Equals, newEmail)
	c.Assert(after.SignerSeed, qt.Equals, before.SignerSeed)
}

// TestUpdateUserPassword covers the updateUserPasswordHandler (PUT /users/password):
// the too-short check runs before the old-password check, then a successful change.
func TestUpdateUserPassword(t *testing.T) {
	token := testCreateUser(t, testPass)
	c := qt.New(t)
	defer func() {
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()

	// Negative cases run first, while the stored password is still testPass.

	// New password shorter than 8 chars is rejected before the old-password check.
	requestAndAssertError(errors.ErrPasswordTooShort, t, http.MethodPut, token,
		&apicommon.UserPasswordUpdate{OldPassword: testPass, NewPassword: "short"}, usersPasswordEndpoint)

	// A wrong old password is rejected with 401.
	requestAndAssertError(errors.ErrUnauthorized, t, http.MethodPut, token,
		&apicommon.UserPasswordUpdate{OldPassword: "wrongpassword", NewPassword: "newpassword123"},
		usersPasswordEndpoint)

	// Success runs last, as it changes the stored password. The response carries a
	// fresh token for the caller, since the change revokes every existing session.
	res := requestAndParse[apicommon.LoginResponse](t, http.MethodPut, token,
		&apicommon.UserPasswordUpdate{OldPassword: testPass, NewPassword: "newpassword123"},
		usersPasswordEndpoint)
	c.Assert(res.Token, qt.Not(qt.Equals), "")

	// The pre-change token is revoked; the returned one works.
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodGet, token, nil, usersMeEndpoint)
	requestAndAssertCode(http.StatusOK, t, http.MethodGet, res.Token, nil, usersMeEndpoint)

	// Without a token the endpoint must reject with 401.
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPut, "",
		&apicommon.UserPasswordUpdate{OldPassword: testPass, NewPassword: "newpassword123"},
		usersPasswordEndpoint)
}

// TestUserVerificationCodeInfo covers the userVerificationCodeInfoHandler
// (GET /users/verify/code) for an unverified user, the missing-email error case,
// and the anti-enumeration behavior for unknown users.
func TestUserVerificationCodeInfo(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()

	// Register an unverified user; POST /users does not verify the account.
	mail := fmt.Sprintf("%d%s", internal.RandomInt(100000000000), testEmail)
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: mail, Password: testPass, FirstName: testFirstName, LastName: testLastName},
		usersEndpoint)

	// The verification info for the unverified user is returned and valid.
	uv := requestAndParse[apicommon.UserVerification](t, http.MethodGet, "", nil,
		"users", "verify", "code?email="+mail)
	c.Assert(uv.Email, qt.Equals, mail)
	c.Assert(uv.Valid, qt.IsTrue)

	// A missing email parameter must be rejected with 400.
	requestAndAssertError(errors.ErrInvalidUserData, t, http.MethodGet, "", nil,
		"users", "verify", "code")

	// An unknown user yields the same 200 shape as a known one (just not valid), so the
	// public endpoint cannot be used to enumerate registered accounts.
	unknown := fmt.Sprintf("nobody-%d@nowhere.com", internal.RandomInt(100000000000))
	uvUnknown := requestAndParse[apicommon.UserVerification](t, http.MethodGet, "", nil,
		"users", "verify", "code?email="+unknown)
	c.Assert(uvUnknown.Email, qt.Equals, unknown)
	c.Assert(uvUnknown.Valid, qt.IsFalse)
	c.Assert(uvUnknown.Expiration.IsZero(), qt.IsTrue)
}
