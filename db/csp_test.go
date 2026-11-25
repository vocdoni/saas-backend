package db

import (
	"context"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
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

// TestVerifyCSPAuthWriteFailure checks that verifying is one write: when it fails, the code keeps
// its secret and no token is verified, so the voter can still solve it.
func TestVerifyCSPAuthWriteFailure(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	database := testDB.DBClient.Database(testDB.database)
	setValidator := func(validator bson.M) {
		c.Assert(database.RunCommand(ctx, bson.D{
			{Key: "collMod", Value: "cspTokens"},
			{Key: "validator", Value: validator},
		}).Err(), qt.IsNil)
	}
	c.Cleanup(func() {
		setValidator(bson.M{})
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	})

	c.Assert(testDB.SetCSPAuth(testAuthToken, testUserID, testCSPBundleID, "secret"), qt.IsNil)
	// a validator refusing any verified challenge makes the verify write fail
	setValidator(bson.M{"verifiedby": bson.M{"$exists": false}})
	c.Assert(testDB.VerifyCSPAuth(testAuthToken), qt.ErrorIs, ErrStoreToken)
	token, err := testDB.CSPAuth(testAuthToken)
	c.Assert(err, qt.IsNil)
	c.Assert(token.Secret, qt.Equals, "secret")
	c.Assert(token.VerifiedBy, qt.HasLen, 0)
	c.Assert(token.Verified, qt.IsFalse)

	// once writes go through again, the code is still solvable
	setValidator(bson.M{})
	c.Assert(testDB.VerifyCSPAuth(testAuthToken), qt.IsNil)
	token, err = testDB.CSPAuth(testAuthToken)
	c.Assert(err, qt.IsNil)
	c.Assert(token.Verified, qt.IsTrue)
	c.Assert(token.Secret, qt.Equals, "")
}

// TestCSPAuthTokensOfOneCode checks the token view: every token of a code resolves to its
// document, and only the token that solved the code is verified, for signing and consumption.
func TestCSPAuthTokensOfOneCode(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	first := testAuthToken
	second := internal.HexBytes(uuid.New().String())
	c.Assert(testDB.SetCSPAuth(first, testUserID, testCSPBundleID, "secret"), qt.IsNil)
	challenge, err := testDB.CSPAuth(first)
	c.Assert(err, qt.IsNil)
	add := func(token internal.HexBytes) bool {
		added, err := testDB.AddCSPAuthToken(CSPTokenRequest{
			ChallengeID: challenge.ID, Token: token, MaxTokens: 10, MaxAttempts: 5, TTL: time.Hour,
		})
		c.Assert(err, qt.IsNil)
		return added
	}
	c.Assert(add(second), qt.IsTrue)

	secondData, err := testDB.CSPAuth(second)
	c.Assert(err, qt.IsNil)
	c.Assert(secondData.ID, qt.DeepEquals, challenge.ID)
	c.Assert(secondData.Token, qt.DeepEquals, second)
	c.Assert(secondData.Secret, qt.Equals, "secret")
	c.Assert(secondData.Tokens, qt.HasLen, 2)

	// the second token solves the code: only it is verified, and the code is spent for the first
	c.Assert(testDB.VerifyCSPAuth(second), qt.IsNil)
	secondData, err = testDB.CSPAuth(second)
	c.Assert(err, qt.IsNil)
	c.Assert(secondData.Verified, qt.IsTrue)
	firstData, err := testDB.CSPAuth(first)
	c.Assert(err, qt.IsNil)
	c.Assert(firstData.Verified, qt.IsFalse)
	c.Assert(testDB.VerifyCSPAuth(first), qt.ErrorIs, ErrChallengeConsumed)

	// a process consumed through the solving token reads as verified...
	c.Assert(testDB.ConsumeCSPProcess(second, testCSPProcessID, testUserAddress), qt.IsNil)
	consumed, err := testDB.IsCSPProcessConsumed(testUserID, testCSPProcessID)
	c.Assert(err, qt.IsNil)
	c.Assert(consumed, qt.IsFalse)
	// ...and one consumed through the other token does not
	otherProcess := internal.HexBytes([]byte("otherProcessID"))
	c.Assert(testDB.ConsumeCSPProcess(first, otherProcess, testUserAddress), qt.IsNil)
	_, err = testDB.IsCSPProcessConsumed(testUserID, otherProcess)
	c.Assert(err, qt.ErrorIs, ErrTokenNotVerified)

	// a solved code takes no more tokens and keeps the one that solved it
	c.Assert(add(internal.HexBytes(uuid.New().String())), qt.IsFalse)
	c.Assert(testDB.RemoveCSPAuthToken(challenge.ID, second), qt.IsNil)
	_, err = testDB.CSPAuth(second)
	c.Assert(err, qt.IsNil)
}

// TestAddCSPAuthTokenCap checks that a code takes at most MaxTokens tokens, the first included,
// and that a dropped token stops resolving.
func TestAddCSPAuthTokenCap(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	const maxTokens = 3
	c.Assert(testDB.SetCSPAuth(testAuthToken, testUserID, testCSPBundleID, "secret"), qt.IsNil)
	challenge, err := testDB.CSPAuth(testAuthToken)
	c.Assert(err, qt.IsNil)
	req := CSPTokenRequest{ChallengeID: challenge.ID, MaxTokens: maxTokens, MaxAttempts: 5, TTL: time.Hour}
	var last internal.HexBytes
	for range maxTokens - 1 {
		last = internal.HexBytes(uuid.New().String())
		req.Token = last
		added, err := testDB.AddCSPAuthToken(req)
		c.Assert(err, qt.IsNil)
		c.Assert(added, qt.IsTrue)
	}
	req.Token = internal.HexBytes(uuid.New().String())
	added, err := testDB.AddCSPAuthToken(req)
	c.Assert(err, qt.IsNil)
	c.Assert(added, qt.IsFalse)

	c.Assert(testDB.RemoveCSPAuthToken(challenge.ID, last), qt.IsNil)
	_, err = testDB.CSPAuth(last)
	c.Assert(err, qt.ErrorIs, ErrTokenNotFound)
	added, err = testDB.AddCSPAuthToken(req)
	c.Assert(err, qt.IsNil)
	c.Assert(added, qt.IsTrue)

	// an expired code takes no token
	c.Assert(testDB.RemoveCSPAuthToken(challenge.ID, req.Token), qt.IsNil)
	req.Token, req.TTL = internal.HexBytes(uuid.New().String()), 0
	added, err = testDB.AddCSPAuthToken(req)
	c.Assert(err, qt.IsNil)
	c.Assert(added, qt.IsFalse)
}

// TestClaimRollbackCSPAuthSend checks the per-destination send claim and its conditional rollback.
func TestClaimRollbackCSPAuthSend(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	_, _, err := testDB.ClaimCSPAuthSend(CSPSendClaimRequest{})
	c.Assert(err, qt.ErrorIs, ErrBadInputs)

	c.Assert(testDB.SetCSPAuth(testAuthToken, testUserID, testCSPBundleID, "secret"), qt.IsNil)
	challenge, err := testDB.CSPAuth(testAuthToken)
	c.Assert(err, qt.IsNil)
	req := CSPSendClaimRequest{
		ChallengeID: challenge.ID, Dest: "aa", CoolDown: time.Hour, TTL: time.Hour, MaxAttempts: 5, MaxResends: 2,
	}
	claim := func(r CSPSendClaimRequest) (CSPSendClaim, bool) {
		cl, ok, err := testDB.ClaimCSPAuthSend(r)
		c.Assert(err, qt.IsNil)
		return cl, ok
	}
	read := func() *CSPAuth {
		data, err := testDB.CSPAuth(testAuthToken)
		c.Assert(err, qt.IsNil)
		return data
	}

	// a destination never sent to is due, and has no previous send
	claimA, ok := claim(req)
	c.Assert(ok, qt.IsTrue)
	c.Assert(claimA.Previous.IsZero(), qt.IsTrue)
	// inside its cooldown it is refused; another destination is not
	_, ok = claim(req)
	c.Assert(ok, qt.IsFalse)
	reqB := req
	reqB.Dest = "bb"
	claimB, ok := claim(reqB)
	c.Assert(ok, qt.IsTrue)
	// the code's resends (2) are spent
	reqC := req
	reqC.Dest = "cc"
	_, ok = claim(reqC)
	c.Assert(ok, qt.IsFalse)
	data := read()
	c.Assert(data.Resends, qt.Equals, 2)
	c.Assert(data.Sends, qt.HasLen, 2)
	c.Assert(data.Sends["bb"].Equal(claimB.At), qt.IsTrue)

	// a rollback only matches its own claim time
	rollback := CSPSendRollback{ChallengeID: challenge.ID, Dest: "bb", ClaimedAt: claimB.At.Add(-time.Millisecond), Resend: true}
	rolled, err := testDB.RollbackCSPAuthSend(rollback)
	c.Assert(err, qt.IsNil)
	c.Assert(rolled, qt.IsFalse)
	rollback.ClaimedAt = claimB.At
	rolled, err = testDB.RollbackCSPAuthSend(rollback)
	c.Assert(err, qt.IsNil)
	c.Assert(rolled, qt.IsTrue)
	data = read()
	c.Assert(data.Resends, qt.Equals, 1)
	_, ok = data.Sends["bb"]
	c.Assert(ok, qt.IsFalse)

	// rolling back a later send to a destination restores its previous send time
	reqA := req
	reqA.CoolDown = 0
	claimA2, ok := claim(reqA)
	c.Assert(ok, qt.IsTrue)
	c.Assert(claimA2.Previous.Equal(claimA.At), qt.IsTrue)
	rolled, err = testDB.RollbackCSPAuthSend(CSPSendRollback{
		ChallengeID: challenge.ID, Dest: "aa", ClaimedAt: claimA2.At, Previous: claimA2.Previous, Resend: true,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(rolled, qt.IsTrue)
	data = read()
	c.Assert(data.Sends["aa"].Equal(claimA.At), qt.IsTrue)
	c.Assert(data.Resends, qt.Equals, 1)

	// a solved code takes no send
	c.Assert(testDB.VerifyCSPAuth(testAuthToken), qt.IsNil)
	_, ok = claim(reqB)
	c.Assert(ok, qt.IsFalse)
}

// TestNewCSPChallenge checks a new code's document: its first token, its first send, and that it
// is deleted only while no other token answers it.
func TestNewCSPChallenge(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	_, err := testDB.NewCSPChallenge(CSPChallengeRequest{})
	c.Assert(err, qt.ErrorIs, ErrBadInputs)
	challenge, err := testDB.NewCSPChallenge(CSPChallengeRequest{
		Token: testAuthToken, UserID: testUserID, AnchorID: testCSPBundleID, Secret: "secret", Dest: "aa",
	})
	c.Assert(err, qt.IsNil)
	c.Assert(challenge.Token, qt.DeepEquals, testAuthToken)
	c.Assert(challenge.ID, qt.Not(qt.DeepEquals), testAuthToken)
	stored, err := testDB.CSPAuth(testAuthToken)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.ID, qt.DeepEquals, challenge.ID)
	c.Assert(stored.Resends, qt.Equals, 0)
	c.Assert(stored.Sends["aa"].Equal(stored.CreatedAt), qt.IsTrue)
	c.Assert(stored.LastSend().Equal(stored.CreatedAt), qt.IsTrue)

	added, err := testDB.AddCSPAuthToken(CSPTokenRequest{
		ChallengeID: challenge.ID, Token: invalidAuthToken, MaxTokens: 10, MaxAttempts: 5, TTL: time.Hour,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(added, qt.IsTrue)
	deleted, err := testDB.DeleteCSPChallenge(challenge.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(deleted, qt.IsFalse)
	c.Assert(testDB.RemoveCSPAuthToken(challenge.ID, invalidAuthToken), qt.IsNil)
	deleted, err = testDB.DeleteCSPChallenge(challenge.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(deleted, qt.IsTrue)
	_, err = testDB.CSPAuth(testAuthToken)
	c.Assert(err, qt.ErrorIs, ErrTokenNotFound)

	// an auth-only grant is verified by its only token
	c.Assert(testDB.SetVerifiedCSPAuth(testAuthToken, testUserID, testCSPBundleID), qt.IsNil)
	grant, err := testDB.CSPAuth(testAuthToken)
	c.Assert(err, qt.IsNil)
	c.Assert(grant.Verified, qt.IsTrue)
	c.Assert(grant.Secret, qt.Equals, "")
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
		for i := 1; i < MaxCSPSignaturesPerElection; i++ {
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
