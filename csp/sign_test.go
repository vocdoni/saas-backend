package csp

import (
	"context"
	"crypto/sha256"
	"math/big"
	"testing"
	"time"

	blind "github.com/arnaucube/go-blindsecp256k1"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/csp/signers"
	"github.com/vocdoni/saas-backend/csp/signers/saltedkey"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/test"
	"go.vocdoni.io/dvote/util"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

func newTestCSP(t *testing.T) (*CSP, *db.MongoStorage) {
	t.Helper()
	testDB, err := db.New(testMongoURI, test.RandomDatabaseName())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c, err := New(ctx, &Config{
		DB:                       testDB,
		MailService:              testMailService,
		SMSService:               testSMSService,
		NotificationThrottleTime: time.Second,
		NotificationCoolDownTime: time.Second * 5,
		RootKey:                  *testRootKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, testDB
}

func TestSign(t *testing.T) {
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

	c.Run("invalid signer type", func(c *qt.C) {
		_, err := csp.Sign(testToken, testAddress, testPID, testUserWeightBytes, "invalid")
		c.Assert(err, qt.ErrorIs, ErrInvalidSignerType)
	})

	c.Run("ecdsa salted success", func(c *qt.C) {
		pid := internal.HexBytes(util.RandomBytes(32))
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		// index the token
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		// verify the token
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		sign, err := csp.Sign(testToken, testAddress, pid, testUserWeightBytes, signers.SignerTypeECDSASalted)
		c.Assert(err, qt.IsNil)
		c.Assert(sign, qt.Not(qt.IsNil))
		c.Assert(csp.isLocked(testUserID, pid), qt.IsFalse)
	})
}

func TestPrepareSaltedKeySigner(t *testing.T) {
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

	c.Run("not found token", func(c *qt.C) {
		c.Cleanup(func() {
			c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
			csp.unlock(testUserID, testPID)
		})
		_, _, _, err := csp.prepareSaltedKeySigner(testToken, testAddress, testPID, testUserWeightBytes)
		c.Assert(err, qt.ErrorIs, ErrInvalidAuthToken)
	})

	c.Run("user already signing", func(c *qt.C) {
		c.Cleanup(func() {
			c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
			csp.unlock(testUserID, testPID)
		})
		// store the token and verify it
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		// lock the user
		csp.lock(testUserID, testPID)
		_, _, _, err := csp.prepareSaltedKeySigner(testToken, testAddress, testPID, testUserWeightBytes)
		c.Assert(err, qt.ErrorIs, ErrUserAlreadySigning)
	})

	c.Run("token not verified", func(c *qt.C) {
		c.Cleanup(func() {
			c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
			csp.unlock(testUserID, testPID)
		})
		// store the token
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		// store the token status
		c.Assert(csp.Storage.ConsumeCSPProcess(testToken, testPID, testAddress), qt.IsNil)
		_, _, _, err := csp.prepareSaltedKeySigner(testToken, testAddress, testPID, testUserWeightBytes)
		c.Assert(err, qt.ErrorIs, ErrAuthTokenNotVerified)
	})

	c.Run("process already consumed", func(c *qt.C) {
		c.Cleanup(func() {
			c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
			csp.unlock(testUserID, testPID)
		})
		// store the token
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		// verify the token
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		// consume the process
		for i := 0; i <= 10; i++ {
			c.Assert(csp.Storage.ConsumeCSPProcess(testToken, testPID, testAddress), qt.IsNil)
		}
		_, _, _, err := csp.prepareSaltedKeySigner(testToken, testAddress, testPID, testUserWeightBytes)
		c.Assert(err, qt.ErrorIs, ErrProcessAlreadyConsumed)
	})

	c.Run("invalid salt pid", func(c *qt.C) {
		c.Cleanup(func() {
			c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
			csp.unlock(testUserID, testPID)
		})
		// index the token
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		// verify the token
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		_, _, _, err := csp.prepareSaltedKeySigner(testToken, testAddress, util.RandomBytes(saltedkey.SaltSize-1), testUserWeightBytes)
		c.Assert(err, qt.ErrorIs, ErrInvalidSalt)
	})

	c.Run("success", func(c *qt.C) {
		c.Cleanup(func() {
			c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
			csp.unlock(testUserID, testPID)
		})
		// index the token
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		// verify the token
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		userID, salt, message, err := csp.prepareSaltedKeySigner(testToken, testAddress, testPID, testUserWeightBytes)
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

	c.Run("not found token", func(c *qt.C) {
		err := csp.finishSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.ErrorIs, ErrInvalidAuthToken)
	})

	c.Run("token not verified", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		// store the token
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		err := csp.finishSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.ErrorIs, ErrAuthTokenNotVerified)
	})

	c.Run("user not signing", func(c *qt.C) {
		c.Cleanup(func() {
			c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
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
			c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
			defer csp.unlock(testUserID, testPID)
		})
		// store the token
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		// verify the token
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		_, _, _, err := csp.prepareSaltedKeySigner(testToken, testAddress, testPID, testUserWeightBytes)
		c.Assert(err, qt.IsNil)
		err = csp.finishSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.IsNil)

		status, err := csp.Storage.CSPProcess(testToken, testPID)
		c.Assert(err, qt.IsNil)
		c.Assert(status.Used, qt.IsTrue)
		c.Assert(status.UsedToken, qt.DeepEquals, testToken)
		c.Assert(status.UsedAddress, qt.DeepEquals, testAddress)
		c.Assert(status.UsedAt.IsZero(), qt.IsFalse)
		c.Assert(status.UsedAt.After(time.Now().Add(-time.Second)), qt.IsTrue)
		c.Assert(status.TimesVoted, qt.Equals, 1)
	})
}

func TestGetBlindR(t *testing.T) {
	c := qt.New(t)

	csp, testDB := newTestCSP(t)

	c.Run("invalid auth token", func(c *qt.C) {
		_, _, err := csp.GetBlindR(testToken, testPID, 1)
		c.Assert(err, qt.ErrorIs, ErrInvalidAuthToken)
	})

	c.Run("token not verified", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		_, _, err := csp.GetBlindR(testToken, testPID, 1)
		c.Assert(err, qt.ErrorIs, ErrAuthTokenNotVerified)
	})

	c.Run("process already consumed", func(c *qt.C) {
		pid := internal.HexBytes(util.RandomBytes(32))
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		for i := 0; i <= 10; i++ {
			c.Assert(csp.Storage.ConsumeCSPProcessBlind(testToken, pid), qt.IsNil)
		}
		_, _, err := csp.GetBlindR(testToken, pid, 1)
		c.Assert(err, qt.ErrorIs, ErrProcessAlreadyConsumed)
	})

	c.Run("invalid salt (pid too short)", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		_, _, err := csp.GetBlindR(testToken, util.RandomBytes(saltedkey.SaltSize-1), 1)
		c.Assert(err, qt.ErrorIs, ErrInvalidSalt)
	})

	c.Run("success", func(c *qt.C) {
		pid := internal.HexBytes(util.RandomBytes(32))
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		tokenR, weightCert, err := csp.GetBlindR(testToken, pid, testUserWeight)
		c.Assert(err, qt.IsNil)
		c.Assert(tokenR, qt.Not(qt.IsNil))
		c.Assert(tokenR, qt.HasLen, 33) // compressed secp256k1 point
		c.Assert(weightCert, qt.Not(qt.IsNil))
		c.Assert(weightCert, qt.Not(qt.HasLen), 0)
	})
}

func TestSignBlindMsg(t *testing.T) {
	c := qt.New(t)

	csp, testDB := newTestCSP(t)

	c.Run("invalid auth token", func(c *qt.C) {
		_, err := csp.SignBlindMsg(testToken, testPID, []byte("blinded"))
		c.Assert(err, qt.ErrorIs, ErrInvalidAuthToken)
	})

	c.Run("token not verified", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		_, err := csp.SignBlindMsg(testToken, testPID, []byte("blinded"))
		c.Assert(err, qt.ErrorIs, ErrAuthTokenNotVerified)
	})

	c.Run("no pending blind session", func(c *qt.C) {
		pid := internal.HexBytes(util.RandomBytes(32))
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		_, err := csp.SignBlindMsg(testToken, pid, []byte("blinded"))
		c.Assert(err, qt.ErrorIs, ErrBlindRNotFound)
	})

	c.Run("process already consumed", func(c *qt.C) {
		pid := internal.HexBytes(util.RandomBytes(32))
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		_, _, err := csp.GetBlindR(testToken, pid, 1)
		c.Assert(err, qt.IsNil)
		for i := 0; i <= 10; i++ {
			c.Assert(csp.Storage.ConsumeCSPProcessBlind(testToken, pid), qt.IsNil)
		}
		_, err = csp.SignBlindMsg(testToken, pid, []byte("blinded"))
		c.Assert(err, qt.ErrorIs, ErrProcessAlreadyConsumed)
	})

	c.Run("full round-trip", func(c *qt.C) {
		pid := internal.HexBytes(util.RandomBytes(32))
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID), qt.IsNil)
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)

		// step 1: server returns R
		tokenR, _, err := csp.GetBlindR(testToken, pid, testUserWeight)
		c.Assert(err, qt.IsNil)

		// client: decompress R, blind the message
		r, err := blind.NewPointFromBytes(tokenR)
		c.Assert(err, qt.IsNil)
		msgHash := sha256.Sum256(testAddress)
		msgBlinded, userSecretData, err := blind.Blind(new(big.Int).SetBytes(msgHash[:]), r)
		c.Assert(err, qt.IsNil)

		// step 2: server blind-signs
		blindSig, err := csp.SignBlindMsg(testToken, pid, msgBlinded.Bytes())
		c.Assert(err, qt.IsNil)
		c.Assert(blindSig, qt.Not(qt.IsNil))

		// client: unblind the signature
		sig := blind.Unblind(new(big.Int).SetBytes(blindSig), userSecretData)

		// verify against the salted pub key
		salt := [saltedkey.SaltSize]byte{}
		copy(salt[:], pid[:saltedkey.SaltSize])
		saltedPub, err := saltedkey.SaltBlindPubKey(csp.Signer.BlindPubKey(), salt)
		c.Assert(err, qt.IsNil)
		c.Assert(blind.Verify(new(big.Int).SetBytes(msgHash[:]), sig, saltedPub), qt.IsTrue)

		// process is consumed; a second sign attempt must fail
		_, err = csp.SignBlindMsg(testToken, pid, msgBlinded.Bytes())
		c.Assert(err, qt.ErrorIs, ErrBlindRNotFound)
	})
}
