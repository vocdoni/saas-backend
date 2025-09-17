package csp

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"regexp"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/db"
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
		NotificationThrottleTime: time.Second,
		NotificationCoolDownTime: time.Second * 5,
		RootKey:                  *testRootKey,
	})
	c.Assert(err, qt.IsNil)

	c.Run("empty bundleID", func(c *qt.C) {
		_, err := csp.BundleAuthToken(nil, testUserID, testUserEmail, notifications.EmailChallenge)
		c.Assert(err, qt.ErrorIs, ErrNoBundleID)
	})

	c.Run("empty userID", func(c *qt.C) {
		_, err := csp.BundleAuthToken(testBundleID, nil, testUserEmail, notifications.EmailChallenge)
		c.Assert(err, qt.ErrorIs, ErrNoUserID)
	})

	c.Run("notification cooldown reached", func(c *qt.C) {
		c.Cleanup(func() { _ = csp.Storage.Reset() })
		// generate a valid token
		_, err := csp.BundleAuthToken(testBundleID, testUserID, "", notifications.EmailChallenge)
		c.Assert(err, qt.ErrorIs, ErrNotificationFailure)
		// try to generate a new token before the cooldown time
		_, err = csp.BundleAuthToken(testBundleID, testUserID, "", notifications.EmailChallenge)
		c.Assert(err, qt.ErrorIs, ErrAttemptCoolDownTime)
	})

	c.Run("success test", func(c *qt.C) {
		c.Cleanup(func() { _ = csp.Storage.Reset() })
		bundleID := internal.HexBytes(testBundleID)
		token, err := csp.BundleAuthToken(testBundleID, testUserID, testUserEmail, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		c.Assert(token, qt.Not(qt.IsNil))
		// calculate expected code and token
		_, expectedCode, err := csp.generateToken(testUserID, bundleID)
		c.Assert(err, qt.IsNil)
		authTokenResult, err := csp.Storage.CSPAuth(token)
		c.Assert(err, qt.IsNil)
		c.Assert(authTokenResult.BundleID.Bytes(), qt.DeepEquals, bundleID.Bytes())
		c.Assert(authTokenResult.UserID.Bytes(), qt.DeepEquals, testUserID.Bytes())
		// wait to dequeue the notification
		time.Sleep(time.Second * 3)
		// get the verification code from the email
		mailBody, err := testMailService.FindEmail(context.Background(), testUserEmail)
		c.Assert(err, qt.IsNil)
		// parse the email body to get the verification code
		seedNotification, err := mailtemplates.VerifyOTPCodeNotification.ExecPlain(struct{ Code string }{`(.{6})`})
		c.Assert(err, qt.IsNil)
		rgxNotification := regexp.MustCompile(seedNotification.PlainBody)
		// verify the user
		mailCode := rgxNotification.FindStringSubmatch(mailBody)
		c.Assert(mailCode, qt.HasLen, 2)
		c.Assert(mailCode[1], qt.Equals, expectedCode)
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
		NotificationThrottleTime: time.Second,
		NotificationCoolDownTime: time.Second * 5,
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

	c.Run("solution not match", func(c *qt.C) {
		c.Cleanup(func() { _ = csp.Storage.Reset() })
		// create the token
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		// try to verify an invalid solution
		err := csp.VerifyBundleAuthToken(testToken, "invalid")
		c.Assert(err, qt.ErrorIs, ErrChallengeCodeFailure)
	})

	c.Run("success", func(c *qt.C) {
		c.Cleanup(func() { _ = csp.Storage.Reset() })
		// create the token
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		// generate the code
		_, code, err := csp.generateToken(testUserID, testBundleID)
		c.Assert(err, qt.IsNil)
		// try to verify an valid solution
		err = csp.VerifyBundleAuthToken(testToken, code)
		c.Assert(err, qt.IsNil)
		// check that the token is verified
		authTokenResult, err := csp.Storage.CSPAuth(testToken)
		c.Assert(err, qt.IsNil)
		c.Assert(authTokenResult.Verified, qt.IsTrue)
	})
}

func TestGenerateToken(t *testing.T) {
	c := qt.New(t)
	secret := otpSecret(testUserID, testBundleID)
	otp := gotp.NewDefaultHOTP(secret)
	token, code, err := new(CSP).generateToken(testUserID, testBundleID)
	c.Assert(err, qt.IsNil)
	c.Assert(token, qt.Not(qt.IsNil))
	c.Assert(code, qt.Equals, otp.At(0))
}

func TestVerifySolution(t *testing.T) {
	c := qt.New(t)

	secret := otpSecret(testUserID, testBundleID)
	otp := gotp.NewDefaultHOTP(secret)
	code := otp.At(0)

	ok := new(CSP).verifySolution(testUserID, testBundleID, code)
	c.Assert(ok, qt.IsTrue)

	ok = new(CSP).verifySolution(testUserID, testBundleID, "invalid")
	c.Assert(ok, qt.IsFalse)
}

func TestOTPSecret(t *testing.T) {
	c := qt.New(t)

	expectedSecret := sha256.Sum256(append(testUserID, testBundleID...))
	encodedSecret := base32.StdEncoding.EncodeToString(expectedSecret[:])
	secret := otpSecret(testUserID, testBundleID)
	c.Assert(secret, qt.Equals, encodedSecret)
}
