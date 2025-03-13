package csp

import (
	"crypto/sha256"
	"errors"
	"time"

	"github.com/vocdoni/saas-backend/csp/signers"
	"github.com/vocdoni/saas-backend/csp/signers/saltedkey"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// Sign method signs a message with the given token, address and processID. It
// returns the signature as HexBytes or an error if the signer type is invalid
// or the signature fails.
func (c *CSP) Sign(token, address, processID internal.HexBytes, signType signers.SignerType) (internal.HexBytes, error) {
	switch signType {
	case signers.SignerTypeECDSASalted:
		userID, salt, message, err := c.prepareEthereumSigner(token, address, processID)
		defer c.unlock(userID, processID)
		if err != nil {
			return nil, err
		}
		signature, err := c.Signer.SignECDSA(*salt, message)
		if err != nil {
			return nil, errors.Join(ErrSign, err)
		}
		if err := c.finishSignProcess(token, address, processID); err != nil {
			return nil, err
		}
		return signature, nil
	case signers.SignerTypeBlindSalted: // TODO: implement this signer
		return nil, ErrInvalidSignerType
	default:
		return nil, ErrInvalidSignerType
	}
}

// prepareEthereumSigner method prepares the data for the Ethereum signer. It
// ensures the following conditions:
// - The auth token is valid and it is already verified.
// - The user belongs to the bundle.
// - The user belongs to the process.
// - The process has not been consumed yet.
// Then generates a bundle CA and encodes it to be signed. It returns userID,
// the salt as nil and the encoded CA as a message to sign.
func (c *CSP) prepareEthereumSigner(token, address, processID internal.HexBytes) (
	internal.HexBytes, *[saltedkey.SaltSize]byte, internal.HexBytes, error,
) {
	// get the data of the auth token and the user from the storage
	authTokenData, userData, err := c.Storage.UserAuthToken(token)
	if err != nil {
		return nil, nil, nil, ErrInvalidAuthToken
	}
	// check if the user is already signing
	if c.isLocked(userData.ID, processID) {
		return nil, nil, nil, ErrUserAlreadySigning
	}
	// lock the user data to avoid concurrent signing
	c.lock(userData.ID, processID)
	// ensure that the auth token has been verified
	if !authTokenData.Verified {
		return nil, nil, nil, ErrAuthTokenNotVerified
	}
	// check if the user belongs to the bundle
	bundleData, ok := userData.Bundles[authTokenData.BundleID.String()]
	if !ok {
		return nil, nil, nil, ErrUserNotBelongsToBundle
	}
	// check if the user belongs to the process
	if bundleData.Processes == nil {
		return nil, nil, nil, ErrUserNotBelongsToProcess
	}
	processData, ok := bundleData.Processes[processID.String()]
	if !ok {
		return nil, nil, nil, ErrUserNotBelongsToProcess
	}
	// check if the process has been consumed
	if processData.Consumed {
		return nil, nil, nil, ErrProcessAlreadyConsumed
	}
	// prepare the data for the signature
	caBundle := &models.CAbundle{
		ProcessId: processID,
		Address:   address,
	}
	// encode the data to sign
	signatureMsg, err := proto.Marshal(caBundle)
	if err != nil {
		return nil, nil, nil, ErrPrepareSignature
	}
	// generate the salt
	salt := [saltedkey.SaltSize]byte{}
	copy(salt[:], processID[:saltedkey.SaltSize])

	return authTokenData.UserID, &salt, signatureMsg, nil
}

func (c *CSP) finishSignProcess(token, address, processID internal.HexBytes) error {
	// get the data of the auth token and the user from the storage
	authTokenData, userData, err := c.Storage.UserAuthToken(token)
	if err != nil {
		return ErrInvalidAuthToken
	}
	// check if the user belongs to the bundle
	bundleData, ok := userData.Bundles[authTokenData.BundleID.String()]
	if !ok {
		return ErrUserNotBelongsToBundle
	}
	// check if the user belongs to the process
	if bundleData.Processes == nil {
		return ErrUserNotBelongsToProcess
	}
	processData, ok := bundleData.Processes[processID.String()]
	if !ok {
		return ErrUserNotBelongsToProcess
	}
	// update the process data to mark it as consumed, and set the
	// token used and the time of the signature
	processData.Consumed = true
	processData.At = time.Now()
	processData.WithToken = authTokenData.Token
	processData.WithAddress = address
	// update the process in the bundle
	bundleData.Processes[processID.String()] = processData
	// update the bundle in the user data
	userData.Bundles[authTokenData.BundleID.String()] = bundleData
	// update the user data in the storage
	if err := c.Storage.SetUser(userData); err != nil {
		return ErrStorageFailure
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
