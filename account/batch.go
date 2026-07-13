package account

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	dvoteapi "go.vocdoni.io/dvote/api"
	dvotetypes "go.vocdoni.io/dvote/types"
)

// BatchItemStatus is the submission outcome of a single transaction in a batch.
type BatchItemStatus string

const (
	// BatchSubmitted means the mempool accepted the broadcast (NOT block-confirmed).
	BatchSubmitted BatchItemStatus = "submitted"
	// BatchFailed means the tx failed to be submitted (fail-fast stops the batch here).
	BatchFailed BatchItemStatus = "failed"
	// BatchPending means the tx was not sent (it followed the failed one).
	BatchPending BatchItemStatus = "pending"
)

// BatchItemResult is the per-input outcome of SubmitSignedTxBatch, returned in the same
// order as the input transactions. For submitted NewProcess items UpstreamID holds the
// predicted on-chain process id and Hash the tx hash (for confirmation).
type BatchItemResult struct {
	Status     BatchItemStatus
	UpstreamID internal.HexBytes
	Hash       internal.HexBytes
	Err        string
}

// SubmitSignedTxBatch submits several already-signed transactions at once, in order, via
// the node's batch endpoint (POST /chain/transactions/batch). The transactions must carry
// contiguous account nonces (see BuildNewProcessTx with an explicit Nonce). It returns a
// per-input result in input order; "submitted" means mempool-accepted, not confirmed —
// callers must confirm each submitted item on-chain (WaitTxMined) and resubmit the rest.
func (a *Account) SubmitSignedTxBatch(stxs [][]byte) ([]BatchItemResult, error) {
	res, err := a.client.SendTxBatch(stxs)
	if err != nil {
		return nil, fmt.Errorf("could not submit signed tx batch: %w", err)
	}
	return mapBatchResults(res), nil
}

// mapBatchResults flattens a node batch response into per-input results aligned with the input
// order. The node groups items by outcome while preserving input order: submitted first, then
// the single failed item, then the pending remainder. Concatenating in that order reconstructs
// the per-input results aligned with the input slice — the positional contract confirmBatch
// relies on (results[i] ↔ pending[i]).
func mapBatchResults(res *dvoteapi.TransactionBatchResult) []BatchItemResult {
	out := make([]BatchItemResult, 0, len(res.Submitted)+len(res.Failed)+len(res.Pending))
	for _, t := range res.Submitted {
		out = append(out, BatchItemResult{
			Status:     BatchSubmitted,
			UpstreamID: internal.HexBytes(t.ProcessID),
			Hash:       internal.HexBytes(t.Hash),
		})
	}
	for _, t := range res.Failed {
		out = append(out, BatchItemResult{Status: BatchFailed, Err: t.Error})
	}
	for range res.Pending {
		out = append(out, BatchItemResult{Status: BatchPending})
	}
	return out
}

// AccountNonce returns the current on-chain nonce of an organization account. Used to seed
// the explicit consecutive nonces of a publish batch with a single read.
func (a *Account) AccountNonce(org common.Address) (uint32, error) {
	acc, err := a.client.Account(org.String())
	if err != nil {
		return 0, fmt.Errorf("could not fetch organization account: %w", err)
	}
	return acc.Nonce, nil
}

// WaitTxMined blocks (up to 40s) until the transaction with the given hash is mined,
// used to confirm a batch item that was accepted by the mempool.
func (a *Account) WaitTxMined(hash internal.HexBytes) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*40)
	defer cancel()
	if _, err := a.client.WaitUntilTxIsMined(ctx, dvotetypes.HexBytes(hash)); err != nil {
		return fmt.Errorf("could not wait for tx to be mined: %w", err)
	}
	return nil
}
