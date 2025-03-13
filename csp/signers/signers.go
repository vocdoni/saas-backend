package signers

import (
	"fmt"

	"go.vocdoni.io/proto/build/go/models"
)

type SignerType string

var (
	// SignerTypeBlindSalted
	SignerTypeBlindSalted SignerType = SignerType(models.ProofCA_ECDSA_BLIND_PIDSALTED.String())
	// SignerTypeEthereum is the standard secp256k1 signature used in Ethereum
	SignerTypeECDSASalted SignerType = SignerType(models.ProofCA_ECDSA_PIDSALTED.String())
)

var (
	// ErrInvalidSignerType is returned when the signer type is not supported
	ErrInvalidSignerType = fmt.Errorf("invalid signer type")
	// ErrInvalidRootKey is returned when the root key provided is not valid
	// for the signer type
	ErrInvalidRootKey = fmt.Errorf("invalid root key")
	// ErrSignOperation is returned when the signer cannot sign the message
	ErrSignOperation = fmt.Errorf("cannot sign the message")
)
