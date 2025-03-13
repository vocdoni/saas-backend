package signers

import "github.com/vocdoni/saas-backend/internal"

type SignerType string

const (
	// SignerTypeBlind is a secp256k1 blind signature
	SignerTypeBlind SignerType = "blind"
	// SignerTypeEthereum is the standard secp256k1 signature used in Ethereum
	SignerTypeEthereum SignerType = "ecdsa"
	// SignerTypeSharedKey identifier the shared key (common for all users on
	// the same processId)
	SignerTypeSharedKey SignerType = "sharedkey"
)

// Signer is the interface that must be implemented by all signers. A signer
// is an entity that can sign a message using a specific algorithm. This
// signatures are used as a proof to vote in a process for CSP users.
type Signer interface {
	// Init initializes the signer. It receives an instance of a key-value
	// database for internal use and the root key that will be used to sign
	// the messages. It returns an error if the signer cannot be initialized.
	Init(kvdb *KeyStore, rootKey internal.HexBytes) error
	// Sign signs a message using the root key and the salt. It returns the
	// signature of the message. It returns an error if the message cannot be
	// signed. If the salt is nil, it is not used. The token is used to
	// identify the user that is signing the message.
	Sign(token, salt, msg internal.HexBytes) (internal.HexBytes, error)
}
