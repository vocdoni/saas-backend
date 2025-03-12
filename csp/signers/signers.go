package signers

import (
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/db"
)

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

type Signer interface {
	Init(db db.Database) error
	Sign(salt, msg internal.HexBytes) (internal.HexBytes, error)
}
