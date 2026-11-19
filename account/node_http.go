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
		return &nodeStatusError{Status: resp.StatusCode, Body: body}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxNodeResponseBytes)).Decode(out); err != nil {
		return fmt.Errorf("could not decode response: %w", err)
	}
	return nil
}

// nodeStatusError is a non-200 answer of the node, so callers can act on its API error code.
type nodeStatusError struct {
	Status int
	Body   []byte
}

func (e *nodeStatusError) Error() string {
	return fmt.Sprintf("unexpected status %d (%s)", e.Status, e.Body)
}

// apiCode returns the node's API error code from a {"error":...,"code":N} body, or 0.
func (e *nodeStatusError) apiCode() int {
	var body struct {
		Code int `json:"code"`
	}
	_ = json.Unmarshal(e.Body, &body)
	return body.Code
}
