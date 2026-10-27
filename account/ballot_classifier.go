package account

import (
	"math/big"

	"github.com/vocdoni/saas-backend/db"
)

// BallotKind is how a decoded ballot counts in a question's db.BallotCounts.
type BallotKind int

const (
	// BallotInvalid is a ballot the chain's tally skips: its values break the ballot protocol.
	BallotInvalid BallotKind = iota
	// BallotBlank is a ballot that selects nothing (see BallotClassifier.Classify).
	BallotBlank
	// BallotValid is a tallied ballot that selects at least one value.
	BallotValid
)

// BallotClassifier sorts a question's decoded ballots into blank, invalid and valid, and reports which
// options a valid ballot selected. It is pure: built once per question, then safe for concurrent use.
type BallotClassifier struct {
	layout string      // the named ballot layout (EffectiveQuestionType); "" for a raw protocol
	rules  db.VoteType // the on-chain vote options the election was minted with
	width  int         // length of the per-option arrays; 0 = none
}

// NewBallotClassifier builds the classifier of a published question. The validity rules are the vote
// options the election was minted with (VoteTypeFromQuestion: the stored protocol, or the one derived
// from the named type), and the layout that gives "blank" and "selected" their meaning is the named type
// the protocol encodes (EffectiveQuestionType), so a singlechoice that allows overwrites is still a
// singlechoice.
func NewBallotClassifier(q *db.VotingProcessQuestion) (BallotClassifier, error) {
	rules, err := VoteTypeFromQuestion(q)
	if err != nil {
		return BallotClassifier{}, err
	}
	c := BallotClassifier{layout: EffectiveQuestionType(q), rules: rules}
	switch c.layout {
	case db.VotingTypeSingleChoice:
		c.width = int(MaxChoiceValue(q.Choices)) + 1
	case db.VotingTypeMultiChoice, db.VotingTypeCumulative:
		c.width = len(q.Choices)
	default:
		// ranked ranks every choice on every ballot and a raw protocol has no defined layout:
		// neither has per-option counts
	}
	return c, nil
}

// Width is the length of the per-option count arrays; 0 when the question has none (ranked, raw).
func (c BallotClassifier) Width() int {
	return c.width
}

// Classify sorts one decoded ballot (its votes values and the voter's weight). Validity is checked first:
// the ballot is invalid when the chain's tally would reject it (the checks of dvote's results.AddVote), so
// a ballot the tally skips is never blank. A tallied singlechoice ballot is blank only as [] (a [0] selects
// the choice valued 0); any other layout is blank as [] or all zeros. Otherwise it is valid. selected holds
// the option indexes a valid ballot chose, within Width: singlechoice its value, multichoice the fields holding 1, cumulative
// the fields above 0; nil for ranked, raw, blank and invalid ballots.
func (c BallotClassifier) Classify(votes []int, weight *big.Int) (kind BallotKind, selected []int) {
	if !c.tallyAccepts(votes, weight) {
		return BallotInvalid, nil
	}
	if c.isBlank(votes) {
		return BallotBlank, nil
	}
	return BallotValid, c.selected(votes)
}

func (c BallotClassifier) isBlank(votes []int) bool {
	if c.layout == db.VotingTypeSingleChoice {
		return len(votes) == 0
	}
	for _, v := range votes {
		if v != 0 {
			return false
		}
	}
	return true
}

// tallyAccepts mirrors the validity checks of dvote vochain/results/results.go (*Results).AddVote, in
// the same order and with the same integer conversions (a negative value wraps when cast to uint32 or
// uint64, exactly as on chain). Keep it in step with that function when bumping go.vocdoni.io/dvote.
func (c BallotClassifier) tallyAccepts(votes []int, weight *big.Int) bool {
	if len(votes) > int(c.rules.MaxCount) {
		return false
	}
	if c.rules.UniqueChoices {
		seen := make(map[int]bool, len(votes))
		for _, v := range votes {
			if seen[v] {
				return false
			}
			seen[v] = true
		}
	}
	if c.rules.MaxValue > 0 {
		for _, v := range votes {
			if uint32(v) > c.rules.MaxValue {
				return false
			}
		}
	}
	if c.rules.MaxTotalCost > 0 || c.rules.CostFromWeight {
		maxCost := new(big.Int).SetUint64(uint64(c.rules.MaxTotalCost))
		if c.rules.CostFromWeight {
			maxCost = big.NewInt(1) // the chain defaults a missing weight to 1
			if weight != nil {
				maxCost = weight
			}
		}
		exponent := new(big.Int).SetUint64(uint64(c.rules.CostExponent))
		cost := new(big.Int)
		for _, v := range votes {
			cost.Add(cost, new(big.Int).Exp(new(big.Int).SetUint64(uint64(v)), exponent, nil))
			if cost.Cmp(maxCost) > 0 {
				return false
			}
		}
	}
	return true
}

// selected returns the option indexes a valid ballot chose. Indexes outside Width cannot come from a
// valid ballot of a named layout, and are dropped defensively rather than trusted.
func (c BallotClassifier) selected(votes []int) []int {
	var out []int
	switch c.layout {
	case db.VotingTypeSingleChoice:
		if v := votes[0]; v >= 0 && v < c.width {
			out = append(out, v)
		}
	case db.VotingTypeMultiChoice:
		for i, v := range votes {
			if v == 1 && i < c.width {
				out = append(out, i)
			}
		}
	case db.VotingTypeCumulative:
		for i, v := range votes {
			if v > 0 && i < c.width {
				out = append(out, i)
			}
		}
	default:
	}
	return out
}
