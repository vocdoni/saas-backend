package signature

import "github.com/vocdoni/saas-backend/internal"

type Signature interface {
	Sign(token, salt, msg internal.HexBytes) (internal.HexBytes, error)
}
