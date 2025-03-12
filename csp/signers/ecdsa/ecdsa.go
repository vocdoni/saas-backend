package ecdsa

import (
	"fmt"
	"math/big"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/vocdoni/saas-backend/internal"
	vocdonicrypto "go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/db"
)

// SaltSize is the size of the salt used for derive the new key
const SaltSize = 20

type EthereumSigner struct {
	rootKey internal.HexBytes
}

func (s *EthereumSigner) Init(db db.Database, rootKey internal.HexBytes) error {
	// check if the root key is valid
	if _, err := ethcrypto.ToECDSA(rootKey); err != nil {
		return fmt.Errorf("invalid root key: %w", err)
	}
	// set the key store and the root key
	s.rootKey = rootKey
	return nil
}

func (s *EthereumSigner) Sign(token, salt, msg internal.HexBytes) (internal.HexBytes, error) {
	signKeys := new(vocdonicrypto.SignKeys)
	if err := signKeys.AddHexKey(s.rootKey.String()); err != nil {
		return nil, fmt.Errorf("cannot add root key: %w", err)
	}
	if salt != nil {
		signKeys.Private.D = new(big.Int).Add(signKeys.Private.D, new(big.Int).SetBytes(salt))
	}
	signature, err := signKeys.SignEthereum(msg)
	if err != nil {
		return nil, fmt.Errorf("cannot sign: %w", err)
	}
	return signature, nil
}
