package ecdsa

import (
	"errors"
	"math/big"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/vocdoni/saas-backend/csp/signers"
	"github.com/vocdoni/saas-backend/internal"
	vocdonicrypto "go.vocdoni.io/dvote/crypto/ethereum"
)

// SaltSize is the size of the salt used for derive the new key
const SaltSize = 20

// EthereumSigner is a signer that uses an an ethereum signature (ECDSA) to
// sign a message. It needs a root key to sign the message. It implements
// the Signer interface.
type EthereumSigner struct {
	rootKey internal.HexBytes
}

// Init initializes the signer with the root key. It returns an error if the
// root key is invalid. The db.Database parameter is not used for this type
// of signer.
func (s *EthereumSigner) Init(_ *signers.KeyStore, rootKey internal.HexBytes) error {
	// check if the root key is valid
	if _, err := ethcrypto.ToECDSA(rootKey); err != nil {
		return errors.Join(signers.ErrInvalidRootKey, err)
	}
	// set the key store and the root key
	s.rootKey = rootKey
	return nil
}

// Sign signs a message using the root key and the salt. It returns the
// signature of the message. It returns an error if the message cannot be
// signed. If the salt is nil, it is not used. The token is not used for
// this type of signer.
func (s *EthereumSigner) Sign(_, salt, msg internal.HexBytes) (internal.HexBytes, error) {
	signKeys := new(vocdonicrypto.SignKeys)
	if err := signKeys.AddHexKey(s.rootKey.String()); err != nil {
		return nil, errors.Join(signers.ErrInvalidRootKey, err)
	}
	if salt != nil {
		signKeys.Private.D = new(big.Int).Add(signKeys.Private.D, salt.BigInt())
	}
	signature, err := signKeys.SignEthereum(msg)
	if err != nil {
		return nil, errors.Join(signers.ErrSignOperation, err)
	}
	return signature, nil
}
