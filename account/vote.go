package account

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
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

// transientSendErr reports whether a SendTx error may be gone on a resend: a transport error, or
// a 502/504 that only a proxy in front of the node answers. A 503 is definitive here: dvote has
// already retried it, waiting a block each time.
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

// sendNeverLeft reports whether a SendTx error proves the tx never reached the node: the
// connection could not even be opened (refused, unreachable, DNS). dvote returns the HTTP client's
// error as is, so the dial failure is still in the chain. Any other transport error may have hit
// after the request was written, so the node may hold the tx.
func sendNeverLeft(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

// SubmitVote relays a signed vote tx and confirms it is on chain, returning its vote id (the vote
// nullifier as assigned on chain). Unlike SubmitSignedTx, it resends the tx on a transient transport
// error until voteRetryBudget runs out, and treats the node's "already in cache" answer as an
// accepted send. Resending is safe: the bytes are identical, so the chain sees the same ballot under
// the same nullifier.
//
// The vote is cast when this tx (its locally computed hash) is mined, whoever sent it. When it is
// not seen mined, the chain is asked for the vote under the nullifier, and only this same tx counts:
// a different ballot found there (an earlier vote, with this one rejected as an overwrite) is a
// failure. An empty nullifier skips the lookup. Whether the vote is metered is not decided here:
// the caller meters a confirmed vote once per nullifier.
func (a *Account) SubmitVote(ctx context.Context, stx, nullifier []byte) ([]byte, error) {
	// the tx hash is sha256 of the signed bytes (vochaintx.TxKey), so it is known even when the
	// node's answer to the send was lost
	txHash := sha256.Sum256(stx)
	deadline := time.Now().Add(voteRetryBudget)
	backoff := voteRetryBase
	// maybeAccepted: a send of this call may have reached the node with its answer lost.
	// sent: the node holds the tx (this call's send was accepted, or it was already in cache).
	maybeAccepted, sent := false, false
	var voteID []byte
	var sendErr error
	for {
		var data []byte
		_, data, sendErr = a.client.SendTx(stx)
		if sendErr == nil || strings.Contains(sendErr.Error(), txInCacheMsg) {
			voteID, sent = data, true
			break
		}
		if !transientSendErr(sendErr) {
			break
		}
		// a dial failure is retried too, but proves nothing reached the node: it is no reason to
		// wait for the tx to be mined afterwards
		if !sendNeverLeft(sendErr) {
			maybeAccepted = true
		}
		if !time.Now().Add(backoff).Before(deadline) || !sleepCtx(ctx, backoff) {
			break
		}
		backoff = min(backoff*2, voteRetryCap)
	}
	if len(voteID) == 0 { // the in-cache answer carries no response data
		voteID = nullifier
	}

	failure := sendErr
	if sent || maybeAccepted {
		// the node holds the tx, or may from a send whose answer was lost: give it the chance to
		// be mined. A mined hash means these exact bytes are on chain, so the vote is cast.
		minedErr := a.waitVoteMined(ctx, txHash[:])
		if minedErr == nil {
			return voteID, nil
		}
		if sent {
			failure = minedErr
		}
	}
	if len(nullifier) == 0 {
		return nil, fmt.Errorf("could not submit vote tx: %w", failure)
	}
	vote, lookupErr := a.VoteByNullifier(nullifier)
	switch {
	case errors.Is(lookupErr, ErrVoteNotFound):
		return nil, fmt.Errorf("could not submit vote tx: %w", failure)
	case lookupErr != nil:
		return nil, fmt.Errorf("could not submit vote tx: %w (nullifier lookup: %w)", failure, lookupErr)
	case !bytes.Equal(vote.TxHash, txHash[:]):
		// the nullifier holds another ballot: this one was rejected (e.g. overwrite count reached)
		return nil, fmt.Errorf("could not submit vote tx: %w (the chain holds another vote under its nullifier)", failure)
	}
	// this very tx is on chain, though not seen mined here
	return nullifier, nil
}

// waitVoteMined waits up to voteMinedTimeout for the tx with the given hash to be in a block.
func (a *Account) waitVoteMined(ctx context.Context, txHash []byte) error {
	ctx, cancel := context.WithTimeout(ctx, voteMinedTimeout)
	defer cancel()
	if err := waitTxMined(ctx, a.client, txHash); err != nil {
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
