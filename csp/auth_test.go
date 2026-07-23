package csp

import (
	"context"
	"io"
	"regexp"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
	"github.com/vocdoni/saas-backend/test"
	"github.com/xlzd/gotp"
)

func TestBundleAuthToken(t *testing.T) {
	c := qt.New(t)
	testDB, err := db.New(testMongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)

	ctx := context.Background()
	csp, err := New(ctx, &Config{
		DB:                       testDB,
		MailService:              testMailService,
		SMSService:               testSMSService,
		NotificationCoolDownTime: time.Second * 5,
		RootKey:                  *testRootKey,
	})
	c.Assert(err, qt.IsNil)

	c.Run("empty bundleID", func(c *qt.C) {
		_, err := csp.BundleAuthToken(
			nil,
			testUserID,
			testUserEmail,
			notifications.EmailChallenge,
			apicommon.DefaultLang,
			"",
			"",
			testAddress.Address(),
		)
		c.Assert(err, qt.ErrorIs, ErrNoBundleID)
	})

	c.Run("empty userID", func(c *qt.C) {
		_, err := csp.BundleAuthToken(
			testBundleID,
			nil,
			testUserEmail,
			notifications.EmailChallenge,
			apicommon.DefaultLang,
			"",
			"",
			testAddress.Address(),
		)
		c.Assert(err, qt.ErrorIs, ErrNoUserID)
	})

	c.Run("notification cooldown reached", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		// generate a valid token
		_, err := csp.BundleAuthToken(testBundleID, testUserID, "",
			notifications.EmailChallenge, apicommon.DefaultLang, "", "", testAddress.Address())
		c.Assert(err, qt.ErrorIs, ErrNotificationFailure)
		// try to generate a new token before the cooldown time
		_, err = csp.BundleAuthToken(testBundleID, testUserID, "",
			notifications.EmailChallenge, apicommon.DefaultLang, "", "", testAddress.Address())
		c.Assert(err, qt.ErrorIs, errors.ErrAttemptCoolDownTime)

		var apiErr errors.Error
		c.Assert(errors.As(err, &apiErr), qt.IsTrue)

		data, ok := apiErr.Data.(map[string]any)
		c.Assert(ok, qt.IsTrue)

		coolDownTime, ok := data["coolDownTime"]
		c.Assert(ok, qt.IsTrue)

		switch v := coolDownTime.(type) {
		case int:
			c.Assert(v > 0, qt.IsTrue)
		case int64:
			c.Assert(v > 0, qt.IsTrue)
		case float64:
			c.Assert(v > 0, qt.IsTrue)
		default:
			c.Fatalf("coolDownTime should be a positive numeric value in a client-friendly unit, got %T", coolDownTime)
		}
	})

	c.Run("auth-only ignores cooldown", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		token, err := csp.BundleAuthToken(
			testBundleID,
			testUserID,
			"",
			"",
			apicommon.DefaultLang,
			"",
			"",
			testAddress.Address(),
		)
		c.Assert(err, qt.IsNil)
		c.Assert(token, qt.Not(qt.IsNil))

		token, err = csp.BundleAuthToken(
			testBundleID,
			testUserID,
			"",
			"",
			apicommon.DefaultLang,
			"",
			"",
			testAddress.Address(),
		)
		c.Assert(err, qt.IsNil)
		c.Assert(token, qt.Not(qt.IsNil))
	})

	c.Run("success test", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		bundleID := internal.HexBytes(testBundleID)
		token, err := csp.BundleAuthToken(
			testBundleID,
			testUserID,
			testUserEmail,
			notifications.EmailChallenge,
			apicommon.DefaultLang,
			testOrgName,
			testOrgLogo,
			testAddress.Address(),
		)
		c.Assert(err, qt.IsNil)
		c.Assert(token, qt.Not(qt.IsNil))
		authTokenResult, err := csp.Storage.CSPAuth(token)
		c.Assert(err, qt.IsNil)
		expectedCode := gotp.NewDefaultHOTP(authTokenResult.Secret).At(0)
		c.Assert(authTokenResult.BundleID.Bytes(), qt.DeepEquals, bundleID.Bytes())
		c.Assert(authTokenResult.UserID.Bytes(), qt.DeepEquals, testUserID.Bytes())
		// wait to dequeue the notification
		time.Sleep(time.Second * 3)
		// get the verification code from the email
		mailBody, err := testMailService.FindEmail(context.Background(), testUserEmail)
		c.Assert(err, qt.IsNil)
		// parse the email body to get the verification code
		seedNotification, err := mailtemplates.VerifyOTPCodeNotification.Localized(apicommon.DefaultLang).
			ExecPlain(struct {
				Code             string
				Organization     string
				OrganizationLogo string
			}{`(.{6})`, testOrgName, testOrgLogo})
		c.Assert(err, qt.IsNil)
		rgxNotification := regexp.MustCompile(seedNotification.PlainBody)
		// verify the user
		mailCode := rgxNotification.FindStringSubmatch(mailBody)
		c.Assert(mailCode, qt.HasLen, 2)
		c.Assert(mailCode[1], qt.Equals, expectedCode)
		// get the user weight
	})
}

func TestVerifyBundleAuthToken(t *testing.T) {
	c := qt.New(t)

	testDB, err := db.New(testMongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)

	ctx := context.Background()
	csp, err := New(ctx, &Config{
		DB:                       testDB,
		MailService:              testMailService,
		SMSService:               testSMSService,
		NotificationCoolDownTime: time.Second * 5,
		NotificationTTL:          time.Second * 5,
		RootKey:                  *testRootKey,
	})
	c.Assert(err, qt.IsNil)

	c.Run("empty token", func(c *qt.C) {
		err := csp.VerifyBundleAuthToken(nil, "")
		c.Assert(err, qt.ErrorIs, ErrInvalidAuthToken)
	})

	c.Run("empty solution", func(c *qt.C) {
		err := csp.VerifyBundleAuthToken([]byte("invalid"), "")
		c.Assert(err, qt.ErrorIs, ErrInvalidSolution)
	})

	c.Run("token not found", func(c *qt.C) {
		err := csp.VerifyBundleAuthToken([]byte("invalid"), "invalid")
		c.Assert(err, qt.ErrorIs, ErrInvalidAuthToken)
	})

	c.Run("expired OTP", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		// shrink the OTP window for this subtest so we can trigger expiry with a
		// millisecond-scale sleep instead of waiting out the full window
		origExpiry := csp.notificationTTL
		csp.notificationTTL = 10 * time.Millisecond
		c.Cleanup(func() { csp.notificationTTL = origExpiry })
		secret := gotp.RandomSecret(16)
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, secret), qt.IsNil)
		code := gotp.NewDefaultHOTP(secret).At(0)
		time.Sleep(50 * time.Millisecond)
		err := csp.VerifyBundleAuthToken(testToken, code)
		c.Assert(err, qt.ErrorIs, ErrTokenExpired)
	})

	c.Run("solution not match", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		secret := gotp.RandomSecret(16)
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, secret), qt.IsNil)
		err := csp.VerifyBundleAuthToken(testToken, "invalid")
		c.Assert(err, qt.ErrorIs, ErrChallengeCodeFailure)
	})

	c.Run("empty secret rejected", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		// a token without a stored challenge secret (legacy row, auth-only, or
		// already-verified) must never OTP-verify, regardless of the solution.
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, ""), qt.IsNil)
		// the code an attacker would compute from an empty secret must not pass;
		// the token is reported expired so the client restarts the OTP flow
		emptySecretCode := gotp.NewDefaultHOTP("").At(0)
		err := csp.VerifyBundleAuthToken(testToken, emptySecretCode)
		c.Assert(err, qt.ErrorIs, ErrTokenExpired)
	})

	c.Run("success", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		secret := gotp.RandomSecret(16)
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, secret), qt.IsNil)
		code := gotp.NewDefaultHOTP(secret).At(0)
		err = csp.VerifyBundleAuthToken(testToken, code)
		c.Assert(err, qt.IsNil)
		// check that the token is verified and secret is cleared
		authTokenResult, err := csp.Storage.CSPAuth(testToken)
		c.Assert(err, qt.IsNil)
		c.Assert(authTokenResult.Verified, qt.IsTrue)
		c.Assert(authTokenResult.Secret, qt.Equals, "")
		// re-verifying an already-verified token (secret now wiped) must fail
		err = csp.VerifyBundleAuthToken(testToken, code)
		c.Assert(err, qt.ErrorIs, ErrTokenExpired)
	})

	c.Run("too many attempts", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		secret := gotp.RandomSecret(16)
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, secret), qt.IsNil)
		validCode := gotp.NewDefaultHOTP(secret).At(0)
		// exhaust the allowed attempts with wrong codes
		for range MaxChallengeAttempts {
			err = csp.VerifyBundleAuthToken(testToken, "000000")
			c.Assert(err, qt.ErrorIs, ErrChallengeCodeFailure)
		}
		// even the valid code is now rejected because attempts are exhausted
		err = csp.VerifyBundleAuthToken(testToken, validCode)
		c.Assert(err, qt.ErrorIs, ErrTooManyAttempts)
	})
}

func TestResendChallenge(t *testing.T) {
	c := qt.New(t)

	testDB, err := db.New(testMongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)

	ctx := context.Background()
	csp, err := New(ctx, &Config{
		DB:                       testDB,
		MailService:              testMailService,
		SMSService:               testSMSService,
		NotificationCoolDownTime: time.Second * 5,
		NotificationTTL:          time.Second * 30,
		RootKey:                  *testRootKey,
	})
	c.Assert(err, qt.IsNil)

	c.Run("empty token", func(c *qt.C) {
		err := csp.ResendChallenge(
			nil,
			testUserEmail,
			notifications.EmailChallenge,
			apicommon.DefaultLang,
			testOrgName,
			testOrgLogo,
			testAddress.Address(),
		)
		c.Assert(err, qt.ErrorIs, ErrInvalidAuthToken)
	})

	c.Run("empty destination and type", func(c *qt.C) {
		err := csp.ResendChallenge(
			testToken,
			"",
			"",
			apicommon.DefaultLang,
			testOrgName,
			testOrgLogo,
			testAddress.Address(),
		)
		c.Assert(err, qt.ErrorIs, errors.ErrInvalidData)
	})

	c.Run("empty destination", func(c *qt.C) {
		err := csp.ResendChallenge(
			testToken,
			"",
			notifications.EmailChallenge,
			apicommon.DefaultLang,
			testOrgName,
			testOrgLogo,
			testAddress.Address(),
		)
		c.Assert(err, qt.ErrorIs, errors.ErrInvalidData)
	})

	c.Run("empty challenge type", func(c *qt.C) {
		err := csp.ResendChallenge(
			testToken,
			testUserEmail,
			"",
			apicommon.DefaultLang,
			testOrgName,
			testOrgLogo,
			testAddress.Address(),
		)
		c.Assert(err, qt.ErrorIs, errors.ErrInvalidData)
	})

	c.Run("token not found", func(c *qt.C) {
		err := csp.ResendChallenge(
			testToken,
			testUserEmail,
			notifications.EmailChallenge,
			apicommon.DefaultLang,
			testOrgName,
			testOrgLogo,
			testAddress.Address(),
		)
		c.Assert(err, qt.ErrorIs, ErrInvalidAuthToken)
	})

	c.Run("token already verified", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, gotp.RandomSecret(16)), qt.IsNil)
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)

		err := csp.ResendChallenge(
			testToken,
			testUserEmail,
			notifications.EmailChallenge,
			apicommon.DefaultLang,
			testOrgName,
			testOrgLogo,
			testAddress.Address(),
		)
		c.Assert(err, qt.ErrorIs, errors.ErrUserAlreadyVerified)
	})

	c.Run("empty secret rejected", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		// an unverified token with no stored secret (legacy/auth-only) must not
		// have a code resent; it is reported expired so the client restarts.
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, ""), qt.IsNil)
		err := csp.ResendChallenge(
			testToken,
			testUserEmail,
			notifications.EmailChallenge,
			apicommon.DefaultLang,
			testOrgName,
			testOrgLogo,
			testAddress.Address(),
		)
		c.Assert(err, qt.ErrorIs, ErrTokenExpired)
	})

	c.Run("success", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		secret := gotp.RandomSecret(16)
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, secret), qt.IsNil)

		expectedCode := gotp.NewDefaultHOTP(secret).At(0)

		err = csp.ResendChallenge(
			testToken,
			testUserEmail,
			notifications.EmailChallenge,
			apicommon.DefaultLang,
			testOrgName,
			testOrgLogo,
			testAddress.Address(),
		)
		c.Assert(err, qt.IsNil)

		time.Sleep(time.Second * 3)
		mailCode := fetchOTPCodeFromEmail(c, testUserEmail)
		c.Assert(mailCode, qt.Equals, expectedCode)
	})
}

// TestResendChallengeCooldown verifies that successive resends of the same token
// are rate-limited: the first resend is allowed, an immediate second is rejected
// with the cooldown error, and once the cooldown elapses a further resend is
// allowed again (M4).
func TestResendChallengeCooldown(t *testing.T) {
	c := qt.New(t)

	testDB, err := db.New(testMongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)

	ctx := context.Background()
	// a short cooldown keeps the recovery leg of the test fast
	csp, err := New(ctx, &Config{
		DB:                       testDB,
		MailService:              testMailService,
		SMSService:               testSMSService,
		NotificationCoolDownTime: 800 * time.Millisecond,
		NotificationTTL:          time.Second * 30,
		RootKey:                  *testRootKey,
	})
	c.Assert(err, qt.IsNil)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, gotp.RandomSecret(16)), qt.IsNil)

	resend := func() error {
		return csp.ResendChallenge(
			testToken,
			testUserEmail,
			notifications.EmailChallenge,
			apicommon.DefaultLang,
			testOrgName,
			testOrgLogo,
			testAddress.Address(),
		)
	}

	// the first resend is always allowed (no prior resend recorded); drain the
	// delivered email so it does not pollute other tests' inbox searches
	c.Assert(resend(), qt.IsNil)
	fetchOTPCodeFromEmail(c, testUserEmail)
	// an immediate second resend is blocked by the cooldown (no email delivered)
	c.Assert(resend(), qt.ErrorIs, errors.ErrAttemptCoolDownTime)
	// after the cooldown elapses, a further resend is allowed again
	time.Sleep(time.Second)
	c.Assert(resend(), qt.IsNil)
	fetchOTPCodeFromEmail(c, testUserEmail)
}

func TestBundleAuthTokenResendAndVerifyFlow(t *testing.T) {
	c := qt.New(t)

	testDB, err := db.New(testMongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)

	ctx := context.Background()
	csp, err := New(ctx, &Config{
		DB:                       testDB,
		MailService:              testMailService,
		SMSService:               testSMSService,
		NotificationCoolDownTime: time.Second * 5,
		NotificationTTL:          time.Second * 30,
		RootKey:                  *testRootKey,
	})
	c.Assert(err, qt.IsNil)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	token, err := csp.BundleAuthToken(
		testBundleID,
		testUserID,
		testUserEmail,
		notifications.EmailChallenge,
		apicommon.DefaultLang,
		testOrgName,
		testOrgLogo,
		testAddress.Address(),
	)
	c.Assert(err, qt.IsNil)
	c.Assert(token, qt.Not(qt.IsNil))

	firstAuthData, err := csp.Storage.CSPAuth(token)
	c.Assert(err, qt.IsNil)
	expectedFirstCode, err := csp.regenerateTokenCode(firstAuthData.Secret)
	c.Assert(err, qt.IsNil)
	firstCode := fetchOTPCodeFromEmail(c, testUserEmail)
	c.Assert(firstCode, qt.Equals, expectedFirstCode)

	err = csp.ResendChallenge(
		token,
		testUserEmail,
		notifications.EmailChallenge,
		apicommon.DefaultLang,
		testOrgName,
		testOrgLogo,
		testAddress.Address(),
	)
	c.Assert(err, qt.IsNil)
	resendCode := fetchOTPCodeFromEmail(c, testUserEmail)
	c.Assert(resendCode, qt.Equals, expectedFirstCode)

	secondBundleID := internal.HexBytes("bundleID-2")
	secondToken, err := csp.BundleAuthToken(
		secondBundleID,
		testUserID,
		testUserEmail,
		notifications.EmailChallenge,
		apicommon.DefaultLang,
		testOrgName,
		testOrgLogo,
		testAddress.Address(),
	)
	c.Assert(err, qt.IsNil)
	c.Assert(secondToken, qt.Not(qt.IsNil))

	secondAuthData, err := csp.Storage.CSPAuth(secondToken)
	c.Assert(err, qt.IsNil)
	expectedSecondCode, err := csp.regenerateTokenCode(secondAuthData.Secret)
	c.Assert(err, qt.IsNil)
	secondCode := fetchOTPCodeFromEmail(c, testUserEmail)
	c.Assert(secondCode, qt.Equals, expectedSecondCode)
	c.Assert(secondCode, qt.Not(qt.Equals), resendCode)

	err = csp.VerifyBundleAuthToken(token, resendCode)
	c.Assert(err, qt.IsNil)
	err = csp.VerifyBundleAuthToken(secondToken, secondCode)
	c.Assert(err, qt.IsNil)

	authTokenResult, err := csp.Storage.CSPAuth(token)
	c.Assert(err, qt.IsNil)
	c.Assert(authTokenResult.Verified, qt.IsTrue)
	authTokenSecondResult, err := csp.Storage.CSPAuth(secondToken)
	c.Assert(err, qt.IsNil)
	c.Assert(authTokenSecondResult.Verified, qt.IsTrue)
}

func TestGenerateToken(t *testing.T) {
	c := qt.New(t)
	token, secret, code := new(CSP).generateToken()
	c.Assert(token, qt.Not(qt.IsNil))
	c.Assert(secret, qt.Not(qt.Equals), "")
	c.Assert(code, qt.Equals, gotp.NewDefaultHOTP(secret).At(0))
}

func TestVerifySolution(t *testing.T) {
	c := qt.New(t)

	secret := gotp.RandomSecret(16)
	code := gotp.NewDefaultHOTP(secret).At(0)

	ok := new(CSP).verifySolution(secret, code)
	c.Assert(ok, qt.IsTrue)

	ok = new(CSP).verifySolution(secret, "invalid")
	c.Assert(ok, qt.IsFalse)
}

func fetchOTPCodeFromEmail(c *qt.C, email string) string {
	var mailBody string
	var err error

	for range 25 {
		mailBody, err = testMailService.FindEmail(context.Background(), email)
		if err == nil {
			break
		}
		if err == io.EOF {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		c.Assert(err, qt.IsNil)
	}
	c.Assert(err, qt.IsNil)

	seedNotification, err := mailtemplates.VerifyOTPCodeNotification.Localized(apicommon.DefaultLang).
		ExecPlain(struct {
			Code             string
			Organization     string
			OrganizationLogo string
		}{`(.{6})`, testOrgName, testOrgLogo})
	c.Assert(err, qt.IsNil)

	rgxNotification := regexp.MustCompile(seedNotification.PlainBody)
	mailCode := rgxNotification.FindStringSubmatch(mailBody)
	c.Assert(mailCode, qt.HasLen, 2)
	return mailCode[1]
}
