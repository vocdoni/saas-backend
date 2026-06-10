package csp

import (
	"context"
	"crypto/sha256"
	"math/big"
	"testing"
	"time"

	blind "github.com/arnaucube/go-blindsecp256k1"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/csp/signers/saltedkey"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/test"
	"go.vocdoni.io/dvote/util"
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
		// get R but then exhaust the process
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
