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
	"github.com/vocdoni/saas-backend/csp/storage"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
	"github.com/xlzd/gotp"
)

func TestBundleAuthToken(t *testing.T) {
	c := qt.New(t)

	ctx := context.Background()
	csp, error := New(ctx, &CSPConfig{
		DBName:                   "testBundleAuthToken",
		MongoClient:              dbClient,
		MailService:              testMailService,
		NotificationThrottleTime: time.Second,
		NotificationCoolDownTime: time.Second * 5,
		RootKey:                  *testRootKey,
	})
	c.Assert(error, qt.IsNil)

	c.Run("empty bundleID", func(c *qt.C) {
		_, err := csp.BundleAuthToken(nil, testUserID, testUserEmail, notifications.EmailChallenge)
		c.Assert(err, qt.ErrorIs, ErrNoBundleID)
	})

	c.Run("empty userID", func(c *qt.C) {
		_, err := csp.BundleAuthToken(testBundleID, nil, testUserEmail, notifications.EmailChallenge)
		c.Assert(err, qt.ErrorIs, ErrNoUserID)
	})

	c.Run("user data not found", func(c *qt.C) {
		_, err := csp.BundleAuthToken(testBundleID, testUserID, testUserEmail, notifications.EmailChallenge)
		c.Assert(err, qt.ErrorIs, ErrUserUnknown)
	})

	c.Run("bundle not found in user data", func(c *qt.C) {
		c.Cleanup(func() { _ = csp.Storage.Reset() })
		// add user with no bundles and no mail
		testUserData := &storage.UserData{
			ID:        testUserID,
			Bundles:   map[string]storage.BundleData{},
			ExtraData: testUserExtraData,
		}
		c.Assert(csp.Storage.SetUser(testUserData), qt.IsNil)
		// test bundle is not found in user data
		_, err := csp.BundleAuthToken(testBundleID, testUserID, testUserEmail, notifications.EmailChallenge)
		c.Assert(err, qt.ErrorIs, ErrUserNotBelongsToBundle)
	})

	c.Run("update user data fails", func(c *qt.C) {
		c.Cleanup(func() { _ = csp.Storage.Reset() })
		// add user with no bundles and no mail
		testUserData := &storage.UserData{
			ID:        testUserID,
			Bundles:   map[string]storage.BundleData{},
			ExtraData: testUserExtraData,
		}
		c.Assert(csp.Storage.SetUser(testUserData), qt.IsNil)
		// add bundle to user
		err := csp.Storage.SetUserBundle(testUserID, testBundleID, testPID)
		c.Assert(err, qt.IsNil)
		// test update user data fails (unreachable but testeable)
		_, err = csp.BundleAuthToken(testBundleID, testUserID, "", notifications.EmailChallenge)
		c.Assert(err, qt.ErrorIs, ErrNotificationFailure)
		userResult, err := csp.Storage.User(testUserID)
		c.Assert(err, qt.IsNil)
		c.Assert(userResult.Bundles[testBundleID.String()].LastAttempt, qt.Not(qt.IsNil))
	})

	c.Run("success test", func(c *qt.C) {
		c.Cleanup(func() { _ = csp.Storage.Reset() })
		bundleID := internal.HexBytes(testBundleID)
		c.Assert(csp.Storage.SetUser(&storage.UserData{
			ID: testUserID,
			Bundles: map[string]storage.BundleData{
				bundleID.String(): {
					ID: testBundleID,
					Processes: map[string]storage.ProcessData{
						testPID.String(): {ID: testPID},
					},
				},
			},
			ExtraData: testUserExtraData,
		}), qt.IsNil)
		userResult, err := csp.Storage.User(testUserID)
		c.Assert(err, qt.IsNil)
		token, err := csp.BundleAuthToken(testBundleID, testUserID, testUserEmail, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		c.Assert(token, qt.Not(qt.IsNil))
		// calculate expected code and token
		_, expectedCode, err := csp.generateToken(testUserID, userResult.Bundles[bundleID.String()])
		c.Assert(err, qt.IsNil)
		authTokenResult, _, err := csp.Storage.UserAuthToken(token)
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

	ctx := context.Background()
	csp, error := New(ctx, &CSPConfig{
		DBName:                   "testVerifyBundleAuthToken",
		MongoClient:              dbClient,
		MailService:              testMailService,
		NotificationThrottleTime: time.Second,
		NotificationCoolDownTime: time.Second * 5,
		RootKey:                  *testRootKey,
	})
	c.Assert(error, qt.IsNil)

	// test cases:
	// 1. empty token
	// 2. token not found
	// 4. token bundle not found in user data
	// 5. last attempt is updated
	// 6. solution not match
	// 7. token verified

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
		c.Assert(err, qt.ErrorIs, ErrUserUnknown)
	})

	c.Run("token bundle not found in user data", func(c *qt.C) {
		c.Cleanup(func() { _ = csp.Storage.Reset() })
		// create user with the bundle
		c.Assert(csp.Storage.SetUser(&storage.UserData{
			ID: testUserID,
			Bundles: map[string]storage.BundleData{
				testBundleID.String(): {
					ID: testBundleID,
					Processes: map[string]storage.ProcessData{
						testPID.String(): {ID: testPID},
					},
				},
			},
			ExtraData: testUserExtraData,
		}), qt.IsNil)
		// index the token
		c.Assert(csp.Storage.IndexAuthToken(testUserID, testBundleID, testToken), qt.IsNil)
		// remove the bundle from the user data
		c.Assert(csp.Storage.SetUser(&storage.UserData{
			ID:        testUserID,
			Bundles:   map[string]storage.BundleData{},
			ExtraData: testUserExtraData,
		}), qt.IsNil)
		// verify the token
		err := csp.VerifyBundleAuthToken(testToken, "invalid")
		c.Assert(err, qt.ErrorIs, ErrUserNotBelongsToBundle)
	})

	c.Run("solution not match", func(c *qt.C) {
		c.Cleanup(func() { _ = csp.Storage.Reset() })
		// create user with the bundle
		c.Assert(csp.Storage.SetUser(&storage.UserData{
			ID: testUserID,
			Bundles: map[string]storage.BundleData{
				testBundleID.String(): {
					ID: testBundleID,
					Processes: map[string]storage.ProcessData{
						testPID.String(): {ID: testPID},
					},
				},
			},
			ExtraData: testUserExtraData,
		}), qt.IsNil)
		// index the token
		c.Assert(csp.Storage.IndexAuthToken(testUserID, testBundleID, testToken), qt.IsNil)
		// try to verify an invalid solution
		err := csp.VerifyBundleAuthToken(testToken, "invalid")
		c.Assert(err, qt.ErrorIs, ErrChallengeCodeFailure)
		// check that the last attempt is updated
		userResult, err := csp.Storage.User(testUserID)
		c.Assert(err, qt.IsNil)
		c.Assert(userResult.Bundles[testBundleID.String()].LastAttempt, qt.Not(qt.IsNil))
	})

	c.Run("success", func(c *qt.C) {
		c.Cleanup(func() { _ = csp.Storage.Reset() })
		// create user with the bundle
		c.Assert(csp.Storage.SetUser(&storage.UserData{
			ID: testUserID,
			Bundles: map[string]storage.BundleData{
				testBundleID.String(): {
					ID: testBundleID,
					Processes: map[string]storage.ProcessData{
						testPID.String(): {ID: testPID},
					},
				},
			},
			ExtraData: testUserExtraData,
		}), qt.IsNil)
		// index the token
		c.Assert(csp.Storage.IndexAuthToken(testUserID, testBundleID, testToken), qt.IsNil)
		// generate the code
		_, code, err := csp.generateToken(testUserID, storage.BundleData{
			ID:        testBundleID,
			Processes: make(map[string]storage.ProcessData),
		})
		c.Assert(err, qt.IsNil)
		// try to verify an valid solution
		err = csp.VerifyBundleAuthToken(testToken, code)
		c.Assert(err, qt.IsNil)
		// check that the token is verified
		authTokenResult, _, err := csp.Storage.UserAuthToken(testToken)
		c.Assert(err, qt.IsNil)
		c.Assert(authTokenResult.Verified, qt.IsTrue)
	})
}

func TestGenerateToken(t *testing.T) {
	c := qt.New(t)

	testBundle := storage.BundleData{
		ID: testBundleID,
		Processes: map[string]storage.ProcessData{
			testPID.String(): {ID: testPID},
		},
	}
	secret := otpSecret(testUserID, testBundleID)
	otp := gotp.NewDefaultHOTP(secret)
	token, code, err := new(CSP).generateToken(testUserID, testBundle)
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
