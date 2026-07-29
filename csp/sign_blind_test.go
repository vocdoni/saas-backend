package csp

import (
	"context"
	"math/big"
	"sync"
	"testing"
	"time"

	blind "github.com/arnaucube/go-blindsecp256k1"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/csp/signers/saltedkey"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/test"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/util"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// newTestCSP spins up a CSP backed by a fresh database.
func newTestCSP(c *qt.C) (*CSP, *db.MongoStorage) {
	testDB, err := db.New(testMongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)
	csp, err := New(context.Background(), &Config{
		DB:                       testDB,
		MailService:              testMailService,
		SMSService:               testSMSService,
		NotificationCoolDownTime: time.Second * 5,
		RootKey:                  *testRootKey,
	})
	c.Assert(err, qt.IsNil)
	return csp, testDB
}

// verifiedToken registers a verified auth token for a voter.
func verifiedToken(c *qt.C, csp *CSP, token, userID internal.HexBytes) {
	c.Assert(csp.Storage.SetCSPAuth(token, userID, testScopeID, ""), qt.IsNil)
	c.Assert(csp.Storage.VerifyCSPAuth(token), qt.IsNil)
}

// TestBlindSignRoundTrip walks the flow a voter actually performs and verifies
// the result the way the chain does in cspproof.Verify: unblind the scalar, then
// check the signature against the CSP key salted with the election id.
func TestBlindSignRoundTrip(t *testing.T) {
	c := qt.New(t)
	csp, _ := newTestCSP(c)

	electionID := internal.HexBytes(util.RandomBytes(32))
	verifiedToken(c, csp, testToken, testUserID)

	// 1. the voter opens a session and receives R plus a weight attestation
	tokenR, weightCert, err := csp.PrepareBlindSign(testToken, electionID, testUserWeight)
	c.Assert(err, qt.IsNil)
	c.Assert(tokenR, qt.HasLen, 33) // library's own compressed point encoding
	c.Assert(weightCert, qt.Not(qt.HasLen), 0)

	// 2. the voter builds the ballot bundle the chain will verify, and blinds it.
	//    The CSP never sees this.
	caBundle := &models.CAbundle{
		ProcessId:  electionID,
		Address:    testAddress,
		VoteWeight: new(big.Int).SetUint64(testUserWeight).Bytes(),
	}
	bundleBytes, err := proto.Marshal(caBundle)
	c.Assert(err, qt.IsNil)

	r, err := blind.NewPointFromBytes(tokenR)
	c.Assert(err, qt.IsNil)
	msgHash := ethereum.HashRaw(bundleBytes)
	blindedMsg, userSecretData, err := blind.Blind(new(big.Int).SetBytes(msgHash), r)
	c.Assert(err, qt.IsNil)

	// 3. the CSP blind-signs a scalar it cannot interpret
	blindedSig, err := csp.CompleteBlindSign(testToken, electionID, blindedMsg.Bytes())
	c.Assert(err, qt.IsNil)

	// 4. the voter unblinds, and the signature verifies against the salted key
	signature := blind.Unblind(new(big.Int).SetBytes(blindedSig), userSecretData)
	salt, err := saltFromProcessID(electionID)
	c.Assert(err, qt.IsNil)
	saltedPub, err := saltedkey.SaltBlindPubKey(csp.BlindPubKey(), *salt)
	c.Assert(err, qt.IsNil)
	c.Assert(blind.Verify(new(big.Int).SetBytes(msgHash), signature, saltedPub), qt.IsTrue)

	// the on-chain proof carries the 96-byte uncompressed form
	c.Assert(signature.BytesUncompressed(), qt.HasLen, 96)

	// 5. and the CSP recorded no address for this voter
	status, err := csp.Storage.CSPProcess(testToken, electionID)
	c.Assert(err, qt.IsNil)
	c.Assert(status.Used, qt.IsTrue)
	c.Assert(status.UsedAddress, qt.HasLen, 0)
}

// TestBlindSignSessionIsSingleUse is the security-critical one. Signing two
// different blinded messages with the same k reveals the salted private key via
// d = (s1 - s2) / (m1 - m2), so a session must never sign twice.
func TestBlindSignSessionIsSingleUse(t *testing.T) {
	c := qt.New(t)
	csp, _ := newTestCSP(c)

	electionID := internal.HexBytes(util.RandomBytes(32))
	verifiedToken(c, csp, testToken, testUserID)

	tokenR, _, err := csp.PrepareBlindSign(testToken, electionID, 1)
	c.Assert(err, qt.IsNil)
	r, err := blind.NewPointFromBytes(tokenR)
	c.Assert(err, qt.IsNil)

	blinded := func() internal.HexBytes {
		for {
			m, _, err := blind.Blind(new(big.Int).SetBytes(ethereum.HashRaw(util.RandomBytes(32))), r)
			c.Assert(err, qt.IsNil)
			if len(m.Bytes()) == blindScalarLen {
				return m.Bytes()
			}
		}
	}

	_, err = csp.CompleteBlindSign(testToken, electionID, blinded())
	c.Assert(err, qt.IsNil)

	// a second signature under the same session must not be produced
	_, err = csp.CompleteBlindSign(testToken, electionID, blinded())
	c.Assert(err, qt.Not(qt.IsNil))
}

// TestBlindSignConcurrentCompletion asserts that racing completions produce at
// most one signature, which is what stops two messages being signed with one k.
func TestBlindSignConcurrentCompletion(t *testing.T) {
	c := qt.New(t)
	csp, _ := newTestCSP(c)

	electionID := internal.HexBytes(util.RandomBytes(32))
	verifiedToken(c, csp, testToken, testUserID)

	tokenR, _, err := csp.PrepareBlindSign(testToken, electionID, 1)
	c.Assert(err, qt.IsNil)
	r, err := blind.NewPointFromBytes(tokenR)
	c.Assert(err, qt.IsNil)

	const racers = 6
	msgs := make([]internal.HexBytes, 0, racers)
	for len(msgs) < racers {
		m, _, err := blind.Blind(new(big.Int).SetBytes(ethereum.HashRaw(util.RandomBytes(32))), r)
		c.Assert(err, qt.IsNil)
		if len(m.Bytes()) == blindScalarLen {
			msgs = append(msgs, m.Bytes())
		}
	}

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int
	)
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := csp.CompleteBlindSign(testToken, electionID, msgs[i]); err == nil {
				mu.Lock()
				succeeded++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	c.Assert(succeeded, qt.Equals, 1)
}

// TestBlindRequestParametersAlwaysFullWidth is the regression test for issue
// #597: go-blindsecp256k1 rejects a k whose minimal encoding is not 32 bytes,
// and a uniform draw is short about one time in 256. Unfixed, this loop fails
// within a few hundred iterations.
func TestBlindRequestParametersAlwaysFullWidth(t *testing.T) {
	c := qt.New(t)

	for range 2000 {
		k, r, err := newBlindRequestParameters()
		c.Assert(err, qt.IsNil)
		c.Assert(k.Bytes(), qt.HasLen, blindScalarLen)
		c.Assert(r, qt.Not(qt.IsNil))
	}
}

// TestCompleteBlindSignRejectsShortMessageWithoutBurningSession covers the retry
// path: a blinded message whose minimal encoding is short cannot be signed, and
// the voter must be able to blind again rather than lose their single-use
// session.
func TestCompleteBlindSignRejectsShortMessageWithoutBurningSession(t *testing.T) {
	c := qt.New(t)
	csp, _ := newTestCSP(c)

	electionID := internal.HexBytes(util.RandomBytes(32))
	verifiedToken(c, csp, testToken, testUserID)

	tokenR, _, err := csp.PrepareBlindSign(testToken, electionID, 1)
	c.Assert(err, qt.IsNil)

	// a 31-byte value is exactly the case the signer would reject
	short := internal.HexBytes(util.RandomBytes(31))
	_, err = csp.CompleteBlindSign(testToken, electionID, short)
	c.Assert(err, qt.ErrorIs, ErrRetryBlinding)

	// the session survived, so blinding again still works
	r, err := blind.NewPointFromBytes(tokenR)
	c.Assert(err, qt.IsNil)
	var good internal.HexBytes
	for good == nil {
		m, _, err := blind.Blind(new(big.Int).SetBytes(ethereum.HashRaw(util.RandomBytes(32))), r)
		c.Assert(err, qt.IsNil)
		if len(m.Bytes()) == blindScalarLen {
			good = m.Bytes()
		}
	}
	_, err = csp.CompleteBlindSign(testToken, electionID, good)
	c.Assert(err, qt.IsNil)
}

func TestPrepareBlindSignAuthorization(t *testing.T) {
	c := qt.New(t)
	csp, _ := newTestCSP(c)

	c.Run("unknown token", func(c *qt.C) {
		_, _, err := csp.PrepareBlindSign(internal.HexBytes("nope"), internal.HexBytes(util.RandomBytes(32)), 1)
		c.Assert(err, qt.ErrorIs, ErrInvalidAuthToken)
	})

	c.Run("unverified token", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(csp.Storage.DeleteAllDocuments(), qt.IsNil) })
		tok := internal.HexBytes("unverified")
		c.Assert(csp.Storage.SetCSPAuth(tok, testUserID, testScopeID, ""), qt.IsNil)
		_, _, err := csp.PrepareBlindSign(tok, internal.HexBytes(util.RandomBytes(32)), 1)
		c.Assert(err, qt.ErrorIs, ErrAuthTokenNotVerified)
	})

	c.Run("election id too short to salt", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(csp.Storage.DeleteAllDocuments(), qt.IsNil) })
		verifiedToken(c, csp, testToken, testUserID)
		_, _, err := csp.PrepareBlindSign(testToken, internal.HexBytes(util.RandomBytes(saltedkey.SaltSize-1)), 1)
		c.Assert(err, qt.ErrorIs, ErrInvalidSalt)
	})

	c.Run("already signed anonymously", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(csp.Storage.DeleteAllDocuments(), qt.IsNil) })
		electionID := internal.HexBytes(util.RandomBytes(32))
		verifiedToken(c, csp, testToken, testUserID)
		c.Assert(csp.Storage.ConsumeCSPProcessBlind(testToken, electionID), qt.IsNil)

		_, _, err := csp.PrepareBlindSign(testToken, electionID, 1)
		c.Assert(err, qt.ErrorIs, ErrProcessAlreadyConsumed)
	})
}

func TestCompleteBlindSignWithoutSession(t *testing.T) {
	c := qt.New(t)
	csp, _ := newTestCSP(c)

	electionID := internal.HexBytes(util.RandomBytes(32))
	verifiedToken(c, csp, testToken, testUserID)

	// never prepared, so there is nothing to complete
	msg := internal.HexBytes(util.RandomBytes(32))
	msg[0] |= 0x80 // keep the minimal encoding a full 32 bytes
	_, err := csp.CompleteBlindSign(testToken, electionID, msg)
	c.Assert(err, qt.ErrorIs, ErrBlindSessionNotFound)
}

func TestWeightAttestation(t *testing.T) {
	c := qt.New(t)
	csp, _ := newTestCSP(c)

	electionID := internal.HexBytes(util.RandomBytes(32))
	verifiedToken(c, csp, testToken, testUserID)

	_, weightCert, err := csp.PrepareBlindSign(testToken, electionID, testUserWeight)
	c.Assert(err, qt.IsNil)

	c.Run("verifies for the attested weight", func(c *qt.C) {
		ok, err := csp.VerifyWeightAttestation(electionID, weightCert, testUserWeight)
		c.Assert(err, qt.IsNil)
		c.Assert(ok, qt.IsTrue)
	})

	c.Run("rejects a different weight", func(c *qt.C) {
		ok, err := csp.VerifyWeightAttestation(electionID, weightCert, testUserWeight+1)
		c.Assert(err, qt.IsNil)
		c.Assert(ok, qt.IsFalse)
	})

	c.Run("rejects a different election", func(c *qt.C) {
		ok, err := csp.VerifyWeightAttestation(internal.HexBytes(util.RandomBytes(32)), weightCert, testUserWeight)
		c.Assert(err, qt.IsNil)
		c.Assert(ok, qt.IsFalse)
	})

	c.Run("rejects a garbage signature", func(c *qt.C) {
		ok, err := csp.VerifyWeightAttestation(electionID, internal.HexBytes(util.RandomBytes(65)), testUserWeight)
		c.Assert(err, qt.IsNil)
		c.Assert(ok, qt.IsFalse)
	})

	c.Run("covers the full uint64 weight range", func(c *qt.C) {
		// big.NewInt(int64(weight)) would overflow here; the fixed 8-byte
		// big-endian encoding does not. Reuses the token registered above, on a
		// different election so its signing slot is still free.
		huge := uint64(1) << 63
		other := internal.HexBytes(util.RandomBytes(32))
		_, cert, err := csp.PrepareBlindSign(testToken, other, huge)
		c.Assert(err, qt.IsNil)

		ok, err := csp.VerifyWeightAttestation(other, cert, huge)
		c.Assert(err, qt.IsNil)
		c.Assert(ok, qt.IsTrue)
	})
}
