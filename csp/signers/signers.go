package signers

import (
	"fmt"

	"github.com/vocdoni/saas-backend/internal"
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

// Signer is the interface that must be implemented by all signers. A signer
// is an entity that can sign a message using a specific algorithm. This
// signatures are used as a proof to vote in a process for CSP users.
type Signer interface {
	// Init initializes the signer. It receives an instance of a key-value
	// database for internal use and the root key that will be used to sign
	// the messages. It returns an error if the signer cannot be initialized.
	Init(rootKey internal.HexBytes) error
	// Sign signs a message using the root key and the salt. It returns the
	// signature of the message. It returns an error if the message cannot be
	// signed. If the salt is nil, it is not used. The token is used to
	// identify the user that is signing the message.
	Sign(token, salt, msg internal.HexBytes) (internal.HexBytes, error)

	// PubKey returns the public key of the signer.
	PubKey(salt internal.HexBytes) (internal.HexBytes, error)

	// NewTokenRequest returns a new token request for the signer
	NewTokenRequest() internal.HexBytes
}
