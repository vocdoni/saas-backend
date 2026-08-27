package csp

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"

	blind "github.com/arnaucube/go-blindsecp256k1"
	"github.com/vocdoni/saas-backend/csp/signers"
	"github.com/vocdoni/saas-backend/csp/signers/saltedkey"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// Sign method signs a message with the given token, address and processID. It
// returns the signature as HexBytes or an error if the signer type is invalid
// or the signature fails.
func (c *CSP) Sign(
	token, address, processID, weight internal.HexBytes,
	signType signers.SignerType,
) (internal.HexBytes, error) {
	switch signType {
	case signers.SignerTypeECDSASalted:
		userID, salt, message, err := c.prepareSaltedKeySigner(token, address, processID, weight)
		defer c.unlock(userID, processID)
		if err != nil {
			return nil, err
		}
		signature, err := c.Signer.SignECDSA(*salt, message)
		if err != nil {
			return nil, errors.Join(ErrSign, err)
		}
		if err := c.finishSaltedKeySigner(token, address, processID); err != nil {
			return nil, err
		}
		return signature, nil
	default:
		return nil, ErrInvalidSignerType
	}
}

// NewBlindRequest issues the round-1 blind point R for a blind (OFF_CHAIN_CA_V2) signature on the
// given election and stores the matching one-time nonce k for round 2. It validates the auth token
// (verified, not yet fully consumed) but never sees the voter address — signing a message the CSP
// cannot read is what keeps the vote unlinkable. R is a compressed blind point (33 bytes) the client
// uses to blind its CA bundle before calling BlindSign.
func (c *CSP) NewBlindRequest(token, processID internal.HexBytes) (internal.HexBytes, error) {
	authTokenData, err := c.Storage.CSPAuth(token)
	if err != nil {
		if errors.Is(err, db.ErrTokenNotFound) {
			return nil, ErrInvalidAuthToken
		}
		return nil, errors.Join(ErrSign, err)
	}
	if !authTokenData.Verified {
		return nil, ErrAuthTokenNotVerified
	}
	if consumed, err := c.Storage.IsCSPProcessConsumed(authTokenData.UserID, processID); err != nil {
		return nil, errors.Join(ErrSign, err)
	} else if consumed {
		return nil, ErrProcessAlreadyConsumed
	}
	// go-blindsecp256k1's BlindSign rejects a k whose big-endian encoding is not exactly 32 bytes
	// (a ~1/256 leading-zero case). We control k, so regenerate until it is full-width and never
	// leak that failure to round 2; the client still has to retry the ~1/256 blinded-message case.
	var k *big.Int
	var r *blind.Point
	for range 128 {
		k, r, err = blind.NewRequestParameters()
		if err != nil {
			return nil, errors.Join(ErrSign, err)
		}
		if len(k.Bytes()) == 32 {
			break
		}
	}
	if len(k.Bytes()) != 32 {
		return nil, errors.Join(ErrSign, fmt.Errorf("could not derive a full-width blind nonce"))
	}
	if err := c.Storage.SetCSPProcessBlindSecret(token, processID, k.Bytes()); err != nil {
		if errors.Is(err, db.ErrProcessAlreadyConsumed) {
			return nil, ErrProcessAlreadyConsumed
		}
		return nil, errors.Join(ErrSign, err)
	}
	return r.Bytes(), nil
}

// BlindSign performs the round-2 blind signature: it loads the one-time nonce k stored in round 1,
// blind-signs the client-supplied blinded message with the V2-salted key (the salt binds processID
// and the CSP-authorized weight, so a forged bundle weight cannot verify), consumes the election for
// the user, and clears k. It returns the raw blind-signature scalar; the client unblinds it into the
// final ProofCA signature. The address is never seen here — anonymity holds by construction.
func (c *CSP) BlindSign(token, processID, weight, blindedMsg internal.HexBytes) (internal.HexBytes, error) {
	authTokenData, err := c.Storage.CSPAuth(token)
	if err != nil {
		if errors.Is(err, db.ErrTokenNotFound) {
			return nil, ErrInvalidAuthToken
		}
		return nil, errors.Join(ErrSign, err)
	}
	if !authTokenData.Verified {
		return nil, ErrAuthTokenNotVerified
	}
	// serialize concurrent round-2 requests for the same (user, election)
	if c.isLocked(authTokenData.UserID, processID) {
		return nil, ErrUserAlreadySigning
	}
	c.lock(authTokenData.UserID, processID)
	defer c.unlock(authTokenData.UserID, processID)

	proc, err := c.Storage.CSPProcessByUserAndProcess(authTokenData.UserID, processID)
	if err != nil {
		if errors.Is(err, db.ErrTokenNotFound) {
			return nil, ErrBlindRequestNotFound
		}
		return nil, errors.Join(ErrSign, err)
	}
	if proc.BlindSecret == nil {
		return nil, ErrBlindRequestNotFound
	}
	if proc.TimesVoted > db.MaxVoteOverwritesPerProcess {
		return nil, ErrProcessAlreadyConsumed
	}
	salt, err := saltedkey.V2Salt(processID, weight)
	if err != nil {
		return nil, errors.Join(ErrSign, err)
	}
	secretK := new(big.Int).SetBytes(proc.BlindSecret)
	signature, err := c.Signer.SignBlind(salt, blindedMsg, secretK)
	if err != nil {
		return nil, errors.Join(ErrSign, err)
	}
	if err := c.Storage.ConsumeCSPProcessBlind(token, processID); err != nil {
		if errors.Is(err, db.ErrProcessAlreadyConsumed) {
			return nil, ErrProcessAlreadyConsumed
		}
		return nil, errors.Join(ErrSign, err)
	}
	return signature, nil
}

// prepareSaltedKeySigner method prepares the data for the Ethereum signer.
// It ensures the following conditions:
// - The auth token is valid and it is already verified.
// - The user belongs to the bundle.
// - The user belongs to the process.
// - The process has not been consumed yet.
// Then generates a bundle CA and encodes it to be signed. It returns userID,
// the salt as nil and the encoded CA as a message to sign.
//
//revive:disable:function-result-limit
func (c *CSP) prepareSaltedKeySigner(token, address, processID, weight internal.HexBytes) (
	internal.HexBytes, *[saltedkey.SaltSize]byte, internal.HexBytes, error,
) {
	// get the data of the auth token and the user from the storage. A token that is genuinely
	// gone is an auth verdict; any other storage failure must NOT be — the batch endpoint
	// aborts every remaining ballot on an auth verdict, and a Mongo blip is retryable.
	authTokenData, err := c.Storage.CSPAuth(token)
	if err != nil {
		if errors.Is(err, db.ErrTokenNotFound) {
			return nil, nil, nil, ErrInvalidAuthToken
		}
		return nil, nil, nil, errors.Join(ErrSign, err)
	}
	// ensure that the auth token has been verified. Checked BEFORE taking the signer lock: an
	// error return between lock and the deferred unlock in Sign used to leave the (user,
	// election) lock held forever, because the nil userID returned alongside the error made the
	// defer unlock a different key than the one locked.
	if !authTokenData.Verified {
		return nil, nil, nil, ErrAuthTokenNotVerified
	}
	// check if the user is already signing
	if c.isLocked(authTokenData.UserID, processID) {
		return nil, nil, nil, ErrUserAlreadySigning
	}
	// check if the process is already consumed for this user
	if consumed, err := c.Storage.IsCSPProcessConsumed(authTokenData.UserID, processID); err != nil {
		log.Warn(err)
		switch err {
		case db.ErrTokenNotVerified:
			return nil, nil, nil, ErrAuthTokenNotVerified
		default:
			return nil, nil, nil, ErrSign
		}
	} else if consumed {
		return nil, nil, nil, ErrProcessAlreadyConsumed
	}
	// lock the user data to avoid concurrent signing. Every return below this line carries
	// authTokenData.UserID — error or not — so Sign's deferred unlock always releases the key
	// that was locked here.
	c.lock(authTokenData.UserID, processID)

	// prepare the data for the signature
	caBundle := &models.CAbundle{
		ProcessId:  processID,
		Address:    address,
		VoteWeight: weight,
	}
	// encode the data to sign
	signatureMsg, err := proto.Marshal(caBundle)
	if err != nil {
		return authTokenData.UserID, nil, nil, ErrPrepareSignature
	}
	// generate the salt
	salt := [saltedkey.SaltSize]byte{}
	if len(processID) < saltedkey.SaltSize {
		return authTokenData.UserID, nil, nil, ErrInvalidSalt
	}
	copy(salt[:], processID[:saltedkey.SaltSize])
	return authTokenData.UserID, &salt, signatureMsg, nil
}

func (c *CSP) finishSaltedKeySigner(token, address, processID internal.HexBytes) error {
	// get the data of the auth token and the user from the storage; same not-found vs
	// storage-failure split as prepareSaltedKeySigner, and for the same reason.
	authTokenData, err := c.Storage.CSPAuth(token)
	if err != nil {
		if errors.Is(err, db.ErrTokenNotFound) {
			return ErrInvalidAuthToken
		}
		return errors.Join(ErrSign, err)
	}
	// ensure that the auth token has been verified
	if !authTokenData.Verified {
		return ErrAuthTokenNotVerified
	}
	if !c.isLocked(authTokenData.UserID, processID) {
		return ErrUserIsNotAlreadySigning
	}
	// check if the process is already consumed for this user
	if consumed, err := c.Storage.IsCSPProcessConsumed(authTokenData.UserID, processID); err != nil {
		fmt.Println(err)
		return ErrSign
	} else if consumed {
		return ErrProcessAlreadyConsumed
	}
	// update the process data to mark it as consumed, and set the token used
	if err := c.Storage.ConsumeCSPProcess(token, processID, address); err != nil {
		log.Warn(err)
		// a different address than the one this election was first consumed with is an
		// authorization rejection (the slot is pinned to that address), not a signer failure —
		// and it is recoverable: re-sign with the pinned address, which sign-info reports.
		if errors.Is(err, db.ErrInvalidData) {
			return ErrAddressMismatch
		}
		return ErrSign
	}
	return nil
}

func (c *CSP) lock(userID, processID internal.HexBytes) {
	id := sha256.Sum256(append(userID, processID...))
	c.signerLock.Store(id, struct{}{})
}

func (c *CSP) isLocked(userID, processID internal.HexBytes) bool {
	id := sha256.Sum256(append(userID, processID...))
	_, ok := c.signerLock.Load(id)
	return ok
}

func (c *CSP) unlock(userID, processID internal.HexBytes) {
	id := sha256.Sum256(append(userID, processID...))
	c.signerLock.Delete(id)
}
