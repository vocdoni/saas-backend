package csp

import (
	"context"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/csp/signers"
	"github.com/vocdoni/saas-backend/csp/signers/saltedkey"
	"github.com/vocdoni/saas-backend/csp/storage"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/util"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

func TestSign(t *testing.T) {
	c := qt.New(t)

	ctx := context.Background()
	csp, error := New(ctx, &CSPConfig{
		DBName:                   "testPrepareSaltedKeySigner",
		MongoClient:              dbClient,
		MailService:              testMailService,
		NotificationThrottleTime: time.Second,
		NotificationCoolDownTime: time.Second * 5,
		RootKey:                  *testRootKey,
	})
	c.Assert(error, qt.IsNil)

	c.Run("invalid signer type", func(c *qt.C) {
		_, err := csp.Sign(testToken, testAddress, testPID, "invalid")
		c.Assert(err, qt.ErrorIs, ErrInvalidSignerType)
	})

	c.Run("ecdsa salted success", func(c *qt.C) {
		pid := internal.HexBytes(util.RandomBytes(32))
		c.Cleanup(func() { _ = csp.Storage.Reset() })
		// create user with the bundles
		c.Assert(csp.Storage.SetUser(&storage.UserData{
			ID: testUserID,
			Bundles: map[string]storage.BundleData{
				testBundleID.String(): {
					ID: testBundleID,
					Processes: map[string]storage.ProcessData{
						pid.String(): {ID: pid},
					},
				},
			},
		}), qt.IsNil)
		// index the token
		c.Assert(csp.Storage.IndexAuthToken(testUserID, testBundleID, testToken), qt.IsNil)
		// verify the token
		c.Assert(csp.Storage.VerifyAuthToken(testToken), qt.IsNil)
		sign, err := csp.Sign(testToken, testAddress, pid, signers.SignerTypeECDSASalted)
		c.Assert(err, qt.IsNil)
		c.Assert(sign, qt.Not(qt.IsNil))
		c.Assert(csp.isLocked(testUserID, pid), qt.IsFalse)
	})
}

func TestPrepareSaltedKeySigner(t *testing.T) {
	c := qt.New(t)

	ctx := context.Background()
	csp, error := New(ctx, &CSPConfig{
		DBName:                   "testPrepareSaltedKeySigner",
		MongoClient:              dbClient,
		MailService:              testMailService,
		NotificationThrottleTime: time.Second,
		NotificationCoolDownTime: time.Second * 5,
		RootKey:                  *testRootKey,
	})
	c.Assert(error, qt.IsNil)

	c.Run("not found token", func(c *qt.C) {
		_, _, _, err := csp.prepareSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.ErrorIs, ErrInvalidAuthToken)
	})

	c.Run("user already signing", func(c *qt.C) {
		c.Cleanup(func() {
			_ = csp.Storage.Reset()
			csp.unlock(testUserID, testPID)
		})
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
		}), qt.IsNil)
		// index the token
		c.Assert(csp.Storage.IndexAuthToken(testUserID, testBundleID, testToken), qt.IsNil)
		// lock the user
		csp.lock(testUserID, testPID)
		_, _, _, err := csp.prepareSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.ErrorIs, ErrUserAlreadySigning)
	})

	c.Run("token not verified", func(c *qt.C) {
		c.Cleanup(func() {
			_ = csp.Storage.Reset()
			csp.unlock(testUserID, testPID)
		})
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
		}), qt.IsNil)
		// index the token
		c.Assert(csp.Storage.IndexAuthToken(testUserID, testBundleID, testToken), qt.IsNil)
		_, _, _, err := csp.prepareSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.ErrorIs, ErrAuthTokenNotVerified)
	})

	c.Run("user not in bundle", func(c *qt.C) {
		c.Skip("not reachable")
	})

	c.Run("user not in process", func(c *qt.C) {
		c.Cleanup(func() {
			_ = csp.Storage.Reset()
			csp.unlock(testUserID, testPID)
		})
		// create user with the bundle
		c.Assert(csp.Storage.SetUser(&storage.UserData{
			ID: testUserID,
			Bundles: map[string]storage.BundleData{
				testBundleID.String(): {
					ID: testBundleID,
					Processes: map[string]storage.ProcessData{
						"otherProcess": {ID: testPID},
					},
				},
			},
		}), qt.IsNil)
		// index the token
		c.Assert(csp.Storage.IndexAuthToken(testUserID, testBundleID, testToken), qt.IsNil)
		// verify the token
		c.Assert(csp.Storage.VerifyAuthToken(testToken), qt.IsNil)
		_, _, _, err := csp.prepareSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.ErrorIs, ErrUserNotBelongsToProcess)
	})

	c.Run("process already consumed", func(c *qt.C) {
		c.Cleanup(func() {
			_ = csp.Storage.Reset()
			csp.unlock(testUserID, testPID)
		})
		// create user with the bundle
		c.Assert(csp.Storage.SetUser(&storage.UserData{
			ID: testUserID,
			Bundles: map[string]storage.BundleData{
				testBundleID.String(): {
					ID: testBundleID,
					Processes: map[string]storage.ProcessData{
						testPID.String(): {ID: testPID, Consumed: true},
					},
				},
			},
		}), qt.IsNil)
		// index the token
		c.Assert(csp.Storage.IndexAuthToken(testUserID, testBundleID, testToken), qt.IsNil)
		// verify the token
		c.Assert(csp.Storage.VerifyAuthToken(testToken), qt.IsNil)
		_, _, _, err := csp.prepareSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.ErrorIs, ErrProcessAlreadyConsumed)
	})

	c.Run("invalid salt pid", func(c *qt.C) {
		c.Cleanup(func() {
			_ = csp.Storage.Reset()
			csp.unlock(testUserID, testPID)
		})
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
		}), qt.IsNil)
		// index the token
		c.Assert(csp.Storage.IndexAuthToken(testUserID, testBundleID, testToken), qt.IsNil)
		// verify the token
		c.Assert(csp.Storage.VerifyAuthToken(testToken), qt.IsNil)
		_, _, _, err := csp.prepareSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.ErrorIs, ErrInvalidSalt)
	})

	c.Run("success", func(c *qt.C) {
		pid := internal.HexBytes(util.RandomBytes(32))
		c.Cleanup(func() {
			_ = csp.Storage.Reset()
			csp.unlock(testUserID, testPID)
		})
		// create user with the bundle
		c.Assert(csp.Storage.SetUser(&storage.UserData{
			ID: testUserID,
			Bundles: map[string]storage.BundleData{
				testBundleID.String(): {
					ID: testBundleID,
					Processes: map[string]storage.ProcessData{
						pid.String(): {ID: pid},
					},
				},
			},
		}), qt.IsNil)
		// index the token
		c.Assert(csp.Storage.IndexAuthToken(testUserID, testBundleID, testToken), qt.IsNil)
		// verify the token
		c.Assert(csp.Storage.VerifyAuthToken(testToken), qt.IsNil)
		userID, salt, message, err := csp.prepareSaltedKeySigner(testToken, testAddress, pid)
		c.Assert(err, qt.IsNil)
		c.Assert(userID, qt.DeepEquals, testUserID)
		c.Assert((*salt)[:], qt.DeepEquals, pid.Bytes()[:saltedkey.SaltSize])
		c.Assert(message, qt.Not(qt.IsNil))
		var caBundle models.CAbundle
		err = proto.Unmarshal(message, &caBundle)
		c.Assert(err, qt.IsNil)
		c.Assert(caBundle.ProcessId, qt.DeepEquals, pid.Bytes())
		c.Assert(caBundle.Address, qt.DeepEquals, testAddress.Bytes())
		c.Assert(csp.isLocked(testUserID, pid), qt.IsTrue)
	})
}

func TestFinishSaltedKeySigner(t *testing.T) {
	c := qt.New(t)

	ctx := context.Background()
	csp, error := New(ctx, &CSPConfig{
		DBName:                   "testFinishSaltedKeySigner",
		MongoClient:              dbClient,
		MailService:              testMailService,
		NotificationThrottleTime: time.Second,
		NotificationCoolDownTime: time.Second * 5,
		RootKey:                  *testRootKey,
	})
	c.Assert(error, qt.IsNil)

	c.Run("not found token", func(c *qt.C) {
		err := csp.finishSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.ErrorIs, ErrInvalidAuthToken)
	})

	c.Run("token not verified", func(c *qt.C) {
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
		}), qt.IsNil)
		// index the token
		c.Assert(csp.Storage.IndexAuthToken(testUserID, testBundleID, testToken), qt.IsNil)
		err := csp.finishSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.ErrorIs, ErrAuthTokenNotVerified)
	})

	c.Run("user not signing", func(c *qt.C) {
		c.Cleanup(func() {
			_ = csp.Storage.Reset()
			defer csp.unlock(testUserID, testPID)
		})
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
		}), qt.IsNil)
		// index the token
		c.Assert(csp.Storage.IndexAuthToken(testUserID, testBundleID, testToken), qt.IsNil)
		// verify the token
		c.Assert(csp.Storage.VerifyAuthToken(testToken), qt.IsNil)
		err := csp.finishSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.ErrorIs, ErrUserIsNotAlreadySigning)
	})

	c.Run("user not in bundle", func(c *qt.C) {
		c.Skip("not reachable")
	})

	c.Run("user not in process", func(c *qt.C) {
		c.Cleanup(func() {
			_ = csp.Storage.Reset()
			defer csp.unlock(testUserID, testPID)
		})
		// create user with the bundle
		c.Assert(csp.Storage.SetUser(&storage.UserData{
			ID: testUserID,
			Bundles: map[string]storage.BundleData{
				testBundleID.String(): {
					ID: testBundleID,
					Processes: map[string]storage.ProcessData{
						"otherProcess": {ID: testPID},
					},
				},
			},
		}), qt.IsNil)
		// index the token
		c.Assert(csp.Storage.IndexAuthToken(testUserID, testBundleID, testToken), qt.IsNil)
		// verify the token
		c.Assert(csp.Storage.VerifyAuthToken(testToken), qt.IsNil)
		// lock the user
		csp.lock(testUserID, testPID)
		err := csp.finishSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.ErrorIs, ErrUserNotBelongsToProcess)
	})

	c.Run("success", func(c *qt.C) {
		pid := internal.HexBytes(util.RandomBytes(32))
		c.Cleanup(func() {
			_ = csp.Storage.Reset()
			defer csp.unlock(testUserID, testPID)
		})
		// create user with the bundle
		c.Assert(csp.Storage.SetUser(&storage.UserData{
			ID: testUserID,
			Bundles: map[string]storage.BundleData{
				testBundleID.String(): {
					ID: testBundleID,
					Processes: map[string]storage.ProcessData{
						pid.String(): {ID: pid},
					},
				},
			},
		}), qt.IsNil)
		// index the token
		c.Assert(csp.Storage.IndexAuthToken(testUserID, testBundleID, testToken), qt.IsNil)
		// verify the token
		c.Assert(csp.Storage.VerifyAuthToken(testToken), qt.IsNil)
		_, _, _, err := csp.prepareSaltedKeySigner(testToken, testAddress, pid)
		c.Assert(err, qt.IsNil)
		err = csp.finishSaltedKeySigner(testToken, testAddress, pid)
		c.Assert(err, qt.IsNil)
		userData, err := csp.Storage.User(testUserID)
		c.Assert(err, qt.IsNil)
		c.Assert(userData.Bundles[testBundleID.String()].Processes[pid.String()].Consumed, qt.IsTrue)
		c.Assert(userData.Bundles[testBundleID.String()].Processes[pid.String()].WithToken, qt.DeepEquals, testToken)
		c.Assert(userData.Bundles[testBundleID.String()].Processes[pid.String()].WithAddress, qt.DeepEquals, testAddress)
		c.Assert(userData.Bundles[testBundleID.String()].Processes[pid.String()].At.IsZero(), qt.IsFalse)
		at := userData.Bundles[testBundleID.String()].Processes[pid.String()].At
		c.Assert(at.IsZero(), qt.IsFalse)
		c.Assert(at.After(time.Now().Add(-time.Second)), qt.IsTrue)
	})
}
