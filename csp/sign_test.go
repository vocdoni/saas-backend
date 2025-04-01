package csp

import (
	"context"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/csp/signers"
	"github.com/vocdoni/saas-backend/csp/signers/saltedkey"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/util"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

func TestSign(t *testing.T) {
	c := qt.New(t)

	ctx := context.Background()
	csp, err := New(ctx, &Config{
		DBName:                   "testPrepareSaltedKeySigner",
		MongoClient:              dbClient,
		MailService:              testMailService,
		NotificationThrottleTime: time.Second,
		NotificationCoolDownTime: time.Second * 5,
		RootKey:                  *testRootKey,
	})
	c.Assert(err, qt.IsNil)

	c.Run("invalid signer type", func(c *qt.C) {
		_, err := csp.Sign(testToken, testAddress, testPID, "invalid")
		c.Assert(err, qt.ErrorIs, ErrInvalidSignerType)
	})

	c.Run("ecdsa salted success", func(c *qt.C) {
		pid := internal.HexBytes(util.RandomBytes(32))
		c.Cleanup(func() { _ = csp.Storage.Reset() })
		// index the token
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		// verify the token
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		sign, err := csp.Sign(testToken, testAddress, pid, signers.SignerTypeECDSASalted)
		c.Assert(err, qt.IsNil)
		c.Assert(sign, qt.Not(qt.IsNil))
		c.Assert(csp.isLocked(testUserID, pid), qt.IsFalse)
	})
}

func TestPrepareSaltedKeySigner(t *testing.T) {
	c := qt.New(t)

	ctx := context.Background()
	csp, err := New(ctx, &Config{
		DBName:                   "testPrepareSaltedKeySigner",
		MongoClient:              dbClient,
		MailService:              testMailService,
		NotificationThrottleTime: time.Second,
		NotificationCoolDownTime: time.Second * 5,
		RootKey:                  *testRootKey,
	})
	c.Assert(err, qt.IsNil)

	c.Run("not found token", func(c *qt.C) {
		c.Cleanup(func() {
			_ = csp.Storage.Reset()
			csp.unlock(testUserID, testPID)
		})
		_, _, _, err := csp.prepareSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.ErrorIs, ErrInvalidAuthToken)
	})

	c.Run("user already signing", func(c *qt.C) {
		c.Cleanup(func() {
			_ = csp.Storage.Reset()
			csp.unlock(testUserID, testPID)
		})
		// store the token and verify it
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
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
		// store the token
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		// store the token status
		c.Assert(csp.Storage.ConsumeCSPProcess(testToken, testPID, testAddress), qt.IsNil)
		_, _, _, err := csp.prepareSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.ErrorIs, ErrAuthTokenNotVerified)
	})

	c.Run("process already consumed", func(c *qt.C) {
		c.Cleanup(func() {
			_ = csp.Storage.Reset()
			csp.unlock(testUserID, testPID)
		})
		// store the token
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		// verify the token
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		// consume the process
		c.Assert(csp.Storage.ConsumeCSPProcess(testToken, testPID, testAddress), qt.IsNil)
		_, _, _, err := csp.prepareSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.ErrorIs, ErrProcessAlreadyConsumed)
	})

	c.Run("invalid salt pid", func(c *qt.C) {
		c.Cleanup(func() {
			_ = csp.Storage.Reset()
			csp.unlock(testUserID, testPID)
		})
		// index the token
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		// verify the token
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		_, _, _, err := csp.prepareSaltedKeySigner(testToken, testAddress, util.RandomBytes(saltedkey.SaltSize-1))
		c.Assert(err, qt.ErrorIs, ErrInvalidSalt)
	})

	c.Run("success", func(c *qt.C) {
		c.Cleanup(func() {
			_ = csp.Storage.Reset()
			csp.unlock(testUserID, testPID)
		})
		// index the token
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		// verify the token
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		userID, salt, message, err := csp.prepareSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.IsNil)
		c.Assert(userID, qt.DeepEquals, testUserID)
		c.Assert((*salt)[:], qt.DeepEquals, testPID.Bytes()[:saltedkey.SaltSize])
		c.Assert(message, qt.Not(qt.IsNil))
		var caBundle models.CAbundle
		err = proto.Unmarshal(message, &caBundle)
		c.Assert(err, qt.IsNil)
		c.Assert(caBundle.ProcessId, qt.DeepEquals, testPID.Bytes())
		c.Assert(caBundle.Address, qt.DeepEquals, testAddress.Bytes())
		c.Assert(csp.isLocked(testUserID, testPID), qt.IsTrue)
	})
}

func TestFinishSaltedKeySigner(t *testing.T) {
	c := qt.New(t)

	ctx := context.Background()
	csp, err := New(ctx, &Config{
		DBName:                   "testFinishSaltedKeySigner",
		MongoClient:              dbClient,
		MailService:              testMailService,
		NotificationThrottleTime: time.Second,
		NotificationCoolDownTime: time.Second * 5,
		RootKey:                  *testRootKey,
	})
	c.Assert(err, qt.IsNil)

	c.Run("not found token", func(c *qt.C) {
		err := csp.finishSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.ErrorIs, ErrInvalidAuthToken)
	})

	c.Run("token not verified", func(c *qt.C) {
		c.Cleanup(func() { _ = csp.Storage.Reset() })
		// store the token
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		err := csp.finishSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.ErrorIs, ErrAuthTokenNotVerified)
	})

	c.Run("user not signing", func(c *qt.C) {
		c.Cleanup(func() {
			_ = csp.Storage.Reset()
			defer csp.unlock(testUserID, testPID)
		})
		// store the token
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		// verify the token
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		err := csp.finishSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.ErrorIs, ErrUserIsNotAlreadySigning)
	})

	c.Run("success", func(c *qt.C) {
		c.Cleanup(func() {
			_ = csp.Storage.Reset()
			defer csp.unlock(testUserID, testPID)
		})
		// store the token
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		// verify the token
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		_, _, _, err := csp.prepareSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.IsNil)
		err = csp.finishSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.IsNil)

		status, err := csp.Storage.CSPProcess(testToken, testPID)
		c.Assert(err, qt.IsNil)
		c.Assert(status.Consumed, qt.IsTrue)
		c.Assert(status.ConsumedToken, qt.DeepEquals, testToken)
		c.Assert(status.ConsumedAddress, qt.DeepEquals, testAddress)
		c.Assert(status.ConsumedAt.IsZero(), qt.IsFalse)
		c.Assert(status.ConsumedAt.After(time.Now().Add(-time.Second)), qt.IsTrue)
	})
}
