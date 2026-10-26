package account

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"go.vocdoni.io/dvote/api"
)

// ballotCountsPageSize is the page size requested from the votes list (the node's maximum).
const ballotCountsPageSize = 100

// voteReadAttempts is how many times a bulk vote read is tried before giving up.
const voteReadAttempts = 3

// voteReadBackoff is the sleep between the attempts of a bulk vote read; a variable so tests can shrink it.
var voteReadBackoff = []time.Duration{time.Second, 2 * time.Second}

// voteReader performs the bulk vote reads of ElectionBallotCounts with its own http client, so the
// connections are kept alive for all the concurrent fetches and every request is bound to a context.
type voteReader struct {
	base *url.URL
	hc   *http.Client
}

// newVoteReader returns a voteReader for the API served at apiEndpoint.
func newVoteReader(apiEndpoint string) (*voteReader, error) {
	base, err := url.Parse(apiEndpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid api endpoint %q: %w", apiEndpoint, err)
	}
	return &voteReader{
		base: base,
		hc: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: ballotCountsFetchers + 1,
				IdleConnTimeout:     30 * time.Second,
			},
		},
	}, nil
}

// listPage fetches one page of the votes list of an election.
func (r *voteReader) listPage(ctx context.Context, electionID []byte, page int) (*api.VotesList, error) {
	u := r.base.JoinPath("votes")
	u.RawQuery = url.Values{
		"electionId": {hex.EncodeToString(electionID)},
		"limit":      {strconv.Itoa(ballotCountsPageSize)},
		"page":       {strconv.Itoa(page)},
	}.Encode()
	list := &api.VotesList{}
	notFound := fmt.Errorf("votes page %d not found", page)
	if err := r.get(ctx, u, notFound, list); err != nil {
		return nil, fmt.Errorf("could not fetch votes page %d of election %x: %w", page, electionID, err)
	}
	return list, nil
}

// vote fetches one vote by its nullifier, wrapping ErrVoteNotFound when the node keeps answering 404.
func (r *voteReader) vote(ctx context.Context, nullifier []byte) (*api.Vote, error) {
	vote := &api.Vote{}
	if err := r.get(ctx, r.base.JoinPath("votes", hex.EncodeToString(nullifier)), ErrVoteNotFound, vote); err != nil {
		return nil, fmt.Errorf("could not fetch vote %x: %w", nullifier, err)
	}
	return vote, nil
}

// get GETs u and decodes the JSON body into out. It makes up to voteReadAttempts attempts, retrying on
// transport errors, 5xx, 429 and 404 (a row the index has not caught up with), and never once ctx is
// done. A final 404 is reported as notFound.
func (r *voteReader) get(ctx context.Context, u *url.URL, notFound error, out any) error {
	var lastErr error
	attempt := 0
	for ; attempt < voteReadAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(voteReadBackoff[attempt-1]):
			}
		}
		retry, err := r.getOnce(ctx, u, notFound, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retry || ctx.Err() != nil {
			attempt++
			break
		}
	}
	return fmt.Errorf("read failed after %d attempt(s): %w", attempt, lastErr)
}

// getOnce makes one request; retry tells whether a failure is worth another attempt.
func (r *voteReader) getOnce(ctx context.Context, u *url.URL, notFound error, out any) (retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false, err
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return true, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return true, err
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		if err := json.Unmarshal(body, out); err != nil {
			return false, fmt.Errorf("could not decode response: %w", err)
		}
		return false, nil
	case resp.StatusCode == http.StatusNotFound:
		return true, notFound
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return true, fmt.Errorf("status %d (%s)", resp.StatusCode, body)
	default:
		return false, fmt.Errorf("status %d (%s)", resp.StatusCode, body)
	}
}
