package storage

import (
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/internal"
)

var (
	testAuthToken    = internal.HexBytes(uuid.New().String())
	invalidAuthToken = internal.HexBytes(uuid.New().String())
	testUserID       = internal.HexBytes([]byte("123456"))
	testUserAddress  = internal.HexBytes([]byte("address"))
	testCSPBundleID  = internal.HexBytes([]byte("bundleID"))
	testCSPProcessID = internal.HexBytes([]byte("processID"))
)

func TestSetGetCSPAuthToken(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.Reset(), qt.IsNil) })

	c.Run("nil token", func(c *qt.C) {
		c.Assert(testDB.SetCSPAuthToken(nil, testUserID, testCSPBundleID), qt.ErrorIs, ErrBadInputs)
	})
	c.Run("nil userID", func(c *qt.C) {
		c.Assert(testDB.SetCSPAuthToken(testAuthToken, nil, testCSPBundleID), qt.ErrorIs, ErrBadInputs)
	})
	c.Run("nil bundleID", func(c *qt.C) {
		c.Assert(testDB.SetCSPAuthToken(testAuthToken, testUserID, nil), qt.ErrorIs, ErrBadInputs)
	})
	c.Run("valid token", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.Reset(), qt.IsNil) })
		// set the token and check it was set
		c.Assert(testDB.SetCSPAuthToken(testAuthToken, testUserID, testCSPBundleID), qt.IsNil)
		token, err := testDB.CSPAuthToken(testAuthToken)
		c.Assert(err, qt.IsNil)
		c.Assert(token.Token, qt.DeepEquals, testAuthToken)
		c.Assert(token.UserID, qt.DeepEquals, testUserID)
		c.Assert(token.BundleID, qt.DeepEquals, testCSPBundleID)
		c.Assert(token.Verified, qt.IsFalse)
	})
	c.Run("last token", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.Reset(), qt.IsNil) })
		// get non existing last token
		_, err := testDB.LastCSPAuthToken(testUserID, testCSPBundleID)
		c.Assert(err, qt.ErrorIs, ErrTokenNotFound)
		// set first token
		firtstToken := internal.HexBytes(uuid.New().String())
		c.Assert(testDB.SetCSPAuthToken(firtstToken, testUserID, testCSPBundleID), qt.IsNil)
		// get last token
		token, err := testDB.LastCSPAuthToken(testUserID, testCSPBundleID)
		c.Assert(err, qt.IsNil)
		c.Assert(token.Token, qt.DeepEquals, firtstToken)
		c.Assert(token.UserID, qt.DeepEquals, testUserID)
		c.Assert(token.BundleID, qt.DeepEquals, testCSPBundleID)
		c.Assert(token.Verified, qt.IsFalse)
		// set second token
		secondToken := internal.HexBytes(uuid.New().String())
		c.Assert(testDB.SetCSPAuthToken(secondToken, testUserID, testCSPBundleID), qt.IsNil)
		// get last token
		token, err = testDB.LastCSPAuthToken(testUserID, testCSPBundleID)
		c.Assert(err, qt.IsNil)
		c.Assert(token.Token, qt.DeepEquals, secondToken)
		c.Assert(token.UserID, qt.DeepEquals, testUserID)
		c.Assert(token.BundleID, qt.DeepEquals, testCSPBundleID)
		c.Assert(token.Verified, qt.IsFalse)
	})
	c.Run("non existing token", func(c *qt.C) {
		// get a non-existing token
		_, err := testDB.CSPAuthToken(invalidAuthToken)
		c.Assert(err, qt.ErrorIs, ErrTokenNotFound)
	})
}

func TestVerifyCSPAuthToken(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.Reset(), qt.IsNil) })

	c.Run("nil token", func(c *qt.C) {
		c.Assert(testDB.VerifyCSPAuthToken(nil), qt.ErrorIs, ErrBadInputs)
	})
	c.Run("non existing token", func(c *qt.C) {
		c.Assert(testDB.VerifyCSPAuthToken(invalidAuthToken), qt.ErrorIs, ErrTokenNotFound)
	})
	c.Run("valid token", func(c *qt.C) {
		// set the token and verify it
		c.Assert(testDB.SetCSPAuthToken(testAuthToken, testUserID, testCSPBundleID), qt.IsNil)
		c.Assert(testDB.VerifyCSPAuthToken(testAuthToken), qt.IsNil)
		token, err := testDB.CSPAuthToken(testAuthToken)
		c.Assert(err, qt.IsNil)
		c.Assert(token.Verified, qt.IsTrue)
		c.Assert(token.VerifiedAt.IsZero(), qt.IsFalse)
	})
}

func TestCSPAuthTokenStatus(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.Reset(), qt.IsNil) })

	c.Run("nil inputs", func(c *qt.C) {
		c.Assert(testDB.ConsumeCSPAuthToken(nil, testCSPProcessID, testUserAddress), qt.ErrorIs, ErrBadInputs)
		c.Assert(testDB.ConsumeCSPAuthToken(testAuthToken, nil, testUserAddress), qt.ErrorIs, ErrBadInputs)
		c.Assert(testDB.ConsumeCSPAuthToken(testAuthToken, testCSPProcessID, nil), qt.ErrorIs, ErrBadInputs)

		_, err := testDB.CSPAuthTokenStatus(nil, testCSPProcessID)
		c.Assert(err, qt.ErrorIs, ErrBadInputs)
		_, err = testDB.CSPAuthTokenStatus(testAuthToken, nil)
		c.Assert(err, qt.ErrorIs, ErrBadInputs)
	})

	c.Run("non existing token", func(c *qt.C) {
		c.Assert(testDB.ConsumeCSPAuthToken(invalidAuthToken, testCSPProcessID, testUserAddress), qt.ErrorIs, ErrTokenNotFound)
		_, err := testDB.CSPAuthTokenStatus(invalidAuthToken, testCSPProcessID)
		c.Assert(err, qt.ErrorIs, ErrTokenNotFound)
	})

	c.Run("consume token", func(c *qt.C) {
		// set the token and consume it
		c.Assert(testDB.SetCSPAuthToken(testAuthToken, testUserID, testCSPBundleID), qt.IsNil)
		c.Assert(testDB.ConsumeCSPAuthToken(testAuthToken, testCSPProcessID, testUserAddress), qt.IsNil)
		status, err := testDB.CSPAuthTokenStatus(testAuthToken, testCSPProcessID)
		c.Assert(err, qt.IsNil)
		c.Assert(status.ProcessID, qt.DeepEquals, testCSPProcessID)
		c.Assert(status.Consumed, qt.IsTrue)
		c.Assert(status.ConsumedToken, qt.DeepEquals, testAuthToken)
		c.Assert(status.ConsumedAddress, qt.DeepEquals, testUserAddress)
		c.Assert(status.ConsumedAt.IsZero(), qt.IsFalse)
		// try to consume it again to check it fails
		c.Assert(testDB.ConsumeCSPAuthToken(testAuthToken, testCSPProcessID, testUserAddress), qt.ErrorIs, ErrProcessAlreadyConsumed)
	})
}
