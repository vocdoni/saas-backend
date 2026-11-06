package account

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/vocdoni/saas-backend/internal"
)

const (
	// maxNodeResponseBytes caps the body decoded from a node API response. The node is trusted
	// and its responses are bounded (a votes page holds at most votesPageLimit votes), so this
	// only guards against a misbehaving endpoint.
	maxNodeResponseBytes = 4 * internal.MiB
	// maxNodeErrorBodyBytes caps the excerpt of a non-200 response body quoted in the error.
	maxNodeErrorBodyBytes = 512
)

// getJSON sends a GET request for rawURL to the node, bound to ctx, and decodes its JSON
// response body into out. A status other than 200 is an error quoting the start of the body.
func getJSON(ctx context.Context, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("could not build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not send request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxNodeErrorBodyBytes))
		return fmt.Errorf("unexpected status %d (%s)", resp.StatusCode, body)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxNodeResponseBytes)).Decode(out); err != nil {
		return fmt.Errorf("could not decode response: %w", err)
	}
	return nil
}
