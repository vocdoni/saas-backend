package signers

import "github.com/vocdoni/saas-backend/internal"

type Signer interface {
	Sign(token, salt, msg internal.HexBytes) (internal.HexBytes, error)
}
