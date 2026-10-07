package db

import (
	"testing"
	"time"

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

func TestSetGetCSPAuth(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	c.Run("nil token", func(c *qt.C) {
		c.Assert(testDB.SetCSPAuth(nil, testUserID, testCSPBundleID, ""), qt.ErrorIs, ErrBadInputs)
	})
	c.Run("nil userID", func(c *qt.C) {
		c.Assert(testDB.SetCSPAuth(testAuthToken, nil, testCSPBundleID, ""), qt.ErrorIs, ErrBadInputs)
	})
	c.Run("nil bundleID", func(c *qt.C) {
		c.Assert(testDB.SetCSPAuth(testAuthToken, testUserID, nil, ""), qt.ErrorIs, ErrBadInputs)
	})
	c.Run("valid token", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		// set the token and check it was set
		c.Assert(testDB.SetCSPAuth(testAuthToken, testUserID, testCSPBundleID, ""), qt.IsNil)
		token, err := testDB.CSPAuth(testAuthToken)
		c.Assert(err, qt.IsNil)
		c.Assert(token.Token, qt.DeepEquals, testAuthToken)
		c.Assert(token.UserID, qt.DeepEquals, testUserID)
		c.Assert(token.AnchorID, qt.DeepEquals, testCSPBundleID)
		c.Assert(token.Verified, qt.IsFalse)
	})
	c.Run("last token", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		// get non existing last token
		_, err := testDB.LastCSPAuth(testUserID, testCSPBundleID)
		c.Assert(err, qt.ErrorIs, ErrTokenNotFound)
		// set first token
		firtstToken := internal.HexBytes(uuid.New().String())
		c.Assert(testDB.SetCSPAuth(firtstToken, testUserID, testCSPBundleID, ""), qt.IsNil)
		// get last token
		token, err := testDB.LastCSPAuth(testUserID, testCSPBundleID)
		c.Assert(err, qt.IsNil)
		c.Assert(token.Token, qt.DeepEquals, firtstToken)
		c.Assert(token.UserID, qt.DeepEquals, testUserID)
		c.Assert(token.AnchorID, qt.DeepEquals, testCSPBundleID)
		c.Assert(token.Verified, qt.IsFalse)
		// wait 10 ms since token.CreatedAt has only ms precision
		time.Sleep(10 * time.Millisecond)
		// set second token
		secondToken := internal.HexBytes(uuid.New().String())
		c.Assert(testDB.SetCSPAuth(secondToken, testUserID, testCSPBundleID, ""), qt.IsNil)
		// get last token
		token, err = testDB.LastCSPAuth(testUserID, testCSPBundleID)
		c.Assert(err, qt.IsNil)
		c.Assert(token.Token, qt.DeepEquals, secondToken)
		c.Assert(token.UserID, qt.DeepEquals, testUserID)
		c.Assert(token.AnchorID, qt.DeepEquals, testCSPBundleID)
		c.Assert(token.Verified, qt.IsFalse)
	})
	c.Run("non existing token", func(c *qt.C) {
		// get a non-existing token
		_, err := testDB.CSPAuth(invalidAuthToken)
		c.Assert(err, qt.ErrorIs, ErrTokenNotFound)
	})
}

func TestVerifyCSPAuth(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	c.Run("nil token", func(c *qt.C) {
		c.Assert(testDB.VerifyCSPAuth(nil), qt.ErrorIs, ErrBadInputs)
	})
	c.Run("non existing token", func(c *qt.C) {
		c.Assert(testDB.VerifyCSPAuth(invalidAuthToken), qt.ErrorIs, ErrTokenNotFound)
	})
	c.Run("valid token", func(c *qt.C) {
		// set the token and verify it
		c.Assert(testDB.SetCSPAuth(testAuthToken, testUserID, testCSPBundleID, ""), qt.IsNil)
		c.Assert(testDB.VerifyCSPAuth(testAuthToken), qt.IsNil)
		token, err := testDB.CSPAuth(testAuthToken)
		c.Assert(err, qt.IsNil)
		c.Assert(token.Verified, qt.IsTrue)
		c.Assert(token.VerifiedAt.IsZero(), qt.IsFalse)
	})
}

func TestVerifyCSPAuthForChallenge(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	c.Assert(testDB.SetCSPAuth(testAuthToken, testUserID, testCSPBundleID, "secret"), qt.IsNil)
	challenge, err := testDB.CSPAuth(testAuthToken)
	c.Assert(err, qt.IsNil)
	first := internal.HexBytes(uuid.New().String())
	second := internal.HexBytes(uuid.New().String())
	c.Assert(testDB.SetCSPAuthForChallenge(first, challenge), qt.IsNil)
	c.Assert(testDB.SetCSPAuthForChallenge(second, challenge), qt.IsNil)

	firstData, err := testDB.CSPAuth(first)
	c.Assert(err, qt.IsNil)
	c.Assert(firstData.ChallengeID, qt.DeepEquals, testAuthToken)
	c.Assert(firstData.ChallengeToken(), qt.DeepEquals, testAuthToken)
	c.Assert(firstData.UserID, qt.DeepEquals, testUserID)
	c.Assert(firstData.AnchorID, qt.DeepEquals, testCSPBundleID)
	c.Assert(firstData.Secret, qt.Equals, "")

	// verifying one token consumes the shared challenge
	c.Assert(testDB.VerifyCSPAuth(first), qt.IsNil)
	firstData, err = testDB.CSPAuth(first)
	c.Assert(err, qt.IsNil)
	c.Assert(firstData.Verified, qt.IsTrue)
	challenge, err = testDB.CSPAuth(testAuthToken)
	c.Assert(err, qt.IsNil)
	c.Assert(challenge.Secret, qt.Equals, "")
	c.Assert(challenge.Verified, qt.IsFalse)

	// so no other token answering it verifies anymore
	c.Assert(testDB.VerifyCSPAuth(second), qt.ErrorIs, ErrChallengeConsumed)
	secondData, err := testDB.CSPAuth(second)
	c.Assert(err, qt.IsNil)
	c.Assert(secondData.Verified, qt.IsFalse)
}

func TestClaimCSPAuthSend(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	c.Run("nil token", func(c *qt.C) {
		_, err := testDB.ClaimCSPAuthSend(nil, time.Minute)
		c.Assert(err, qt.ErrorIs, ErrBadInputs)
	})
	c.Run("non existing token", func(c *qt.C) {
		claimed, err := testDB.ClaimCSPAuthSend(invalidAuthToken, 0)
		c.Assert(err, qt.IsNil)
		c.Assert(claimed, qt.IsFalse)
	})
	c.Run("cooldown counts from creation, then from the last send", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(testDB.SetCSPAuth(testAuthToken, testUserID, testCSPBundleID, "secret"), qt.IsNil)
		token, err := testDB.CSPAuth(testAuthToken)
		c.Assert(err, qt.IsNil)
		c.Assert(token.LastSent(), qt.Equals, token.CreatedAt)

		// created just now, which was the first send
		claimed, err := testDB.ClaimCSPAuthSend(testAuthToken, time.Minute)
		c.Assert(err, qt.IsNil)
		c.Assert(claimed, qt.IsFalse)

		time.Sleep(10 * time.Millisecond)
		claimed, err = testDB.ClaimCSPAuthSend(testAuthToken, 5*time.Millisecond)
		c.Assert(err, qt.IsNil)
		c.Assert(claimed, qt.IsTrue)
		token, err = testDB.CSPAuth(testAuthToken)
		c.Assert(err, qt.IsNil)
		c.Assert(token.LastSentAt.After(token.CreatedAt), qt.IsTrue)
		c.Assert(token.LastSent(), qt.Equals, token.LastSentAt)

		// the send just claimed restarts the cooldown
		claimed, err = testDB.ClaimCSPAuthSend(testAuthToken, 5*time.Millisecond)
		c.Assert(err, qt.IsNil)
		c.Assert(claimed, qt.IsFalse)
	})
}

func TestIncrementCSPAuthAttempts(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	const maxAttempts = 5

	c.Run("nil token", func(c *qt.C) {
		_, err := testDB.IncrementCSPAuthAttempts(nil, maxAttempts)
		c.Assert(err, qt.ErrorIs, ErrBadInputs)
	})
	c.Run("non existing token", func(c *qt.C) {
		_, err := testDB.IncrementCSPAuthAttempts(invalidAuthToken, maxAttempts)
		c.Assert(err, qt.ErrorIs, ErrTokenNotFound)
	})
	c.Run("conditional increment caps at max", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(testDB.SetCSPAuth(testAuthToken, testUserID, testCSPBundleID, "secret"), qt.IsNil)
		// the first maxAttempts increments are recorded
		for range maxAttempts {
			recorded, err := testDB.IncrementCSPAuthAttempts(testAuthToken, maxAttempts)
			c.Assert(err, qt.IsNil)
			c.Assert(recorded, qt.IsTrue)
		}
		// once the cap is reached, further increments are not recorded
		recorded, err := testDB.IncrementCSPAuthAttempts(testAuthToken, maxAttempts)
		c.Assert(err, qt.IsNil)
		c.Assert(recorded, qt.IsFalse)
		// the counter never exceeds the cap
		token, err := testDB.CSPAuth(testAuthToken)
		c.Assert(err, qt.IsNil)
		c.Assert(token.Attempts, qt.Equals, maxAttempts)
	})
}

func TestCSPProcess(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	c.Run("nil inputs", func(c *qt.C) {
		c.Assert(testDB.ConsumeCSPProcess(nil, testCSPProcessID, testUserAddress), qt.ErrorIs, ErrBadInputs)
		c.Assert(testDB.ConsumeCSPProcess(testAuthToken, nil, testUserAddress), qt.ErrorIs, ErrBadInputs)
		c.Assert(testDB.ConsumeCSPProcess(testAuthToken, testCSPProcessID, nil), qt.ErrorIs, ErrBadInputs)

		_, err := testDB.CSPProcess(nil, testCSPProcessID)
		c.Assert(err, qt.ErrorIs, ErrBadInputs)
		_, err = testDB.CSPProcess(testAuthToken, nil)
		c.Assert(err, qt.ErrorIs, ErrBadInputs)
	})

	c.Run("non existing token", func(c *qt.C) {
		c.Assert(testDB.ConsumeCSPProcess(invalidAuthToken, testCSPProcessID, testUserAddress), qt.ErrorIs, ErrTokenNotFound)
		_, err := testDB.CSPProcess(invalidAuthToken, testCSPProcessID)
		c.Assert(err, qt.ErrorIs, ErrTokenNotFound)
	})

	c.Run("vote once correctly", func(c *qt.C) {
		// set the token and consume it
		c.Assert(testDB.SetCSPAuth(testAuthToken, testUserID, testCSPBundleID, ""), qt.IsNil)
		c.Assert(testDB.ConsumeCSPProcess(testAuthToken, testCSPProcessID, testUserAddress), qt.IsNil)
		status, err := testDB.CSPProcess(testAuthToken, testCSPProcessID)
		c.Assert(err, qt.IsNil)
		c.Assert(status.ProcessID, qt.DeepEquals, testCSPProcessID)
		c.Assert(status.Used, qt.IsTrue)
		c.Assert(status.UsedToken, qt.DeepEquals, testAuthToken)
		c.Assert(status.UsedAddress, qt.DeepEquals, testUserAddress)
		c.Assert(status.UsedAt.IsZero(), qt.IsFalse)
		c.Assert(status.TimesVoted, qt.Equals, 1)
	})

	c.Run("cannot consume with different address", func(c *qt.C) {
		c.Assert(testDB.ConsumeCSPProcess(testAuthToken, testCSPProcessID, internal.RandomBytes(16)), qt.ErrorIs, ErrInvalidData)
	})

	c.Run("consume token", func(c *qt.C) {
		// set the token and consume it
		for i := 1; i <= 10; i++ {
			c.Assert(testDB.ConsumeCSPProcess(testAuthToken, testCSPProcessID, testUserAddress), qt.IsNil)
			status, err := testDB.CSPProcess(testAuthToken, testCSPProcessID)
			c.Assert(err, qt.IsNil)
			c.Assert(status.ProcessID, qt.DeepEquals, testCSPProcessID)
			c.Assert(status.Used, qt.IsTrue)
			c.Assert(status.UsedToken, qt.DeepEquals, testAuthToken)
			c.Assert(status.UsedAddress, qt.DeepEquals, testUserAddress)
			c.Assert(status.UsedAt.IsZero(), qt.IsFalse)
			c.Assert(status.TimesVoted, qt.Equals, i+1)
		}
		// try to consume it again to check it fails
		c.Assert(testDB.ConsumeCSPProcess(testAuthToken, testCSPProcessID, testUserAddress), qt.ErrorIs, ErrProcessAlreadyConsumed)
	})
}
