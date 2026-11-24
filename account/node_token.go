package account

import (
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/google/uuid"
	"go.vocdoni.io/dvote/apiclient"
)

// nodeToken is the Bearer token saas-backend presents to the node. voconed lists it in
// --trustedClientTokens, so the relay of many users' requests skips the per-client limits. It is
// also the owner token of the censuses saas-backend creates, so it is a secret. Nil keeps a random
// token per client.
var nodeToken atomic.Pointer[uuid.UUID]

// SetNodeToken sets the Bearer token for every node client created afterwards. An empty string
// leaves it unset. Call it once at startup, before New and NewAPIClient.
func SetNodeToken(token string) error {
	if token == "" {
		return nil
	}
	t, err := uuid.Parse(token)
	if err != nil {
		return fmt.Errorf("node client token is not a UUID: %w", err)
	}
	nodeToken.Store(&t)
	return nil
}

// NewAPIClient returns a node API client that presents the node token, when one is set.
func NewAPIClient(apiEndpoint string) (*apiclient.HTTPclient, error) {
	if t := nodeToken.Load(); t != nil {
		return apiclient.NewWithBearer(apiEndpoint, t)
	}
	return apiclient.New(apiEndpoint)
}

// setNodeAuth adds the node token to a request sent outside apiclient.
func setNodeAuth(req *http.Request) {
	if t := nodeToken.Load(); t != nil {
		req.Header.Set("Authorization", "Bearer "+t.String())
	}
}
