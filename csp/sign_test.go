package csp

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	blind "github.com/arnaucube/go-blindsecp256k1"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/csp/signers"
	"github.com/vocdoni/saas-backend/csp/signers/saltedkey"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/test"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/util"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

func TestSign(t *testing.T) {
	c := qt.New(t)

	testDB, err := db.New(testMongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)

	ctx := context.Background()
	csp, err := New(ctx, &Config{
		DB:                       testDB,
		MailService:              testMailService,
		SMSService:               testSMSService,
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
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, ""), qt.IsNil)
		// verify the token
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		sign, err := csp.Sign(testToken, testAddress, pid, testUserWeightBytes, signers.SignerTypeECDSASalted)
		c.Assert(err, qt.IsNil)
		c.Assert(sign, qt.Not(qt.IsNil))
		c.Assert(csp.isLocked(testUserID, pid), qt.IsFalse)
	})

	c.Run("failed sign does not leak the signer lock", func(c *qt.C) {
		pid := internal.HexBytes(util.RandomBytes(32))
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		// an unverified token is rejected...
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, ""), qt.IsNil)
		_, err := csp.Sign(testToken, testAddress, pid, testUserWeightBytes, signers.SignerTypeECDSASalted)
		c.Assert(err, qt.ErrorIs, ErrAuthTokenNotVerified)
		// ...without leaving the (user, election) lock held: after verifying the same token,
		// signing must succeed rather than report the user as already signing forever.
		c.Assert(csp.isLocked(testUserID, pid), qt.IsFalse)
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		sign, err := csp.Sign(testToken, testAddress, pid, testUserWeightBytes, signers.SignerTypeECDSASalted)
		c.Assert(err, qt.IsNil)
		c.Assert(sign, qt.Not(qt.IsNil))
	})
}

func TestBlindSign(t *testing.T) {
	c := qt.New(t)

	testDB, err := db.New(testMongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)

	ctx := context.Background()
	csp, err := New(ctx, &Config{
		DB:                       testDB,
		MailService:              testMailService,
		SMSService:               testSMSService,
		NotificationCoolDownTime: time.Second * 5,
		RootKey:                  *testRootKey,
	})
	c.Assert(err, qt.IsNil)

	// a syntactically valid blinded message: 32 bytes, non-zero, well below the curve order N.
	validBlinded := bytes.Repeat([]byte{0x01}, 32)

	c.Run("blind sign before request fails", func(c *qt.C) {
		pid := internal.HexBytes(util.RandomBytes(32))
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, ""), qt.IsNil)
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		// no NewBlindRequest issued yet: round 2 has no nonce to claim
		_, _, err := csp.BlindSign(testToken, pid, validBlinded)
		c.Assert(err, qt.ErrorIs, ErrBlindRequestNotFound)
	})

	c.Run("invalid blinded message is rejected without consuming the nonce", func(c *qt.C) {
		pid := internal.HexBytes(util.RandomBytes(32))
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, ""), qt.IsNil)
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		_, _, err := csp.NewBlindRequest(testToken, pid, testUserWeightBytes)
		c.Assert(err, qt.IsNil)
		// a too-short (non-32-byte) message is refused before the nonce is claimed
		_, _, err = csp.BlindSign(testToken, pid, []byte("short"))
		c.Assert(err, qt.ErrorIs, ErrInvalidBlindedMessage)
		// the nonce is still armed, so a valid message then claims it
		_, _, err = csp.BlindSign(testToken, pid, validBlinded)
		c.Assert(err, qt.IsNil)
	})

	c.Run("round 1 re-arm pins both R and weight", func(c *qt.C) {
		pid := internal.HexBytes(util.RandomBytes(32))
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, ""), qt.IsNil)
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		r1, w1, err := csp.NewBlindRequest(testToken, pid, testUserWeightBytes)
		c.Assert(err, qt.IsNil)
		c.Assert(w1, qt.DeepEquals, internal.HexBytes(testUserWeightBytes))
		// re-arm with a DIFFERENT weight, as if the member's live weight changed between two round-1
		// calls (a lost response, a second tab, a proxy retry). The idempotent arm must return the
		// pinned R AND the pinned weight — never the new one — so the (R, weight) pair the client
		// blinds stays consistent with what round 2 salts with.
		otherWeight := big.NewInt(int64(testUserWeight + 5)).Bytes()
		r2, w2, err := csp.NewBlindRequest(testToken, pid, otherWeight)
		c.Assert(err, qt.IsNil)
		c.Assert(r2, qt.DeepEquals, r1)
		c.Assert(w2, qt.DeepEquals, w1)
	})

	c.Run("full two-round blind flow verifies, weight-bound, single-use", func(c *qt.C) {
		pid := internal.HexBytes(util.RandomBytes(32))
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, ""), qt.IsNil)
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)

		// The two-round flow, retried on the ~1/256 case where the client's blinded message is not
		// 32 bytes (go-blindsecp256k1 rejects it): the CSP returns ErrInvalidBlindedMessage WITHOUT
		// consuming the nonce, and the client re-blinds against the same (idempotent) R.
		msgHash := ethereum.HashRaw([]byte("blind ballot"))
		var signature *blind.Signature
		for range 32 {
			rBytes, _, err := csp.NewBlindRequest(testToken, pid, testUserWeightBytes)
			c.Assert(err, qt.IsNil)
			signerR, err := blind.NewPointFromBytes(rBytes)
			c.Assert(err, qt.IsNil)
			msgBlinded, userSecretData, err := blind.Blind(new(big.Int).SetBytes(msgHash), signerR)
			c.Assert(err, qt.IsNil)
			blindedSig, signedWeight, err := csp.BlindSign(testToken, pid, msgBlinded.Bytes())
			if err != nil {
				c.Assert(err, qt.ErrorIs, ErrInvalidBlindedMessage) // ~1/256; nonce not consumed
				continue
			}
			// round 2 reports the pinned weight it actually salted with
			c.Assert(signedWeight, qt.DeepEquals, internal.HexBytes(testUserWeightBytes))
			signature = blind.Unblind(new(big.Int).SetBytes(blindedSig), userSecretData)
			break
		}
		c.Assert(signature, qt.Not(qt.IsNil))

		// verify against the CSP blind key salted with the SAME processID+weight
		salt, err := saltedkey.V2Salt(pid, testUserWeightBytes)
		c.Assert(err, qt.IsNil)
		saltedPub, err := saltedkey.SaltBlindPubKey(csp.Signer.BlindPubKey(), salt)
		c.Assert(err, qt.IsNil)
		c.Assert(blind.Verify(new(big.Int).SetBytes(msgHash), signature, saltedPub), qt.IsTrue)

		// a forged weight does not verify (V2 binds the weight into the salt)
		otherSalt, err := saltedkey.V2Salt(pid, big.NewInt(int64(testUserWeight+1)).Bytes())
		c.Assert(err, qt.IsNil)
		forgedPub, err := saltedkey.SaltBlindPubKey(csp.Signer.BlindPubKey(), otherSalt)
		c.Assert(err, qt.IsNil)
		c.Assert(blind.Verify(new(big.Int).SetBytes(msgHash), signature, forgedPub), qt.IsFalse)

		// single-use: the nonce was consumed, so a second valid round-2 is refused, and round 1
		// refuses to re-arm a signed election
		_, _, err = csp.BlindSign(testToken, pid, validBlinded)
		c.Assert(err, qt.ErrorIs, ErrProcessAlreadyConsumed)
		_, _, err = csp.NewBlindRequest(testToken, pid, testUserWeightBytes)
		c.Assert(err, qt.ErrorIs, ErrProcessAlreadyConsumed)
	})

	c.Run("concurrent round-2 claims yield exactly one signature", func(c *qt.C) {
		pid := internal.HexBytes(util.RandomBytes(32))
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, ""), qt.IsNil)
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		_, _, err := csp.NewBlindRequest(testToken, pid, testUserWeightBytes)
		c.Assert(err, qt.IsNil)

		// two distinct, individually-valid blinded messages fired concurrently for the same nonce.
		// The atomic claim must let exactly one through — signing both with one k leaks the key.
		msgs := [][]byte{bytes.Repeat([]byte{0x01}, 32), bytes.Repeat([]byte{0x02}, 32)}
		var wg sync.WaitGroup
		results := make([]error, len(msgs))
		wg.Add(len(msgs))
		for i, m := range msgs {
			go func() {
				defer wg.Done()
				_, _, results[i] = csp.BlindSign(testToken, pid, m)
			}()
		}
		wg.Wait()

		var ok, consumed int
		for _, e := range results {
			switch {
			case e == nil:
				ok++
			case errIsAny(e, ErrProcessAlreadyConsumed, ErrBlindRequestNotFound):
				consumed++
			default:
				c.Fatalf("unexpected blind-sign error: %v", e)
			}
		}
		c.Assert(ok, qt.Equals, 1)
		c.Assert(consumed, qt.Equals, 1)
	})
}

// errIsAny reports whether err matches any of the targets.
func errIsAny(err error, targets ...error) bool {
	for _, t := range targets {
		if errors.Is(err, t) {
			return true
		}
	}
	return false
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
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, ""), qt.IsNil)
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
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, ""), qt.IsNil)
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
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, ""), qt.IsNil)
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
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, ""), qt.IsNil)
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
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, ""), qt.IsNil)
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
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, ""), qt.IsNil)
		err := csp.finishSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.ErrorIs, ErrAuthTokenNotVerified)
	})

	c.Run("user not signing", func(c *qt.C) {
		c.Cleanup(func() {
			c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
			defer csp.unlock(testUserID, testPID)
		})
		// store the token
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, ""), qt.IsNil)
		// verify the token
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		err := csp.finishSaltedKeySigner(testToken, testAddress, testPID)
		c.Assert(err, qt.ErrorIs, ErrUserIsNotAlreadySigning)
	})

	c.Run("address mismatch", func(c *qt.C) {
		c.Cleanup(func() {
			c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
			csp.unlock(testUserID, testPID)
		})
		// store and verify the token, then consume the election with testAddress
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, ""), qt.IsNil)
		c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
		c.Assert(csp.Storage.ConsumeCSPProcess(testToken, testPID, testAddress), qt.IsNil)
		// signing again for a DIFFERENT address is the pinned-address rejection, its own
		// outcome — the fix that succeeds is re-signing with the pinned address, so it must
		// not read as already_consumed (terminal) nor as a signer failure.
		csp.lock(testUserID, testPID)
		otherAddress := internal.HexBytes(util.RandomBytes(20))
		err := csp.finishSaltedKeySigner(testToken, otherAddress, testPID)
		c.Assert(err, qt.ErrorIs, ErrAddressMismatch)
	})

	c.Run("success", func(c *qt.C) {
		c.Cleanup(func() {
			c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
			defer csp.unlock(testUserID, testPID)
		})
		// store the token
		c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testBundleID, ""), qt.IsNil)
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
