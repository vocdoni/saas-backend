package statussync

import (
	"context"
	"fmt"

	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/db"
	vocdoniapi "go.vocdoni.io/dvote/api"
)

// ballotCountsProcessor computes and persists a question's blank, invalid and per-option ballot
// counts (db.BallotCounts) from its final election.
type ballotCountsProcessor struct {
	acc *account.Account
}

// NewBallotCountsProcessor returns the results processor that persists each final question's
// db.BallotCounts, reading the votes from the chain through acc.
func NewBallotCountsProcessor(acc *account.Account) ResultsProcessor {
	return &ballotCountsProcessor{acc: acc}
}

// Name identifies the processor in logs.
func (*ballotCountsProcessor) Name() string { return "ballotCounts" }

// Process computes the counts unless they are already stored (no chain call then) and persists them
// first-write-wins. Any failure persists nothing and is retried on a later trigger.
func (p *ballotCountsProcessor) Process(
	ctx context.Context, store *db.MongoStorage, q *db.VotingProcessQuestion, e *vocdoniapi.Election,
) error {
	if q.BallotCounts != nil {
		return nil
	}
	classifier, err := account.NewBallotClassifier(q)
	if err != nil {
		return fmt.Errorf("classifying ballots of question %s: %w", q.ID.Hex(), err)
	}
	counts, err := p.acc.ElectionBallotCounts(ctx, q.UpstreamID, e.VoteCount, classifier)
	if err != nil {
		return err
	}
	if err := store.SetQuestionBallotCounts(q.UpstreamID, counts); err != nil {
		return fmt.Errorf("storing ballot counts of question %s: %w", q.ID.Hex(), err)
	}
	return nil
}
