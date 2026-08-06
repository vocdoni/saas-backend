package csp

import (
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/vocdoni/saas-backend/csp/signers/saltedkey"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/dvote/vochain/genesis"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// Sign salted-ECDSA-signs a CA bundle for the given token, address and
// processID, and consumes the voter's signing slot for that election. It returns
// the signature as HexBytes, or an error if the voter is not entitled to it.
//
// startTime is the election's on-chain start (the voting process's startDate),
// which with the CSP's ChainID selects the salt derivation via the CSP soft fork
// (see electionSalt). The anonymous flow does not come through here: it takes no
// address, returns a blinded scalar rather than a signature, and pairs with its
// own preparation step. See PrepareBlindSign and CompleteBlindSign in
// sign_blind.go.
func (c *CSP) Sign(token, address, processID, weight internal.HexBytes, startTime uint32) (internal.HexBytes, error) {
	userID, salt, message, err := c.prepareSaltedKeySigner(token, address, processID, weight, startTime)
	defer c.unlock(userID, processID)
	if err != nil {
		return nil, err
	}
	signature, err := c.Signer.SignECDSA(salt, message)
	if err != nil {
		return nil, errors.Join(ErrSign, err)
	}
	if err := c.finishSaltedKeySigner(token, address, processID); err != nil {
		return nil, err
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
func (c *CSP) prepareSaltedKeySigner(token, address, processID, weight internal.HexBytes, startTime uint32) (
	internal.HexBytes, []byte, internal.HexBytes, error,
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
	if err := checkProcessConsumed(authTokenData.UserID, processID, c.Storage.IsCSPProcessConsumed); err != nil {
		return nil, nil, nil, err
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
	salt, err := c.electionSalt(processID, weight, startTime)
	if err != nil {
		return nil, nil, nil, err
	}
	return authTokenData.UserID, salt, signatureMsg, nil
}

// electionSalt derives the per-election salt applied to the CSP key, mirroring
// the chain's cspproof.ProofVerifierCSP.salt so a proof the CSP signs verifies
// on chain.
//
// After the CSP salted-proof fork (genesis.CSPSaltedProofV2Active) the salt is
// the fixed derivation that covers the whole processID and the CSP-authorized
// vote weight:
//
//	salt = keccak256(processID || weight-as-32-big-endian)[:20]
//
// so sibling elections of one organization derive distinct salts (the legacy
// derivation cropped the processID to its first 20 bytes and shared one salted
// keypair across them all), and a voter who tampers the bundle weight derives a
// different key. Before the fork the salt is the raw processID, of which the
// key primitives consume the first SaltSize bytes — the legacy behaviour, kept
// verbatim so in-flight elections stay valid.
func (c *CSP) electionSalt(processID internal.HexBytes, weight []byte, startTime uint32) ([]byte, error) {
	if genesis.CSPSaltedProofV2Active(c.chainID, startTime) {
		return saltedkey.Salt(processID, weight)
	}
	return processID, nil
}

// consumedCheck reports whether a voter has used up their signature(s) for an
// election. The plain and anonymous flows differ only in how many they allow, so
// callers pass the rule that applies to them.
type consumedCheck func(userID, processID internal.HexBytes) (bool, error)

// checkProcessConsumed reports whether the voter may still sign for this
// election, translating storage errors into CSP ones.
func checkProcessConsumed(userID, processID internal.HexBytes, consumedBy consumedCheck) error {
	consumed, err := consumedBy(userID, processID)
	if err != nil {
		log.Warn(err)
		switch err {
		case db.ErrTokenNotVerified:
			return ErrAuthTokenNotVerified
		default:
			return ErrSign
		}
	}
	if consumed {
		return ErrProcessAlreadyConsumed
	}
	return nil
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
