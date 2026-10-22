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

// ResolveVoteStatus reports a voter's status on one election from its CSP process row (nil: the
// CSP never signed for it). hasSigned is the CSP signature; hasVoted is a vote on chain:
//   - not signed: neither;
//   - signed in a blind census: the CSP never learned the address, so hasVoted is always false
//     and the chain is not asked;
//   - signed and already cached as on chain: hasVoted, with no chain call;
//   - otherwise the row's nullifier is looked up. A vote found is cached on the row (a failed
//     write is only logged: the next call looks it up again). A failed lookup is an error, never
//     a "not voted".
func ResolveVoteStatus(ctx context.Context, store *db.MongoStorage, lookup VoteOnChainLookup, row *db.CSPProcess,
) (hasSigned, hasVoted bool, err error) {
	switch {
	case row == nil || !row.Used:
		return false, false, nil
	case len(row.UsedAddress) == 0:
		return true, false, nil
	case row.VotedOnChain:
		return true, true, nil
	}
	nullifier := state.GenerateNullifier(common.BytesToAddress(row.UsedAddress), row.ProcessID)
	onChain, err := lookup(ctx, nullifier)
	if err != nil {
		return true, false, fmt.Errorf("could not look up vote %x: %w", nullifier, err)
	}
	if onChain {
		if err := store.MarkCSPProcessVotedOnChain(row.UserID, row.ProcessID); err != nil {
			log.Warnw("could not cache vote on chain", "processId", row.ProcessID.String(), "error", err)
		}
	}
	return true, onChain, nil
}
