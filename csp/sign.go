package csp

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"

	blind "github.com/arnaucube/go-blindsecp256k1"
	"github.com/vocdoni/saas-backend/csp/signers/saltedkey"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

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

	k, r, err := blind.NewRequestParameters()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrSign, err)
	}

	key := blindKKey(authTokenData.UserID, processID)
	c.blindKStore.Store(key, k)

	// ECDSA-sign {processID, weight} so the chain can verify voter weight
	// independently of the anonymous blind credential.
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
