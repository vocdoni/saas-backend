package account

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sync"

	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/api"
	"golang.org/x/sync/errgroup"
)

// ballotCountsFetchers bounds the concurrent single-vote fetches of one ElectionBallotCounts run.
const ballotCountsFetchers = 8

// decodeVotePackage decodes a vote's package exactly as the chain's tally does (json.Unmarshal into a
// struct with a string "nonce" and an integer "votes" array, keys matched case-insensitively), so no
// voter can make the counts unreadable. ok is false for a ballot the tally skips: an empty package, one
// that does not decode, the node's {"encrypted": null} fallback for a vote stored with a nil package, or
// its {"encrypted": ...} fallback for a vote with key indexes that it could not decrypt (the chain could
// not tally either). Anything else is tallied, with nil votes (an empty
// ballot) when the package has no "votes". It never errors and never panics.
func decodeVotePackage(vote *api.Vote) (votes []int, ok bool) {
	if len(vote.VotePackage) == 0 {
		return nil, false
	}
	var pkg struct {
		Nonce     string          `json:"nonce,omitempty"`
		Votes     []int           `json:"votes"`
		Encrypted json.RawMessage `json:"encrypted"`
	}
	if err := json.Unmarshal(vote.VotePackage, &pkg); err != nil {
		return nil, false
	}
	if len(pkg.Encrypted) > 0 && (string(pkg.Encrypted) == "null" || len(vote.EncryptionKeyIndexes) > 0) {
		return nil, false
	}
	return pkg.Votes, true
}

// voteWeight parses a vote's weight (a decimal big integer); the node omits it for weight 1.
func voteWeight(vote *api.Vote) (*big.Int, error) {
	if vote.VoteWeight == "" {
		return big.NewInt(1), nil
	}
	w, ok := new(big.Int).SetString(vote.VoteWeight, 10)
	if !ok {
		return nil, fmt.Errorf("vote %x has an unparsable weight %q", []byte(vote.VoteID), vote.VoteWeight)
	}
	return w, nil
}

// ballotTally accumulates classified ballots; safe for concurrent use.
type ballotTally struct {
	mu            sync.Mutex
	blank         uint64
	blankWeight   big.Int
	invalid       uint64
	invalidWeight big.Int
	optionCounts  []uint64
	optionWeights []big.Int
}

func newBallotTally(width int) *ballotTally {
	return &ballotTally{optionCounts: make([]uint64, width), optionWeights: make([]big.Int, width)}
}

func (bt *ballotTally) add(kind BallotKind, selected []int, weight *big.Int) {
	bt.mu.Lock()
	defer bt.mu.Unlock()
	switch kind {
	case BallotBlank:
		bt.blank++
		bt.blankWeight.Add(&bt.blankWeight, weight)
	case BallotInvalid:
		bt.invalid++
		bt.invalidWeight.Add(&bt.invalidWeight, weight)
	default:
		for _, i := range selected {
			bt.optionCounts[i]++
			bt.optionWeights[i].Add(&bt.optionWeights[i], weight)
		}
	}
}

func (bt *ballotTally) counts() *db.BallotCounts {
	out := &db.BallotCounts{
		BlankVoteCount:    bt.blank,
		BlankVoteWeight:   bt.blankWeight.String(),
		InvalidVoteCount:  bt.invalid,
		InvalidVoteWeight: bt.invalidWeight.String(),
	}
	if len(bt.optionCounts) > 0 {
		out.OptionVoteCounts = bt.optionCounts
		out.OptionVoteWeights = make([]string, len(bt.optionWeights))
		for i := range bt.optionWeights {
			out.OptionVoteWeights[i] = bt.optionWeights[i].String()
		}
	}
	return out
}

// ElectionBallotCounts computes the blank, invalid and per-option ballot counts and weights of the
// on-chain election with the given id. It pages the votes list (GET /votes?electionId=, ballotCountsPageSize
// rows per page, up to the last page the first one reports) and fetches every listed vote (the list rows
// carry neither package nor weight) with at most ballotCountsFetchers fetches in flight, then classifies
// each ballot with c. Rows are one per nullifier carrying the latest package, so overwritten votes count
// once. An election with no votes makes no request.
//
// Every request is tried up to voteReadAttempts times (transport errors, 5xx, 429 and 404 are retried
// after voteReadBackoff). It is still all-or-nothing after the retries, so wrong counts are never
// returned: the first read that exhausts them, unparsable weight, or ctx cancellation (which also stops
// the in-flight reads) stops scheduling pages and fetches and is returned; and so is a listed row count
// different from voteCount (an index still catching up). A package the tally would skip is not an error:
// it counts as invalid.
func (a *Account) ElectionBallotCounts(
	ctx context.Context, electionID []byte, voteCount uint64, c BallotClassifier,
) (*db.BallotCounts, error) {
	tally := newBallotTally(c.Width())
	if voteCount == 0 {
		return tally.counts(), nil
	}
	g, gctx := errgroup.WithContext(ctx)
	// one slot for the page loop below, which itself schedules the vote fetches into the group
	g.SetLimit(ballotCountsFetchers + 1)
	var listed uint64
	g.Go(func() error {
		lastPage := uint64(0)
		for page := 0; uint64(page) <= lastPage; page++ {
			if err := gctx.Err(); err != nil {
				return err
			}
			list, err := a.votes.listPage(gctx, electionID, page)
			if err != nil {
				return err
			}
			if page == 0 {
				if list.Pagination == nil {
					return fmt.Errorf("votes list has no pagination")
				}
				lastPage = list.Pagination.LastPage
			}
			for _, row := range list.Votes {
				if err := gctx.Err(); err != nil {
					return err
				}
				listed++
				voteID := internal.HexBytes(row.VoteID)
				g.Go(func() error {
					if err := gctx.Err(); err != nil {
						return err
					}
					return a.countVote(gctx, voteID, c, tally)
				})
			}
		}
		return nil
	})
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("could not count ballots of election %x: %w", electionID, err)
	}
	if listed != voteCount {
		return nil, fmt.Errorf("could not count ballots of election %x: %d votes listed, election reports %d",
			electionID, listed, voteCount)
	}
	return tally.counts(), nil
}

// countVote fetches one vote, classifies its ballot and adds it to the tally.
func (a *Account) countVote(ctx context.Context, voteID internal.HexBytes, c BallotClassifier, tally *ballotTally) error {
	vote, err := a.votes.vote(ctx, voteID)
	if err != nil {
		return err
	}
	if len(vote.VoteID) == 0 {
		vote.VoteID = voteID.Bytes()
	}
	votes, ok := decodeVotePackage(vote)
	weight, err := voteWeight(vote)
	if err != nil {
		return err
	}
	kind, selected := BallotInvalid, []int(nil)
	if ok {
		kind, selected = c.Classify(votes, weight)
	}
	tally.add(kind, selected, weight)
	return nil
}
