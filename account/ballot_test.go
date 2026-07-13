package account

import (
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
)

func choices(n int) []db.Choice {
	out := make([]db.Choice, n)
	for i := range out {
		out[i] = db.Choice{Value: uint32(i)}
	}
	return out
}

func TestVoteTypeFromQuestion(t *testing.T) {
	c := qt.New(t)

	c.Run("singlechoice", func(c *qt.C) {
		vt, err := VoteTypeFromQuestion(&db.VotingProcessQuestion{
			Type:    db.VotingTypeSingleChoice,
			Choices: choices(3),
		})
		c.Assert(err, qt.IsNil)
		c.Assert(vt.MaxCount, qt.Equals, uint32(1))
		c.Assert(vt.MaxValue, qt.Equals, uint32(2)) // len-1
		c.Assert(vt.UniqueChoices, qt.IsFalse)
		c.Assert(vt.MaxTotalCost, qt.Equals, uint32(0))
	})

	c.Run("multichoice", func(c *qt.C) {
		vt, err := VoteTypeFromQuestion(&db.VotingProcessQuestion{
			Type:      db.VotingTypeMultiChoice,
			Choices:   choices(4),
			TypeSetup: db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 2, UniqueChoices: true},
		})
		c.Assert(err, qt.IsNil)
		c.Assert(vt.MaxCount, qt.Equals, uint32(4)) // one field per choice
		c.Assert(vt.MaxValue, qt.Equals, uint32(1))
		c.Assert(vt.CostExponent, qt.Equals, uint32(1))
		c.Assert(vt.MaxTotalCost, qt.Equals, uint32(2)) // maxChoices
		c.Assert(vt.UniqueChoices, qt.IsTrue)
	})

	c.Run("ballotProtocol overrides type/typeSetup", func(c *qt.C) {
		vt, err := VoteTypeFromQuestion(&db.VotingProcessQuestion{
			Type:    db.VotingTypeSingleChoice, // ignored when ballotProtocol is set
			Choices: choices(5),
			BallotProtocol: &db.BallotProtocol{
				MaxCount: 5, MaxValue: 4, CostExponent: 2, MaxTotalCost: 12,
				UniqueValues: true, CostFromWeight: true, MaxVoteOverwrites: 1,
			},
		})
		c.Assert(err, qt.IsNil)
		c.Assert(vt.MaxCount, qt.Equals, uint32(5))
		c.Assert(vt.MaxValue, qt.Equals, uint32(4))
		c.Assert(vt.CostExponent, qt.Equals, uint32(2))
		c.Assert(vt.MaxTotalCost, qt.Equals, uint32(12))
		c.Assert(vt.UniqueChoices, qt.IsTrue) // uniqueValues -> uniqueChoices
		c.Assert(vt.CostFromWeight, qt.IsTrue)
		c.Assert(vt.MaxVoteOverwrites, qt.Equals, uint32(1))
	})

	c.Run("unsupported type", func(c *qt.C) {
		_, err := VoteTypeFromQuestion(&db.VotingProcessQuestion{Type: "quadratic", Choices: choices(2)})
		c.Assert(err, qt.Not(qt.IsNil))
	})

	c.Run("no choices", func(c *qt.C) {
		_, err := VoteTypeFromQuestion(&db.VotingProcessQuestion{Type: db.VotingTypeSingleChoice})
		c.Assert(err, qt.Not(qt.IsNil))
	})
}

func TestElectionTypeFromQuestion(t *testing.T) {
	c := qt.New(t)
	et := ElectionTypeFromQuestion(&db.VotingProcessQuestion{SecretUntilTheEnd: true})
	c.Assert(et.Autostart, qt.IsTrue)
	c.Assert(et.Interruptible, qt.IsTrue)
	c.Assert(et.DynamicCensus, qt.IsFalse)
	c.Assert(et.Anonymous, qt.IsFalse)
	c.Assert(et.SecretUntilTheEnd, qt.IsTrue)
}

func TestComputeMaxCensusSize(t *testing.T) {
	c := qt.New(t)
	c.Assert(ComputeMaxCensusSize([]string{"a", "b"}, 10), qt.Equals, uint64(2)) // subset wins
	c.Assert(ComputeMaxCensusSize(nil, 10), qt.Equals, uint64(10))               // parent size
	c.Assert(ComputeMaxCensusSize(nil, 0), qt.Equals, uint64(0))                 // unknown
}

// TestVoteTypeSingleChoiceNonContiguousValues verifies MaxValue is derived from the actual
// (possibly non-contiguous) Choice.Value set, not len(choices)-1, so a client using values like
// {0,2,5} gets an on-chain MaxValue that admits its highest value (P2-3).
func TestVoteTypeSingleChoiceNonContiguousValues(t *testing.T) {
	c := qt.New(t)
	q := &db.VotingProcessQuestion{
		Type: db.VotingTypeSingleChoice,
		Choices: []db.Choice{
			{Value: 0}, {Value: 2}, {Value: 5},
		},
	}
	vt, err := VoteTypeFromQuestion(q)
	c.Assert(err, qt.IsNil)
	c.Assert(vt.MaxCount, qt.Equals, uint32(1))
	c.Assert(vt.MaxValue, qt.Equals, uint32(5))
}
