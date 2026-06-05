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
	// get the data of the auth token and the user from the storage
	authTokenData, err := c.Storage.CSPAuth(token)
	if err != nil {
		return nil, nil, nil, ErrInvalidAuthToken
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
	// lock the user data to avoid concurrent signing
	c.lock(authTokenData.UserID, processID)
	// ensure that the auth token has been verified
	if !authTokenData.Verified {
		return nil, nil, nil, ErrAuthTokenNotVerified
	}

	// prepare the data for the signature
	caBundle := &models.CAbundle{
		ProcessId:  processID,
		Address:    address,
		VoteWeight: weight,
	}
	// encode the data to sign
	signatureMsg, err := proto.Marshal(caBundle)
	if err != nil {
		return nil, nil, nil, ErrPrepareSignature
	}
	// generate the salt
	salt := [saltedkey.SaltSize]byte{}
	if len(processID) < saltedkey.SaltSize {
		return nil, nil, nil, ErrInvalidSalt
	}
	copy(salt[:], processID[:saltedkey.SaltSize])
	return authTokenData.UserID, &salt, signatureMsg, nil
}

func (c *CSP) finishSaltedKeySigner(token, address, processID internal.HexBytes) error {
	// get the data of the auth token and the user from the storage
	authTokenData, err := c.Storage.CSPAuth(token)
	if err != nil {
		return ErrInvalidAuthToken
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

// blindKKey returns the sync.Map key used to store the ephemeral secretK for a
// blind signing session keyed by (userID, processID).
func blindKKey(userID, processID internal.HexBytes) [32]byte {
	return sha256.Sum256(append(userID, processID...))
}

// GetBlindR begins a blind signing session for the authenticated voter. It
// validates the auth token, checks the process is not yet consumed, generates
// a fresh blind-signing keypair (secretK, R), stores secretK ephemerally, and
// returns:
//   - tokenR: the R point (33 bytes, compressed) the voter needs to blind their ballot.
//   - weight: the voter's weight for this process (1 for non-weighted censuses).
//   - weightCert: a non-blind ECDSA signature over {processID, weight}, allowing
//     the chain to verify the weight independently of the anonymous blind credential.
func (c *CSP) GetBlindR(
	token, processID internal.HexBytes,
	memberWeight uint64,
) (tokenR internal.HexBytes, weightCert internal.HexBytes, err error) {
	authTokenData, err := c.Storage.CSPAuth(token)
	if err != nil {
		return nil, nil, ErrInvalidAuthToken
	}
	if !authTokenData.Verified {
		return nil, nil, ErrAuthTokenNotVerified
	}
	if consumed, err := c.Storage.IsCSPProcessConsumed(authTokenData.UserID, processID); err != nil {
		switch err {
		case db.ErrTokenNotVerified:
			return nil, nil, ErrAuthTokenNotVerified
		default:
			return nil, nil, ErrSign
		}
	} else if consumed {
		return nil, nil, ErrProcessAlreadyConsumed
	}

	// generate ephemeral blind signing parameters
	k, r, err := blind.NewRequestParameters()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrSign, err)
	}

	// store k keyed by (userID, processID) for retrieval during the sign step
	key := blindKKey(authTokenData.UserID, processID)
	c.blindKStore.Store(key, k)

	// build weight attestation: ECDSA-sign {processID, weight_big_endian} with
	// the process salt so the signature is deterministic per (process, weight).
	weightBytes := new(big.Int).SetUint64(memberWeight).Bytes()
	weightMsg, err := proto.Marshal(&models.CAbundle{
		ProcessId:  processID,
		VoteWeight: weightBytes,
	})
	if err != nil {
		c.blindKStore.Delete(key)
		return nil, nil, fmt.Errorf("%w: marshal weight bundle: %w", ErrWeightAttestation, err)
	}
	salt := [saltedkey.SaltSize]byte{}
	if len(processID) < saltedkey.SaltSize {
		c.blindKStore.Delete(key)
		return nil, nil, ErrInvalidSalt
	}
	copy(salt[:], processID[:saltedkey.SaltSize])
	cert, err := c.Signer.SignECDSA(salt, weightMsg)
	if err != nil {
		c.blindKStore.Delete(key)
		return nil, nil, fmt.Errorf("%w: %w", ErrWeightAttestation, err)
	}

	return r.Bytes(), cert, nil
}

// SignBlindMsg completes a blind signing session. It retrieves the stored
// secretK for the (authToken, processID) pair, performs the blind signature on
// blindedMsg, cleans up the ephemeral state, and marks the process as consumed.
// The returned bytes are the raw blind signature that the voter must unblind
// client-side using their UserSecretData.
func (c *CSP) SignBlindMsg(
	token, processID, blindedMsg internal.HexBytes,
) (internal.HexBytes, error) {
	authTokenData, err := c.Storage.CSPAuth(token)
	if err != nil {
		return nil, ErrInvalidAuthToken
	}
	if !authTokenData.Verified {
		return nil, ErrAuthTokenNotVerified
	}

	key := blindKKey(authTokenData.UserID, processID)
	kVal, ok := c.blindKStore.Load(key)
	if !ok {
		return nil, ErrBlindRNotFound
	}
	k, ok := kVal.(*big.Int)
	if !ok {
		return nil, ErrBlindRNotFound
	}
	// always clean up the ephemeral k regardless of outcome
	defer c.blindKStore.Delete(key)

	if consumed, err := c.Storage.IsCSPProcessConsumed(authTokenData.UserID, processID); err != nil {
		switch err {
		case db.ErrTokenNotVerified:
			return nil, ErrAuthTokenNotVerified
		default:
			return nil, ErrSign
		}
	} else if consumed {
		return nil, ErrProcessAlreadyConsumed
	}

	salt := [saltedkey.SaltSize]byte{}
	if len(processID) < saltedkey.SaltSize {
		return nil, ErrInvalidSalt
	}
	copy(salt[:], processID[:saltedkey.SaltSize])

	sig, err := c.Signer.SignBlind(salt, blindedMsg, k)
	if err != nil {
		return nil, errors.Join(ErrSign, err)
	}

	if err := c.Storage.ConsumeCSPProcessBlind(token, processID); err != nil {
		log.Warn(err)
		return nil, ErrSign
	}
	return sig, nil
}
