package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
)

// testCreateOAuthOnlyUser stores a verified account without a password, as an OAuth sign-up creates
// it, and returns it with a session token minted the way the login handlers do.
func testCreateOAuthOnlyUser(t *testing.T) (*db.User, string) {
	t.Helper()
	email := fmt.Sprintf("oauth-setup-%d@test.com", internal.RandomInt(100000000000))
	userID, err := testDB.SetUser(&db.User{
		Email:     email,
		FirstName: "OAuth",
		LastName:  "Only",
		Verified:  true,
		OAuth: map[string]db.OAuthProvider{
			"google": {ExternalID: fmt.Sprintf("0x%d", internal.RandomInt(100000000000)), LinkedAt: time.Now()},
		},
	})
	qt.Assert(t, err, qt.IsNil)
	user, err := testDB.User(userID)
	qt.Assert(t, err, qt.IsNil)
	login, err := testAPI.buildLoginResponse(user)
	qt.Assert(t, err, qt.IsNil)
	return user, login.Token
}

// TestPasswordSetupFlow covers #863: a password-less (OAuth-only) account requests a setup code,
// redeems it to set its first password, and every older session is revoked.
func TestPasswordSetupFlow(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()
	user, token := testCreateOAuthOnlyUser(t)

	requestAndAssertCode(http.StatusOK, t, http.MethodPost, token, nil, usersMePasswordSetupEndpoint)
	mailBody := waitForEmail(t, user.Email)
	setupCode := passwordSetupRgx.FindStringSubmatch(mailBody)
	c.Assert(setupCode, qt.HasLen, 2)
	setupLink := verificationLinkRgx.FindStringSubmatch(mailBody)
	c.Assert(setupLink, qt.HasLen, 2)
	c.Assert(setupLink[1], qt.Contains, "/account/password/setup")

	newPassword := "setuppassword123"
	session := requestAndParse[apicommon.LoginResponse](t, http.MethodPost, token,
		&apicommon.UserPasswordSetup{Code: setupCode[1], NewPassword: newPassword}, usersMePasswordSetupVerifyEndpoint)
	c.Assert(session.Token, qt.Not(qt.Equals), "")

	// the session that requested the setup is revoked, the returned one works
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodGet, token, nil, usersMeEndpoint)
	me := requestAndParse[apicommon.UserInfo](t, http.MethodGet, session.Token, nil, usersMeEndpoint)
	c.Assert(me.HasPassword, qt.IsTrue)

	// email and password now sign in, and the code cannot be redeemed again
	login := requestAndParse[apicommon.LoginResponse](t, http.MethodPost, "",
		&apicommon.UserInfo{Email: user.Email, Password: newPassword}, authLoginEndpoint)
	c.Assert(login.Token, qt.Not(qt.Equals), "")
	_, err := testDB.UserVerificationCode(user, db.CodeTypePasswordSetup)
	c.Assert(err, qt.Equals, db.ErrNotFound)
}

// TestPasswordSetupRefusedWithPassword checks an account that already has a password can neither
// request nor redeem a setup code.
func TestPasswordSetupRefusedWithPassword(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()
	token := testCreateUser(t, testPass)

	requestAndAssertError(errors.ErrUserAlreadyHasPassword, t, http.MethodPost, token, nil, usersMePasswordSetupEndpoint)
	requestAndAssertError(errors.ErrUserAlreadyHasPassword, t, http.MethodPost, token,
		&apicommon.UserPasswordSetup{Code: "000000", NewPassword: "setuppassword123"}, usersMePasswordSetupVerifyEndpoint)

	// the registration password still signs in
	me := requestAndParse[apicommon.UserInfo](t, http.MethodGet, token, nil, usersMeEndpoint)
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: me.Email, Password: testPass}, authLoginEndpoint)
}

// TestPasswordSetupThrottlingAndGuessing covers issuance throttling and guess limiting: a second
// request inside the cooldown is refused, wrong codes spend the per-code budget before any compare,
// and a fresh code issued past the cooldown works exactly once.
func TestPasswordSetupThrottlingAndGuessing(t *testing.T) {
	c := qt.New(t)
	originalCooldown := testAPI.otpCooldown
	testAPI.otpCooldown = time.Hour
	defer func() {
		testAPI.otpCooldown = originalCooldown
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()
	user, token := testCreateOAuthOnlyUser(t)

	// a second request inside the cooldown is refused and keeps the first code
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, token, nil, usersMePasswordSetupEndpoint)
	firstCode := passwordSetupRgx.FindStringSubmatch(waitForEmail(t, user.Email))
	c.Assert(firstCode, qt.HasLen, 2)
	requestAndAssertError(errors.ErrVerificationMaxAttempts, t, http.MethodPost, token, nil, usersMePasswordSetupEndpoint)

	// a short password is refused before the code is looked at
	requestAndAssertError(errors.ErrPasswordTooShort, t, http.MethodPost, token,
		&apicommon.UserPasswordSetup{Code: firstCode[1], NewPassword: "short"}, usersMePasswordSetupVerifyEndpoint)

	// wrong codes spend the guess budget; once spent even the right code is refused
	for range apicommon.VerificationCodeMaxAttempts {
		requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, token,
			&apicommon.UserPasswordSetup{Code: firstCode[1] + "x", NewPassword: "setuppassword123"},
			usersMePasswordSetupVerifyEndpoint)
	}
	requestAndAssertError(errors.ErrVerificationMaxAttempts, t, http.MethodPost, token,
		&apicommon.UserPasswordSetup{Code: firstCode[1], NewPassword: "setuppassword123"}, usersMePasswordSetupVerifyEndpoint)
	stored, err := testDB.User(user.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.Password, qt.Equals, "")

	// past the cooldown the exhausted code is replaced by a fresh one, which works once
	testAPI.otpCooldown = 0
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, token, nil, usersMePasswordSetupEndpoint)
	freshCode := passwordSetupRgx.FindStringSubmatch(waitForEmail(t, user.Email))
	c.Assert(freshCode, qt.HasLen, 2)
	session := requestAndParse[apicommon.LoginResponse](t, http.MethodPost, token,
		&apicommon.UserPasswordSetup{Code: freshCode[1], NewPassword: "setuppassword123"}, usersMePasswordSetupVerifyEndpoint)
	_, err = testDB.UserVerificationCode(user, db.CodeTypePasswordSetup)
	c.Assert(err, qt.Equals, db.ErrNotFound)

	// with the password set the flow is closed, also for the new session
	requestAndAssertError(errors.ErrUserAlreadyHasPassword, t, http.MethodPost, session.Token,
		&apicommon.UserPasswordSetup{Code: freshCode[1], NewPassword: "anotherpassword123"}, usersMePasswordSetupVerifyEndpoint)
}

// TestPasswordSetupConcurrentDoubleClick verifies that two simultaneous first-ever code requests
// result in exactly one email: the second goroutine loses the atomic insert race and is blocked
// instead of sending a competing code that would invalidate the first.
func TestPasswordSetupConcurrentDoubleClick(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()
	user, token := testCreateOAuthOnlyUser(t)

	// release both goroutines simultaneously before any code exists
	var wg sync.WaitGroup
	statuses := make([]int, 2)
	barrier := make(chan struct{})
	for i := range 2 {
		wg.Go(func() {
			<-barrier
			_, statuses[i] = testRequest(t, http.MethodPost, token, nil, usersMePasswordSetupEndpoint)
		})
	}
	close(barrier)
	wg.Wait()

	// at least one request must have succeeded
	c.Assert(statuses[0] == http.StatusOK || statuses[1] == http.StatusOK, qt.IsTrue)

	// exactly one email must have been sent; the race loser is blocked, never mails
	_ = waitForEmail(t, user.Email)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := testMailService.FindEmail(ctx, user.Email)
	c.Assert(errors.Is(err, io.EOF), qt.IsTrue, qt.Commentf("a second email was unexpectedly delivered"))
}

// TestPasswordSetupNeedsMail checks no setup code is issued when the service runs without mail, since
// it could never reach the mailbox it has to prove.
func TestPasswordSetupNeedsMail(t *testing.T) {
	c := qt.New(t)
	originalMail := testAPI.mail
	testAPI.mail = nil
	defer func() {
		testAPI.mail = originalMail
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()
	user, token := testCreateOAuthOnlyUser(t)

	requestAndAssertError(errors.ErrNotSupported, t, http.MethodPost, token, nil, usersMePasswordSetupEndpoint)
	_, err := testDB.UserVerificationCode(user, db.CodeTypePasswordSetup)
	c.Assert(err, qt.Equals, db.ErrNotFound)
}
