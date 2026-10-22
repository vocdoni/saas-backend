package account

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Retry knobs of SubmitVote. Package vars rather than constants so tests can shorten them.
var (
	// voteRetryBase is the first backoff between two sends of the same vote tx; it doubles
	// after every transient failure, up to voteRetryCap.
	voteRetryBase = 500 * time.Millisecond
	voteRetryCap  = 4 * time.Second
	// voteRetryBudget is the wall-clock deadline for starting a new send. It is checked
	// before each attempt; an attempt already in flight is not interrupted.
	voteRetryBudget = 30 * time.Second
	// voteMinedTimeout bounds each wait for the vote tx to be included in a block, the same
	// bound SubmitSignedTx uses.
	voteMinedTimeout = 40 * time.Second
)

const (
	// apiErrorPrefix starts every error dvote's SendTx returns for a non-200 answer
	// ("API error: <status> (<body>)"); a transport failure has no such prefix.
	apiErrorPrefix = "API error: "
	// txInCacheMsg is cometbft's mempool.ErrTxInCache, which the node returns (HTTP 500) for a
	// tx it already holds: an earlier send of the same bytes was accepted.
	txInCacheMsg = "already exists in cache"
	// mempoolFullMsg is what dvote's Request returns once its own 503 ("mempool is full")
	// retries run out: it closes the last 503 body and then reads it (apiclient/client.go:210-232),
	// so a persistent 503 never surfaces as "API error: 503".
	mempoolFullMsg = "read on closed response body"
)

// sendTxStatus returns the HTTP status of an error returned by dvote's SendTx, or 0 when the
// request never got an HTTP answer (connection reset, client timeout...). The string is all dvote
// exposes.
func sendTxStatus(err error) int {
	rest, ok := strings.CutPrefix(err.Error(), apiErrorPrefix)
	if !ok {
		return 0
	}
	code, _, _ := strings.Cut(rest, " ")
	status, convErr := strconv.Atoi(code)
	if convErr != nil {
		return 0
	}
	return status
}

// transientSendErr reports whether a SendTx error may be gone on a resend, and whether the node
// may have taken the tx anyway: a transport error, or a 502/504 that only a proxy in front of
// the node answers. A 503 is definitive here: dvote has already retried it, waiting a block each
// time.
func transientSendErr(err error) bool {
	if strings.Contains(err.Error(), mempoolFullMsg) {
		return false
	}
	switch sendTxStatus(err) {
	case 0, 502, 504:
		return true
	default:
		return false
	}
}

// SubmitVote relays a signed vote tx and confirms it by nullifier. Unlike SubmitSignedTx, it
// resends the tx on a transient transport error until voteRetryBudget runs out, treats the
// node's "already in cache" answer as an accepted send, and before reporting any failure asks
// the chain whether a vote with this nullifier exists, so a vote that landed despite the errors
// is never reported failed. Resending is safe: the bytes are identical, so the chain sees the
// same ballot under the same nullifier.
//
// voteID is the vote nullifier as assigned on chain. relayedBefore is true when this call cannot
// claim the vote as its own: the node already held the tx before any send of this call, or the
// sends failed and the vote was found by its nullifier. The caller uses it to avoid metering a
// vote twice. An empty nullifier skips the lookup.
func (a *Account) SubmitVote(ctx context.Context, stx, nullifier []byte) (
	voteID []byte, relayedBefore bool, err error,
) {
	// the tx hash is sha256 of the signed bytes (vochaintx.TxKey), so it is known even when the
	// node's answer to the send was lost
	txHash := sha256.Sum256(stx)
	deadline := time.Now().Add(voteRetryBudget)
	backoff := voteRetryBase
	// maybeAccepted: a send of this call may have reached the node with its answer lost.
	// ours: a send of this call is known to have been accepted.
	maybeAccepted, sent, ours := false, false, false
	var sendErr error
	for {
		var data []byte
		_, data, sendErr = a.client.SendTx(stx)
		if sendErr == nil || strings.Contains(sendErr.Error(), txInCacheMsg) {
			// in cache after an ambiguous send of ours: that send was accepted. In cache on the
			// first send: someone relayed these bytes before us.
			voteID, sent, ours = data, true, sendErr == nil || maybeAccepted
			break
		}
		if !transientSendErr(sendErr) {
			break
		}
		maybeAccepted = true
		if !time.Now().Add(backoff).Before(deadline) || !sleepCtx(ctx, backoff) {
			break
		}
		backoff = min(backoff*2, voteRetryCap)
	}

	failure := sendErr
	if sent {
		minedErr := a.waitVoteMined(ctx, txHash[:])
		if minedErr == nil {
			if len(voteID) == 0 { // the in-cache answer carries no response data
				voteID = nullifier
			}
			return voteID, !ours, nil
		}
		failure = minedErr
	} else if maybeAccepted && len(nullifier) > 0 {
		// the node may hold the tx from a send whose answer was lost: give it the same chance
		// to be mined before asking for the vote. A failed wait is only a ctx expiry, so the
		// lookup below decides either way. A mined local hash means one of this call's sends
		// was taken: the vote is ours.
		ours = a.waitVoteMined(ctx, txHash[:]) == nil
	}
	if len(nullifier) == 0 {
		return nil, false, fmt.Errorf("could not submit vote tx: %w", failure)
	}
	if _, lookupErr := a.VoteByNullifier(nullifier); lookupErr != nil {
		if errors.Is(lookupErr, ErrVoteNotFound) {
			return nil, false, fmt.Errorf("could not submit vote tx: %w", failure)
		}
		return nil, false, fmt.Errorf("could not submit vote tx: %w (nullifier lookup: %w)", failure, lookupErr)
	}
	return nullifier, !ours, nil
}

// waitVoteMined waits up to voteMinedTimeout for the tx with the given hash to be in a block.
func (a *Account) waitVoteMined(ctx context.Context, txHash []byte) error {
	ctx, cancel := context.WithTimeout(ctx, voteMinedTimeout)
	defer cancel()
	if _, err := a.client.WaitUntilTxIsMined(ctx, txHash); err != nil {
		return fmt.Errorf("could not wait for vote tx to be mined: %w", err)
	}
	return nil
}

// sleepCtx waits for d, returning false if ctx is done first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
