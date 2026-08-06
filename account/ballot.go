package account

import (
	"fmt"
	"slices"

	"github.com/vocdoni/saas-backend/db"
)

// A question describes its ballot twice: as a named type (singlechoice, multichoice, ranked,
// cumulative) with its TypeSetup, and as a raw BallotProtocol. This file keeps the two halves
// in step.
//
// BallotProtocolFromType is the single mapping table, and the reverse direction is defined as
// equality against it — QuestionTypeFromBallotProtocol asks "which named type would produce
// exactly this protocol?". So the two can never drift apart, and ResolveBallotShape can fill in
// whichever half a client left out. What is stored obeys, for any question with choices:
//
//	Type != "" ⇒ BallotProtocolFromType(Type, TypeSetup, Choices) == *BallotProtocol
//
// A question with no choices yet is the one carve-out: there is no ballot to derive either half
// from, so a draft can be stored with neither a protocol nor a satisfied invariant. It cannot be
// published (publish validation rejects a choiceless question), and adding its choices reconciles
// it.
//
// Which matters because a question is immutable once published: a stored shape that disagreed
// with the election it minted would go on disagreeing forever, and the API would keep serving
// the half that was not used.

// VoteTypeFromQuestion translates a question's ballot specification into the on-chain vote
// options (db.VoteType). Questions written since the two halves were reconciled always carry a
// BallotProtocol, and it is authoritative; a legacy question carrying only Type/TypeSetup is
// translated through BallotProtocolFromType.
//
// Note that path never turns TypeSetup.UniqueChoices into an on-chain uniqueValues flag, unlike
// the mapping it replaces: on the dense multichoice layout that combination admits no valid
// ballot at all and tallies silently to zero (issue #619).
func VoteTypeFromQuestion(q *db.VotingProcessQuestion) (db.VoteType, error) {
	bp := q.BallotProtocol
	if bp == nil {
		derived, err := BallotProtocolFromType(q.Type, q.TypeSetup, q.Choices)
		if err != nil {
			return db.VoteType{}, err
		}
		bp = derived
	}
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

// BallotProtocolFromType derives the canonical on-chain ballot parameters of a named question
// type:
//   - singlechoice picks one of N choices (one field, value = the chosen Choice.Value);
//   - multichoice is approval-style (one field per choice, each 0/1, with MaxTotalCost bounding
//     the number of selections);
//   - ranked assigns every choice a distinct rank (one field per choice holding its rank 0..N-1,
//     so uniqueValues holds); the convention is highest-value-wins;
//   - cumulative spreads a budget across the choices (maxValue 0 is the "amounts" marker; the
//     per-value cap comes from MaxTotalCost and CostExponent, 1 for a linear budget or 2 for
//     quadratic).
//
// setup.MinChoices has no protocol counterpart and is ignored — the chain has no minimum-count
// field. setup.UniqueChoices is ignored too: the only named type that uses uniqueValues is ranked,
// and it derives the flag from the type, not from this field. Callers reject a multichoice that
// sets it at the API boundary rather than silently dropping it.
//
// MaxVoteOverwrites and CostFromWeight are always zero here: QuestionTypeSetup has no field that
// could express them, so a protocol carrying either is simply not a named shape.
func BallotProtocolFromType(
	qType string, setup db.QuestionTypeSetup, choices []db.Choice,
) (*db.BallotProtocol, error) {
	if len(choices) == 0 {
		return nil, fmt.Errorf("question has no choices")
	}
	switch qType {
	case db.VotingTypeSingleChoice:
		// MaxValue must cover the highest client-supplied Choice.Value, which need not be a
		// contiguous 0..n-1 range, so derive it from the actual values rather than the count.
		var maxValue uint32
		for i := range choices {
			if v := choices[i].Value; v > maxValue {
				maxValue = v
			}
		}
		return &db.BallotProtocol{MaxCount: 1, MaxValue: maxValue}, nil
	case db.VotingTypeMultiChoice:
		return &db.BallotProtocol{
			MaxCount:     uint32(len(choices)),
			MaxValue:     1,
			CostExponent: 1,
			MaxTotalCost: setup.MaxChoices,
		}, nil
	case db.VotingTypeRanked:
		// Ranking needs at least two choices to mean anything. maxValue reaches N-1 so the N
		// distinct ranks (0..N-1) fit, which is also what makes uniqueValues satisfiable.
		if len(choices) < 2 {
			return nil, fmt.Errorf("ranked ballot requires at least two choices")
		}
		return &db.BallotProtocol{
			MaxCount:     uint32(len(choices)),
			MaxValue:     uint32(len(choices) - 1),
			UniqueValues: true,
		}, nil
	case db.VotingTypeCumulative:
		// maxValue 0 is the protocol's "values are aggregable amounts" marker: each field holds a
		// credit, bounded in total by MaxTotalCost (the budget) raised to CostExponent. CostExponent
		// 1 is a linear budget, 2 is quadratic.
		if setup.Budget == 0 {
			return nil, fmt.Errorf("cumulative ballot requires a budget greater than zero")
		}
		if setup.CostExponent != 1 && setup.CostExponent != 2 {
			return nil, fmt.Errorf("cumulative ballot costExponent must be 1 (linear) or 2 (quadratic)")
		}
		return &db.BallotProtocol{
			MaxCount:     uint32(len(choices)),
			CostExponent: setup.CostExponent,
			MaxTotalCost: setup.Budget,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported question type %q", qType)
	}
}

// OpenChoiceMatcher returns a predicate reporting whether a decoded vote package (its votes values)
// selected the question's open-value choice, or nil when the question has no open choice or its
// ballot layout gives "selected" no meaning: ranked ranks every choice on every ballot, and an
// unnamed protocol has no defined layout. Layouts follow BallotProtocolFromType — singlechoice's
// single field holds the chosen Choice.Value; multichoice and cumulative carry one field per choice
// (by position in the choices slice), holding a 0/1 selection flag and a credit amount respectively.
func OpenChoiceMatcher(qType string, choices []db.Choice) func(votes []int) bool {
	open := slices.IndexFunc(choices, func(c db.Choice) bool { return c.OpenValue })
	if open == -1 {
		return nil
	}
	switch qType {
	case db.VotingTypeSingleChoice:
		want := int(choices[open].Value)
		return func(votes []int) bool { return len(votes) == 1 && votes[0] == want }
	case db.VotingTypeMultiChoice:
		return func(votes []int) bool { return open < len(votes) && votes[open] == 1 }
	case db.VotingTypeCumulative:
		return func(votes []int) bool { return open < len(votes) && votes[open] > 0 }
	default:
		return nil
	}
}

// QuestionTypeFromBallotProtocol recognises the named question type, and the canonical TypeSetup
// that goes with it, encoded by a raw ballot protocol over the given choices. A shape is
// recognised only when BallotProtocolFromType reproduces the protocol exactly, so a recognised
// type is guaranteed to re-derive the same on-chain parameters.
//
// ok is false for the shapes that have no named type — anything using vote overwrites or weighted
// cost, or a cumulative/ranked protocol a client hand-crafted away from its canonical shape —
// which stay expressible as a raw protocol.
//
// The choices are part of the question: a protocol whose MaxValue cannot carry the highest
// Choice.Value is not singlechoice over those choices, however much it looks like one.
func QuestionTypeFromBallotProtocol(
	bp *db.BallotProtocol, choices []db.Choice,
) (string, db.QuestionTypeSetup, bool) {
	if bp == nil || len(choices) == 0 {
		return "", db.QuestionTypeSetup{}, false
	}
	// At most one candidate can ever match, whatever the order: each named type maps to a protocol
	// image no other named type produces, so recognition is a function rather than a first-match
	// heuristic. No single field is unique to one type — cumulative shares maxCount 1 with
	// singlechoice at one choice, and maxValue 0 with singlechoice when its only choice is valued 0
	// — but the full 7-field image is distinct for every pair (costExponent and maxTotalCost settle
	// those degenerate cases), pinned by TestBallotShapeUnambiguous.
	candidates := []struct {
		qType string
		setup db.QuestionTypeSetup
	}{
		{db.VotingTypeSingleChoice, db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 1}},
		{db.VotingTypeMultiChoice, db.QuestionTypeSetup{
			MinChoices: minChoicesFor(bp.MaxTotalCost),
			MaxChoices: bp.MaxTotalCost,
		}},
		{db.VotingTypeRanked, db.QuestionTypeSetup{}},
		{db.VotingTypeCumulative, db.QuestionTypeSetup{
			Budget:       bp.MaxTotalCost,
			CostExponent: bp.CostExponent,
		}},
	}
	for _, candidate := range candidates {
		derived, err := BallotProtocolFromType(candidate.qType, candidate.setup, choices)
		if err == nil && *derived == *bp {
			return candidate.qType, candidate.setup, true
		}
	}
	return "", db.QuestionTypeSetup{}, false
}

// minChoicesFor returns the canonical minimum for a bounded selection, keeping the invariant
// MinChoices <= MaxChoices true for an unbounded (MaxChoices 0) multichoice.
func minChoicesFor(maxChoices uint32) uint32 {
	if maxChoices == 0 {
		return 0
	}
	return 1
}

// ValidateBallotProtocol reports why a raw ballot protocol could never yield a countable ballot.
//
// It checks unsatisfiability only, never plausibility: shapes with no named type are exactly what
// the raw protocol is for, so anything a voter could actually satisfy has to stay expressible. A
// protocol that fails here mints an election that accepts votes and tallies them to zero, which
// nothing downstream reports as an error.
func ValidateBallotProtocol(bp *db.BallotProtocol) error {
	if bp == nil {
		return nil
	}
	if bp.MaxCount == 0 {
		return fmt.Errorf("maxCount must be greater than zero")
	}
	// uniqueValues demands every field of the ballot hold a distinct value, so there have to be
	// at least as many legal values (0..maxValue) as there are fields. A dense layout with
	// maxValue 1 and more than two fields is the #619 shape: unsatisfiable by pigeonhole, for
	// any ballot a voter could send. Widened to uint64 so maxValue = MaxUint32 does not wrap.
	if bp.UniqueValues && uint64(bp.MaxValue)+1 < uint64(bp.MaxCount) {
		return fmt.Errorf(
			"uniqueValues requires maxValue+1 (%d) to be at least maxCount (%d): no ballot can "+
				"fill %d fields with distinct values drawn from 0..%d",
			uint64(bp.MaxValue)+1, bp.MaxCount, bp.MaxCount, bp.MaxValue,
		)
	}
	// maxValue 0 is the protocol's "values are aggregable amounts" marker: the per-value cap then
	// comes from maxTotalCost (raised to costExponent) or, for a weighted election, the census
	// weight (costFromWeight). With neither set the chain imposes no limit at all — a voter can put
	// any uint32 on any field — so refuse it. Scoped to MaxCount > 1: at one field, maxValue 0 is
	// also what a singlechoice over a single 0-valued choice derives, which is a legitimate ballot
	// rather than the amounts-mode evasion this catches.
	if bp.MaxValue == 0 && bp.MaxTotalCost == 0 && !bp.CostFromWeight && bp.MaxCount > 1 {
		return fmt.Errorf(
			"maxValue 0 with maxTotalCost 0 and costFromWeight false leaves every field unbounded: " +
				"a cumulative ballot needs a budget (maxTotalCost > 0) or costFromWeight",
		)
	}
	return nil
}

// EffectiveQuestionType returns the named ballot type a stored question actually encodes: the one
// inferred from its protocol when it has one — which is empty for a shape with no named type —
// and otherwise its stored Type.
//
// A stored Type is only a label. Questions written before the two halves were reconciled may
// carry one that contradicts their protocol, and it is the protocol that reaches the chain, so
// anything deciding on the ballot's identity (the plan's voting-type gate) has to ask this rather
// than read q.Type.
func EffectiveQuestionType(q *db.VotingProcessQuestion) string {
	if q.BallotProtocol == nil {
		return q.Type
	}
	qType, _, _ := QuestionTypeFromBallotProtocol(q.BallotProtocol, q.Choices)
	return qType
}

// BallotShapeInput is a question's ballot specification as a client supplied it: either half may
// be missing, and when both are present they may disagree.
type BallotShapeInput struct {
	Type      string
	TypeSetup db.QuestionTypeSetup
	Protocol  *db.BallotProtocol
	Choices   []db.Choice
}

// BallotShape is a reconciled ballot specification, safe to store: Protocol is set, and when Type
// is non-empty it re-derives Protocol exactly.
//
// Both hold for every question that has choices. For a choiceless one there is nothing to derive a
// ballot from, so ResolveBallotShape passes the input through and Protocol stays whatever the
// caller had — nil, if they supplied none.
type BallotShape struct {
	Type      string
	TypeSetup db.QuestionTypeSetup
	Protocol  *db.BallotProtocol
}

// ResolveBallotShape reconciles a question's named type with its raw ballot protocol so the two
// halves cannot disagree in storage.
//
// A supplied protocol is authoritative: Type and TypeSetup are re-derived from it, and both go
// empty when it encodes no named shape. Otherwise the protocol is derived from Type/TypeSetup.
// Either way the result round-trips — which is what lets a client edit a question through either
// half alone, and what keeps the API's answer equal to what the election will be.
//
// Supplying both halves is only allowed when they agree, or when the protocol has no named shape
// at all (vote overwrites, weighted cost, or a hand-crafted non-canonical shape). Two named shapes
// that disagree are an error rather than a silent win for the protocol: a client editing a
// question through its TypeSetup would otherwise get a 200 and none of its edit.
//
// MinChoices is the one thing that cannot be derived, having no on-chain counterpart, and is
// carried over from the input for multichoice only, clamped to the resolved MaxChoices. A
// singlechoice ballot is the single field a voter always fills, so its minimum is canonically 1;
// storing the 0 a caller gets by omitting TypeSetup would state a rule the ballot does not have.
//
// A question with no choices yet is returned untouched: it cannot be published (publish
// validation rejects it) and there is nothing to derive a protocol from.
func ResolveBallotShape(in BallotShapeInput) (BallotShape, error) {
	if len(in.Choices) == 0 {
		return BallotShape{Type: in.Type, TypeSetup: in.TypeSetup, Protocol: in.Protocol}, nil
	}
	protocol := in.Protocol
	if protocol == nil {
		derived, err := BallotProtocolFromType(in.Type, in.TypeSetup, in.Choices)
		if err != nil {
			return BallotShape{}, err
		}
		protocol = derived
	}
	qType, setup, ok := QuestionTypeFromBallotProtocol(protocol, in.Choices)
	if !ok {
		return BallotShape{Protocol: protocol}, nil
	}
	// Both halves supplied and both name a ballot: they have to name the same one. The protocol
	// wins, so without this a client that reads a draft, edits its typeSetup and PUTs the whole
	// body back would lose the edit silently — responses always carry a protocol now.
	if in.Protocol != nil && in.Type != "" {
		stated, err := BallotProtocolFromType(in.Type, in.TypeSetup, in.Choices)
		if err == nil && *stated != *in.Protocol {
			return BallotShape{}, fmt.Errorf(
				"ballotProtocol describes a %s ballot, which contradicts the stated type %q and its typeSetup; "+
					"omit ballotProtocol to define the ballot through type/typeSetup", qType, in.Type,
			)
		}
	}
	// MinChoices is the client's alone — the protocol has no counterpart to check it against. It is
	// only meaningful for multichoice: a singlechoice ballot is the one field a voter always fills,
	// so its minimum is canonically 1 whatever the caller stated.
	if qType == in.Type && qType == db.VotingTypeMultiChoice {
		setup.MinChoices = min(in.TypeSetup.MinChoices, setup.MaxChoices)
	}
	return BallotShape{Type: qType, TypeSetup: setup, Protocol: protocol}, nil
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
