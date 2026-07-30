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

// TestUpdateUserInfo covers the updateUserInfoHandler (PUT /users/me): updating
// names and email, malformed-email rejection, and the unauthenticated case.
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

	// Update the email; the JWT subject is the email, so a new token is issued.
	newEmail := fmt.Sprintf("updated-%d@test.com", internal.RandomInt(100000000000))
	res2 := requestAndParse[apicommon.LoginResponse](t, http.MethodPut, res.Token,
		&apicommon.UserInfo{Email: newEmail}, usersMeEndpoint)
	c.Assert(res2.Token, qt.Not(qt.Equals), "")

	me2 := requestAndParse[apicommon.UserInfo](t, http.MethodGet, res2.Token, nil, usersMeEndpoint)
	c.Assert(me2.Email, qt.Equals, newEmail)

	// A malformed email must be rejected with 400.
	requestAndAssertError(errors.ErrEmailMalformed, t, http.MethodPut, res2.Token,
		&apicommon.UserInfo{Email: "not-an-email"}, usersMeEndpoint)

	// Without a token the endpoint must reject with 401.
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPut, "",
		&apicommon.UserInfo{FirstName: "X"}, usersMeEndpoint)
}

// TestUpdateUserInfoEmailBlockedForOrgCreators verifies that a user who created
// an organization cannot change their email: the org signing key is derived
// deterministically from the creator email, so allowing the change would
// permanently brick on-chain signing for that org (C1). Non-email updates and
// same-email requests must still succeed.
func TestUpdateUserInfoEmailBlockedForOrgCreators(t *testing.T) {
	token := testCreateUser(t, testPass)
	c := qt.New(t)
	defer func() {
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()

	// This user creates an organization, becoming its creator.
	testCreateOrganization(t, token)

	// Changing the email is now refused with 409 Conflict.
	newEmail := fmt.Sprintf("blocked-%d@test.com", internal.RandomInt(100000000000))
	requestAndAssertError(errors.ErrEmailChangeNotAllowed, t, http.MethodPut, token,
		&apicommon.UserInfo{Email: newEmail}, usersMeEndpoint)

	// Updating only the name must still succeed for an org creator.
	res := requestAndParse[apicommon.LoginResponse](t, http.MethodPut, token,
		&apicommon.UserInfo{FirstName: "StillAllowed"}, usersMeEndpoint)
	c.Assert(res.Token, qt.Not(qt.Equals), "")

	me := requestAndParse[apicommon.UserInfo](t, http.MethodGet, res.Token, nil, usersMeEndpoint)
	c.Assert(me.FirstName, qt.Equals, "StillAllowed")

	// Re-submitting the same email is not a change, so it must be allowed.
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, res.Token,
		&apicommon.UserInfo{Email: me.Email}, usersMeEndpoint)
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

	// Success runs last, as it changes the stored password.
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, token,
		&apicommon.UserPasswordUpdate{OldPassword: testPass, NewPassword: "newpassword123"},
		usersPasswordEndpoint)

	// Without a token the endpoint must reject with 401.
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPut, "",
		&apicommon.UserPasswordUpdate{OldPassword: testPass, NewPassword: "newpassword123"},
		usersPasswordEndpoint)
}

// TestUserVerificationCodeInfo covers the userVerificationCodeInfoHandler
// (GET /users/verify/code) for an unverified user, plus the missing-email and
// unknown-user error cases.
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

	// An unknown user must yield 404.
	unknown := fmt.Sprintf("nobody-%d@nowhere.com", internal.RandomInt(100000000000))
	requestAndAssertError(errors.ErrUserNotFound, t, http.MethodGet, "", nil,
		"users", "verify", "code?email="+unknown)
}
