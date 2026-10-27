package account

import (
	"fmt"
	"math/big"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
)

// classifierChoices returns n choices valued by position (0..n-1).
func classifierChoices(n int) []db.Choice {
	choices := make([]db.Choice, n)
	for i := range choices {
		choices[i] = db.Choice{Title: db.MultiLangString{"default": fmt.Sprintf("c%d", i)}, Value: uint32(i)}
	}
	return choices
}

// TestBallotClassifier pins blank/invalid/valid and the selected options per ballot layout, and that
// "invalid" reproduces the chain tally's rejections (dvote results.AddVote).
func TestBallotClassifier(t *testing.T) {
	// singlechoice over sparse values 0, 2, 5: maxValue 5, so the per-option arrays have 6 slots
	single := &db.VotingProcessQuestion{
		Type: db.VotingTypeSingleChoice,
		Choices: []db.Choice{
			{Title: db.MultiLangString{"default": "a"}, Value: 0},
			{Title: db.MultiLangString{"default": "b"}, Value: 2},
			{Title: db.MultiLangString{"default": "c"}, Value: 5},
		},
	}
	multi := &db.VotingProcessQuestion{
		Type: db.VotingTypeMultiChoice, Choices: classifierChoices(3),
		TypeSetup: db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 2},
	}
	linear := &db.VotingProcessQuestion{
		Type: db.VotingTypeCumulative, Choices: classifierChoices(3),
		TypeSetup: db.QuestionTypeSetup{Budget: 10, CostExponent: 1},
	}
	quadratic := &db.VotingProcessQuestion{
		Type: db.VotingTypeCumulative, Choices: classifierChoices(3),
		TypeSetup: db.QuestionTypeSetup{Budget: 10, CostExponent: 2},
	}
	ranked := &db.VotingProcessQuestion{Type: db.VotingTypeRanked, Choices: classifierChoices(3)}
	// a raw protocol no named type produces: two fields of 0..3
	raw := &db.VotingProcessQuestion{
		Choices: classifierChoices(2), BallotProtocol: &db.BallotProtocol{MaxCount: 2, MaxValue: 3},
	}
	// a raw protocol whose cost is bounded by the voter's weight
	rawWeighted := &db.VotingProcessQuestion{
		Choices: classifierChoices(2),
		BallotProtocol: &db.BallotProtocol{
			MaxCount: 2, MaxValue: 3, CostExponent: 1, CostFromWeight: true,
		},
	}

	widths := []struct {
		name  string
		q     *db.VotingProcessQuestion
		width int
	}{
		{"singlechoice", single, 6},
		{"multichoice", multi, 3},
		{"cumulative", linear, 3},
		{"ranked", ranked, 0},
		{"raw", raw, 0},
	}
	for _, w := range widths {
		c := qt.New(t)
		classifier, err := NewBallotClassifier(w.q)
		c.Assert(err, qt.IsNil)
		c.Assert(classifier.Width(), qt.Equals, w.width, qt.Commentf("%s", w.name))
	}

	one := big.NewInt(1)
	tests := []struct {
		name     string
		q        *db.VotingProcessQuestion
		votes    []int
		weight   *big.Int
		kind     BallotKind
		selected []int
	}{
		{"single empty is blank", single, []int{}, one, BallotBlank, nil},
		{"single nil is blank", single, nil, one, BallotBlank, nil},
		{"single [0] selects value 0", single, []int{0}, one, BallotValid, []int{0}},
		{"single sparse value 2", single, []int{2}, one, BallotValid, []int{2}},
		{"single sparse value 5", single, []int{5}, one, BallotValid, []int{5}},
		{"single unused value 3 is still tallied", single, []int{3}, one, BallotValid, []int{3}},
		{"single over maxValue", single, []int{7}, one, BallotInvalid, nil},
		{"single negative value", single, []int{-1}, one, BallotInvalid, nil},
		{"single too many fields", single, []int{0, 2}, one, BallotInvalid, nil},

		{"multi all zeros is blank", multi, []int{0, 0, 0}, one, BallotBlank, nil},
		{"multi empty is blank", multi, []int{}, one, BallotBlank, nil},
		{"multi selects fields holding 1", multi, []int{1, 0, 1}, one, BallotValid, []int{0, 2}},
		{"multi short ballot", multi, []int{0, 1}, one, BallotValid, []int{1}},
		{"multi over maxTotalCost", multi, []int{1, 1, 1}, one, BallotInvalid, nil},
		{"multi over maxValue", multi, []int{2, 0, 0}, one, BallotInvalid, nil},
		{"multi too many fields", multi, []int{1, 0, 0, 0}, one, BallotInvalid, nil},
		{"multi all zeros with too many fields is invalid", multi, []int{0, 0, 0, 0}, one, BallotInvalid, nil},

		{"linear all zeros is blank", linear, []int{0, 0, 0}, one, BallotBlank, nil},
		{"linear within budget", linear, []int{3, 0, 7}, one, BallotValid, []int{0, 2}},
		{"linear over budget", linear, []int{5, 6, 0}, one, BallotInvalid, nil},
		{"linear negative value", linear, []int{-1, 0, 0}, one, BallotInvalid, nil},
		{"quadratic at budget", quadratic, []int{3, 1, 0}, one, BallotValid, []int{0, 1}},
		{"quadratic over budget", quadratic, []int{3, 2, 0}, one, BallotInvalid, nil},

		{"ranked empty is blank", ranked, []int{}, one, BallotBlank, nil},
		{"ranked all zeros is rejected by uniqueValues", ranked, []int{0, 0, 0}, one, BallotInvalid, nil},
		{"ranked has no per-option selection", ranked, []int{2, 1, 0}, one, BallotValid, nil},
		{"ranked repeated ranks", ranked, []int{1, 1, 0}, one, BallotInvalid, nil},
		{"ranked rank over maxValue", ranked, []int{3, 1, 0}, one, BallotInvalid, nil},

		{"raw empty is blank", raw, []int{}, one, BallotBlank, nil},
		{"raw all zeros is blank", raw, []int{0, 0}, one, BallotBlank, nil},
		{"raw has no per-option selection", raw, []int{1, 3}, one, BallotValid, nil},
		{"raw over maxValue", raw, []int{4, 0}, one, BallotInvalid, nil},

		{"costFromWeight within weight", rawWeighted, []int{3, 3}, big.NewInt(6), BallotValid, nil},
		{"costFromWeight over weight", rawWeighted, []int{3, 3}, big.NewInt(5), BallotInvalid, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := qt.New(t)
			classifier, err := NewBallotClassifier(tc.q)
			c.Assert(err, qt.IsNil)
			kind, selected := classifier.Classify(tc.votes, tc.weight)
			c.Assert(kind, qt.Equals, tc.kind)
			c.Assert(selected, qt.DeepEquals, tc.selected)
		})
	}
}

// TestNewBallotClassifierRejectsUnknownShape: a question with neither a protocol nor a named type
// cannot be classified.
func TestNewBallotClassifierRejectsUnknownShape(t *testing.T) {
	c := qt.New(t)
	_, err := NewBallotClassifier(&db.VotingProcessQuestion{Choices: classifierChoices(2)})
	c.Assert(err, qt.Not(qt.IsNil))
}
