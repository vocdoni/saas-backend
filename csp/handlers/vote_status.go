package handlers

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/dvote/vochain/state"
)

// VoteOnChainLookup reports whether a vote with this nullifier is on chain.
// (false, nil) means "not on chain"; an error means "unknown".
type VoteOnChainLookup func(ctx context.Context, nullifier []byte) (bool, error)

// VoteStatus is a voter's status on one election: Signed once the CSP signed its ballot, Voted
// only when the vote is on chain.
type VoteStatus struct {
	Signed bool
	Voted  bool
}

// VoteStatusResolver resolves voters' VoteStatus from their CSP process rows, caching on the rows
// the votes it finds on chain.
type VoteStatusResolver struct {
	Store  *db.MongoStorage
	Lookup VoteOnChainLookup
}

// Resolve reports a voter's status on one election from its CSP process row (nil: the CSP never
// signed for it):
//   - not signed: neither;
//   - signed in a blind census: the CSP never learned the address, so Voted is always false and
//     the chain is not asked;
//   - signed and already cached as on chain: Voted, with no chain call;
//   - otherwise the row's nullifier is looked up. A vote found is cached on the row (a failed
//     write is only logged: the next call looks it up again). A failed lookup is an error, never
//     a "not voted"; the returned status still carries Signed.
func (r VoteStatusResolver) Resolve(ctx context.Context, row *db.CSPProcess) (VoteStatus, error) {
	switch {
	case row == nil || !row.Used:
		return VoteStatus{}, nil
	case len(row.UsedAddress) == 0:
		return VoteStatus{Signed: true}, nil
	case row.VotedOnChain:
		return VoteStatus{Signed: true, Voted: true}, nil
	}
	nullifier := state.GenerateNullifier(common.BytesToAddress(row.UsedAddress), row.ProcessID)
	onChain, err := r.Lookup(ctx, nullifier)
	if err != nil {
		return VoteStatus{Signed: true}, fmt.Errorf("could not look up vote %x: %w", nullifier, err)
	}
	if onChain {
		if err := r.Store.MarkCSPProcessVotedOnChain(row.UserID, row.ProcessID); err != nil {
			log.Warnw("could not cache vote on chain", "processId", row.ProcessID.String(), "error", err)
		}
	}
	return VoteStatus{Signed: true, Voted: onChain}, nil
}
