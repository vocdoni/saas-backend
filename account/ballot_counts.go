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

// decodeVotePackage decodes a vote's package the way the chain's tally does (a JSON object with an
// integer "votes" array, keys matched case-insensitively). ok is false for a package the tally skips
// as undecodable: valid JSON that is not such an object, has no "votes" key, or whose votes are not
// integers. A present but empty "votes" array decodes to an empty, ok ballot. It errors, instead of
// classifying, when the package is empty or is the node's {"encrypted": ...} fallback for a vote it
// could not decrypt: both mean the ballot could not be read, not that it is invalid.
func decodeVotePackage(vote *api.Vote) (votes []int, ok bool, err error) {
	if len(vote.VotePackage) == 0 {
		return nil, false, fmt.Errorf("vote %x has an empty package", []byte(vote.VoteID))
	}
	var pkg struct {
		Votes     *[]int          `json:"votes"`
		Encrypted json.RawMessage `json:"encrypted"`
	}
	decodeErr := json.Unmarshal(vote.VotePackage, &pkg)
	if len(pkg.Encrypted) > 0 && string(pkg.Encrypted) != "null" {
		return nil, false, fmt.Errorf("vote %x package is still encrypted", []byte(vote.VoteID))
	}
	if decodeErr != nil || pkg.Votes == nil {
		return nil, false, nil
	}
	return *pkg.Votes, true, nil
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
// on-chain election with the given id. It pages the votes list and fetches every listed vote (the list
// rows carry neither package nor weight) with at most ballotCountsFetchers fetches in flight, then
// classifies each ballot with c. Rows are one per nullifier carrying the latest package, so overwritten
// votes count once.
//
// It is all-or-nothing, so wrong counts are never returned: the first chain error, listed vote the node
// cannot find, empty or still-encrypted package, unparsable weight, or ctx cancellation stops scheduling
// pages and fetches (the ones already in flight finish on the client's timeout) and is returned;
// and so is a listed row count different from voteCount (an index still catching up).
// A package the tally would skip is not an error: it counts as invalid.
func (a *Account) ElectionBallotCounts(
	ctx context.Context, electionID []byte, voteCount uint64, c BallotClassifier,
) (*db.BallotCounts, error) {
	tally := newBallotTally(c.Width())
	g, gctx := errgroup.WithContext(ctx)
	// one slot for the page loop below, which itself schedules the vote fetches into the group
	g.SetLimit(ballotCountsFetchers + 1)
	var listed uint64
	g.Go(func() error {
		for page := 0; ; page++ {
			if err := gctx.Err(); err != nil {
				return err
			}
			rows, err := a.electionVotesPage(electionID, page)
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				return nil
			}
			for _, row := range rows {
				if err := gctx.Err(); err != nil {
					return err
				}
				listed++
				voteID := internal.HexBytes(row.VoteID)
				g.Go(func() error {
					if err := gctx.Err(); err != nil {
						return err
					}
					return a.countVote(voteID, c, tally)
				})
			}
		}
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
func (a *Account) countVote(voteID internal.HexBytes, c BallotClassifier, tally *ballotTally) error {
	vote, err := a.VoteByNullifier(voteID)
	if err != nil {
		return err
	}
	if len(vote.VoteID) == 0 {
		vote.VoteID = voteID.Bytes()
	}
	votes, ok, err := decodeVotePackage(vote)
	if err != nil {
		return err
	}
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
