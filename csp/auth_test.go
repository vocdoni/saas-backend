package csp

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
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
		NotificationThrottleTime: time.Second,
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
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		// create the token
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		// try to verify an invalid solution
		err := csp.VerifyBundleAuthToken(testToken, "invalid")
		c.Assert(err, qt.ErrorIs, ErrChallengeCodeFailure)
	})

	c.Run("success", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
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

func TestResendChallenge(t *testing.T) {
	c := qt.New(t)

	testDB, err := db.New(testMongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)

	ctx := context.Background()
	csp, err := New(ctx, &Config{
		DB:                       testDB,
		MailService:              testMailService,
		SMSService:               testSMSService,
		NotificationThrottleTime: time.Second,
		NotificationCoolDownTime: time.Second * 5,
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
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
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

	c.Run("success", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)

		expectedCode, err := csp.regenerateTokenCode(testUserID, testBundleID)
		c.Assert(err, qt.IsNil)

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

func TestBundleAuthTokenResendAndVerifyFlow(t *testing.T) {
	c := qt.New(t)

	testDB, err := db.New(testMongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)

	ctx := context.Background()
	csp, err := New(ctx, &Config{
		DB:                       testDB,
		MailService:              testMailService,
		SMSService:               testSMSService,
		NotificationThrottleTime: time.Second,
		NotificationCoolDownTime: time.Second * 5,
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

	expectedFirstCode, err := csp.regenerateTokenCode(testUserID, testBundleID)
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

	expectedSecondCode, err := csp.regenerateTokenCode(testUserID, secondBundleID)
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
