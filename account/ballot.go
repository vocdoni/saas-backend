package account

import (
	"fmt"

	"github.com/vocdoni/saas-backend/db"
)

// VoteTypeFromQuestion translates a voting-process question's friendly ballot type into
// the on-chain vote options (db.VoteType). A non-nil BallotProtocol is a raw override and
// takes priority over Type/TypeSetup. singlechoice picks one of N choices (one field,
// value = chosen index); multichoice is approval-style (one field per choice, each 0/1,
// with MaxTotalCost bounding the number of selections). minChoices is a validation hint
// only — the current protocol has no on-chain minimum-count field.
func VoteTypeFromQuestion(q *db.VotingProcessQuestion) (db.VoteType, error) {
	if q.BallotProtocol != nil {
		bp := q.BallotProtocol
		return db.VoteType{
			MaxCount:          bp.MaxCount,
			MaxValue:          bp.MaxValue,
			MaxVoteOverwrites: bp.MaxVoteOverwrites,
			CostFromWeight:    bp.CostFromWeight,
			CostExponent:      bp.CostExponent,
			UniqueChoices:     bp.UniqueValues,
			MaxTotalCost:      bp.MaxTotalCost,
		}, nil
	}
	if len(q.Choices) == 0 {
		return db.VoteType{}, fmt.Errorf("question has no choices")
	}
	switch q.Type {
	case db.VotingTypeSingleChoice:
		// MaxValue must cover the highest client-supplied Choice.Value, which need not be a
		// contiguous 0..n-1 range, so derive it from the actual values rather than the count.
		var maxValue uint32
		for i := range q.Choices {
			if v := q.Choices[i].Value; v > maxValue {
				maxValue = v
			}
		}
		return db.VoteType{MaxCount: 1, MaxValue: maxValue}, nil
	case db.VotingTypeMultiChoice:
		return db.VoteType{
			MaxCount:      uint32(len(q.Choices)),
			MaxValue:      1,
			CostExponent:  1,
			MaxTotalCost:  q.TypeSetup.MaxChoices,
			UniqueChoices: q.TypeSetup.UniqueChoices,
		}, nil
	default:
		return db.VoteType{}, fmt.Errorf("unsupported question type %q", q.Type)
	}
}

// ElectionTypeFromQuestion builds the on-chain election flags for a question. autostart and
// interruptible are always on, dynamicCensus off; anonymous is deferred (always false);
// secretUntilTheEnd comes from the question.
func ElectionTypeFromQuestion(q *db.VotingProcessQuestion) db.ElectionType {
	return db.ElectionType{
		Autostart:         true,
		Interruptible:     true,
		DynamicCensus:     false,
		SecretUntilTheEnd: q.SecretUntilTheEnd,
		Anonymous:         false,
	}
}

// ComputeMaxCensusSize returns the census size to stamp on a question's election: the size
// of its eligibility subset when set, otherwise the parent census size.
func ComputeMaxCensusSize(eligibleMemberIDs []string, parentCensusSize int64) uint64 {
	if n := len(eligibleMemberIDs); n > 0 {
		return uint64(n)
	}
	if parentCensusSize > 0 {
		return uint64(parentCensusSize)
	}
	return 0
}
