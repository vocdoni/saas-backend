package csp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"

	blind "github.com/arnaucube/go-blindsecp256k1"
	"github.com/vocdoni/saas-backend/csp/signers/saltedkey"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/log"
)

const (
	// weightAttestationDomain separates the weight attestation from every other
	// message this key signs. Without it the attestation is byte-compatible with
	// a CA bundle that simply omits the address, and reusing one signature format
	// for two purposes is how signature-confusion bugs start.
	weightAttestationDomain = "vocdoni-weight-attestation-v1"

	// blindScalarLen is the exact big-endian length go-blindsecp256k1 demands of
	// both the secret k and the blinded message.
	blindScalarLen = 32

	// maxBlindKAttempts bounds the resampling loop below. Each draw fails with
	// probability ~1/256, so exhausting this many is not something that happens.
	maxBlindKAttempts = 64
)

// PrepareBlindSign opens an anonymous signing session for a voter on one
// election and returns the public R point they need in order to blind their
// ballot, together with the CSP's attestation of their voting weight.
//
// The CSP learns nothing here about the address the voter will cast with -- that
// is the point. It only records that this voter has a session open.
func (c *CSP) PrepareBlindSign(
	token, electionID internal.HexBytes, weight uint64,
) (tokenR, weightCert internal.HexBytes, err error) {
	authTokenData, err := c.Storage.CSPAuth(token)
	if err != nil {
		return nil, nil, ErrInvalidAuthToken
	}
	if !authTokenData.Verified {
		return nil, nil, ErrAuthTokenNotVerified
	}
	if err := c.checkProcessConsumed(authTokenData.UserID, electionID, true); err != nil {
		return nil, nil, err
	}
	salt, err := saltFromProcessID(electionID)
	if err != nil {
		return nil, nil, err
	}

	k, r, err := newBlindRequestParameters()
	if err != nil {
		return nil, nil, errors.Join(ErrSign, err)
	}

	// Sign the weight attestation before storing the session: if this fails
	// there is no half-open session left behind.
	cert, err := c.Signer.SignECDSA(*salt, weightAttestationMessage(electionID, weight))
	if err != nil {
		return nil, nil, errors.Join(ErrWeightAttestation, err)
	}

	// k is stored zero-padded to a fixed width; it is read back with SetBytes,
	// which recovers the same integer either way.
	if err := c.Storage.SetCSPBlindSession(
		authTokenData.UserID, electionID, k.FillBytes(make([]byte, blindScalarLen)), 0,
	); err != nil {
		return nil, nil, errors.Join(ErrSign, err)
	}

	log.Debugw("opened blind signing session", "procId", electionID, "weight", weight)
	return r.Bytes(), cert, nil
}

// CompleteBlindSign blind-signs the message a voter prepared earlier and marks
// the election consumed for them.
//
// The signature returned is the blinded scalar s'; the client unblinds it before
// putting it in a vote proof. The CSP never sees the ballot bundle, so it cannot
// check what it is signing -- the authority comes entirely from the checks below
// and from the session having been opened for this voter.
func (c *CSP) CompleteBlindSign(token, electionID, blindedMsg internal.HexBytes) (internal.HexBytes, error) {
	authTokenData, err := c.Storage.CSPAuth(token)
	if err != nil {
		return nil, ErrInvalidAuthToken
	}
	if !authTokenData.Verified {
		return nil, ErrAuthTokenNotVerified
	}

	// Validate the blinded message before touching the session. A value whose
	// minimal encoding is short would be rejected by the signer, and consuming
	// the single-use session first would strand the voter with no way to retry.
	m := new(big.Int).SetBytes(blindedMsg)
	if len(m.Bytes()) != blindScalarLen {
		return nil, ErrRetryBlinding
	}

	// Serialize against the plain path so a voter cannot run both at once.
	if c.isLocked(authTokenData.UserID, electionID) {
		return nil, ErrUserAlreadySigning
	}
	c.lock(authTokenData.UserID, electionID)
	defer c.unlock(authTokenData.UserID, electionID)

	if err := c.checkProcessConsumed(authTokenData.UserID, electionID, true); err != nil {
		return nil, err
	}
	salt, err := saltFromProcessID(electionID)
	if err != nil {
		return nil, err
	}

	// Fetch-and-delete in one step: k is single-use. Signing two different
	// messages with one k reveals the salted private key, so this must never
	// hand the same k out twice.
	session, err := c.Storage.ConsumeCSPBlindSession(authTokenData.UserID, electionID)
	if err != nil {
		if errors.Is(err, db.ErrCSPBlindSessionNotFound) {
			return nil, ErrBlindSessionNotFound
		}
		return nil, errors.Join(ErrSign, err)
	}

	signature, err := c.Signer.SignBlind(*salt, blindedMsg, new(big.Int).SetBytes(session.SecretK))
	if err != nil {
		return nil, errors.Join(ErrSign, err)
	}

	// Record the election as used without an address -- the CSP has none.
	if err := c.Storage.ConsumeCSPProcessBlind(token, electionID); err != nil {
		log.Warn(err)
		return nil, ErrSign
	}
	return signature, nil
}

// BlindPubKey returns the public key that verifies these blind signatures,
// before the per-election salt is applied.
//
// It is the same curve point as PubKey() and encodes identically, because both
// are rootKey*G -- the chain derives the blind key from the census root itself
// (blind.NewPublicKeyFromECDSA). It exists so callers that verify blind
// signatures do not have to know that.
func (c *CSP) BlindPubKey() *blind.PublicKey {
	return c.Signer.BlindPubKey()
}

// newBlindRequestParameters draws a (k, R) pair whose k is exactly 32 bytes when
// minimally encoded.
//
// go-blindsecp256k1's BlindSign rejects any k whose big-endian encoding is not
// exactly 32 bytes, and NewRequestParameters draws k uniformly, so about one
// draw in 256 has a zero leading byte. Resampling here turns what would be an
// intermittent failure at signing time -- after the voter has already blinded
// their ballot -- into a retry nobody sees. See issue #597, which documents the
// same defect surfacing as a flaky test.
func newBlindRequestParameters() (*big.Int, *blind.Point, error) {
	for range maxBlindKAttempts {
		k, r, err := blind.NewRequestParameters()
		if err != nil {
			return nil, nil, err
		}
		if len(k.Bytes()) == blindScalarLen {
			return k, r, nil
		}
	}
	return nil, nil, fmt.Errorf("could not draw a %d-byte k in %d attempts", blindScalarLen, maxBlindKAttempts)
}

// weightAttestationMessage builds the domain-separated message attesting that a
// voter is entitled to the given weight on the given election.
//
// The weight is a fixed 8-byte big-endian field rather than a big.Int encoding,
// so every weight has exactly one representation and the full uint64 range round
// trips.
func weightAttestationMessage(electionID internal.HexBytes, weight uint64) []byte {
	msg := make([]byte, 0, len(weightAttestationDomain)+len(electionID)+8)
	msg = append(msg, weightAttestationDomain...)
	msg = append(msg, electionID...)
	var w [8]byte
	binary.BigEndian.PutUint64(w[:], weight)
	return append(msg, w[:]...)
}

// VerifyWeightAttestation checks an attestation issued by PrepareBlindSign, by
// recovering the signer and comparing it against this CSP's election-salted
// public key. The vote relay uses it to reject a blinded bundle declaring a
// weight the CSP never attested.
//
// It verifies rather than re-signs and compares, so it does not rest on ECDSA
// signing being deterministic.
//
// This is a backstop, not a boundary. The attestation binds only
// {electionID, weight} and carries no address -- it cannot, or it would
// re-identify the voter -- so it is a bearer token, identical for every voter of
// that weight. And the chain reads weight straight from the voter-supplied
// bundle with nowhere to carry this attestation, so submitting a vote directly
// instead of through the relay skips the check entirely.
func (c *CSP) VerifyWeightAttestation(electionID, cert internal.HexBytes, weight uint64) (bool, error) {
	salt, err := saltFromProcessID(electionID)
	if err != nil {
		return false, err
	}
	// recover whoever signed this attestation
	signerPub, err := ethereum.PubKeyFromSignature(weightAttestationMessage(electionID, weight), cert)
	if err != nil {
		// a malformed signature is simply not a valid attestation
		return false, nil
	}
	signerPubDec, err := ethereum.DecompressPubKey(signerPub)
	if err != nil {
		return false, nil
	}
	// derive what this CSP's key looks like once salted for this election
	rootPub, err := c.Signer.ECDSAPubKey()
	if err != nil {
		return false, err
	}
	// SaltECDSAPubKey mutates the key it is given, so hand it a copy
	rootPubCopy := *rootPub
	saltedPub, err := saltedkey.SaltECDSAPubKey(&rootPubCopy, *salt)
	if err != nil {
		return false, errors.Join(ErrWeightAttestation, err)
	}
	return bytes.Equal(signerPubDec, saltedPub), nil
}
