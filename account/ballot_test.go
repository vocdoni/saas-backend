package account

import (
	"math"
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
		// A legacy question stored before uniqueChoices was rejected keeps the flag in its
		// TypeSetup, and publishing it must ignore it: one 0/1 field per choice plus
		// uniqueValues is unsatisfiable, so the election would tally every vote to zero (#619).
		// Everything else about the ballot is unchanged, which the assertions above pin.
		c.Assert(vt.UniqueChoices, qt.IsFalse)
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

// valuedChoices builds choices with the given explicit values, for the cases where the values are
// not the contiguous 0..n-1 that choices() produces.
func valuedChoices(values ...uint32) []db.Choice {
	out := make([]db.Choice, len(values))
	for i, v := range values {
		out[i] = db.Choice{Value: v}
	}
	return out
}

// TestBallotProtocolFromType pins the one mapping table the whole reconciliation rests on: every
// other direction is defined as equality against this function's output.
func TestBallotProtocolFromType(t *testing.T) {
	c := qt.New(t)

	c.Run("singlechoice ignores typeSetup entirely", func(c *qt.C) {
		want := db.BallotProtocol{MaxCount: 1, MaxValue: 2}
		for _, setup := range []db.QuestionTypeSetup{
			{},
			{MinChoices: 1, MaxChoices: 1},
			{MinChoices: 3, MaxChoices: 7, UniqueChoices: true},
		} {
			bp, err := BallotProtocolFromType(db.VotingTypeSingleChoice, setup, choices(3))
			c.Assert(err, qt.IsNil)
			c.Assert(*bp, qt.Equals, want, qt.Commentf("setup %+v", setup))
		}
	})

	c.Run("singlechoice covers the highest choice value", func(c *qt.C) {
		bp, err := BallotProtocolFromType(db.VotingTypeSingleChoice, db.QuestionTypeSetup{},
			valuedChoices(0, 2, 5))
		c.Assert(err, qt.IsNil)
		c.Assert(*bp, qt.Equals, db.BallotProtocol{MaxCount: 1, MaxValue: 5})
	})

	c.Run("multichoice is the dense layout, never unique", func(c *qt.C) {
		// the uniqueChoices in the setup is the #619 shape and must not reach the protocol
		bp, err := BallotProtocolFromType(db.VotingTypeMultiChoice,
			db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 2, UniqueChoices: true}, choices(4))
		c.Assert(err, qt.IsNil)
		c.Assert(*bp, qt.Equals, db.BallotProtocol{
			MaxCount: 4, MaxValue: 1, CostExponent: 1, MaxTotalCost: 2,
		})
	})

	c.Run("ranked derives a unique-values permutation", func(c *qt.C) {
		bp, err := BallotProtocolFromType(db.VotingTypeRanked, db.QuestionTypeSetup{}, choices(3))
		c.Assert(err, qt.IsNil)
		want := db.BallotProtocol{MaxCount: 3, MaxValue: 2, UniqueValues: true}
		c.Assert(*bp, qt.Equals, want)
	})

	c.Run("cumulative reads budget and costExponent, maxValue 0", func(c *qt.C) {
		// linear budget (costExponent 1)
		bp, err := BallotProtocolFromType(db.VotingTypeCumulative,
			db.QuestionTypeSetup{Budget: 10, CostExponent: 1}, choices(3))
		c.Assert(err, qt.IsNil)
		c.Assert(*bp, qt.Equals, db.BallotProtocol{MaxCount: 3, CostExponent: 1, MaxTotalCost: 10})
		// quadratic (costExponent 2) — same shape, different exponent
		bp, err = BallotProtocolFromType(db.VotingTypeCumulative,
			db.QuestionTypeSetup{Budget: 12, CostExponent: 2}, choices(3))
		c.Assert(err, qt.IsNil)
		c.Assert(*bp, qt.Equals, db.BallotProtocol{MaxCount: 3, CostExponent: 2, MaxTotalCost: 12})
	})

	c.Run("errors", func(c *qt.C) {
		_, err := BallotProtocolFromType(db.VotingTypeSingleChoice, db.QuestionTypeSetup{}, nil)
		c.Assert(err, qt.Not(qt.IsNil))
		_, err = BallotProtocolFromType("", db.QuestionTypeSetup{}, choices(2))
		c.Assert(err, qt.Not(qt.IsNil))
		_, err = BallotProtocolFromType("quadratic", db.QuestionTypeSetup{}, choices(2))
		c.Assert(err, qt.Not(qt.IsNil)) // quadratic is not a type name; it is cumulative exp 2
		// ranked needs at least two choices to mean anything
		_, err = BallotProtocolFromType(db.VotingTypeRanked, db.QuestionTypeSetup{}, choices(1))
		c.Assert(err, qt.Not(qt.IsNil))
		// cumulative needs a budget and a 1-or-2 exponent
		_, err = BallotProtocolFromType(db.VotingTypeCumulative,
			db.QuestionTypeSetup{Budget: 0, CostExponent: 2}, choices(3))
		c.Assert(err, qt.Not(qt.IsNil))
		_, err = BallotProtocolFromType(db.VotingTypeCumulative,
			db.QuestionTypeSetup{Budget: 10, CostExponent: 3}, choices(3))
		c.Assert(err, qt.Not(qt.IsNil))
	})
}

// TestQuestionTypeFromBallotProtocol covers the reverse direction: which named type a raw protocol
// encodes, and — more importantly — which ones it does not, since a shape wrongly given a name
// would be a question describing a ballot it does not have.
func TestQuestionTypeFromBallotProtocol(t *testing.T) {
	c := qt.New(t)

	c.Run("recognises the named shapes", func(c *qt.C) {
		qType, setup, ok := QuestionTypeFromBallotProtocol(
			&db.BallotProtocol{MaxCount: 1, MaxValue: 2}, choices(3))
		c.Assert(ok, qt.IsTrue)
		c.Assert(qType, qt.Equals, db.VotingTypeSingleChoice)
		c.Assert(setup, qt.Equals, db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 1})

		qType, setup, ok = QuestionTypeFromBallotProtocol(
			&db.BallotProtocol{MaxCount: 4, MaxValue: 1, CostExponent: 1, MaxTotalCost: 2}, choices(4))
		c.Assert(ok, qt.IsTrue)
		c.Assert(qType, qt.Equals, db.VotingTypeMultiChoice)
		c.Assert(setup, qt.Equals, db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 2})

		// ranked: the permutation ballot saas-integrator-demo sends, now named
		qType, setup, ok = QuestionTypeFromBallotProtocol(
			&db.BallotProtocol{MaxCount: 3, MaxValue: 2, UniqueValues: true}, choices(3))
		c.Assert(ok, qt.IsTrue)
		c.Assert(qType, qt.Equals, db.VotingTypeRanked)
		c.Assert(setup, qt.Equals, db.QuestionTypeSetup{})

		// cumulative: maxValue 0 is the "amounts" marker; budget and exponent come back as setup
		qType, setup, ok = QuestionTypeFromBallotProtocol(
			&db.BallotProtocol{MaxCount: 3, MaxValue: 0, CostExponent: 2, MaxTotalCost: 12}, choices(3))
		c.Assert(ok, qt.IsTrue)
		c.Assert(qType, qt.Equals, db.VotingTypeCumulative)
		c.Assert(setup, qt.Equals, db.QuestionTypeSetup{Budget: 12, CostExponent: 2})
	})

	c.Run("an unbounded approval ballot is still multichoice", func(c *qt.C) {
		qType, setup, ok := QuestionTypeFromBallotProtocol(
			&db.BallotProtocol{MaxCount: 3, MaxValue: 1, CostExponent: 1}, choices(3))
		c.Assert(ok, qt.IsTrue)
		c.Assert(qType, qt.Equals, db.VotingTypeMultiChoice)
		c.Assert(setup, qt.Equals, db.QuestionTypeSetup{MinChoices: 0, MaxChoices: 0})
	})

	c.Run("shapes with no named type", func(c *qt.C) {
		for name, bp := range map[string]*db.BallotProtocol{
			"vote overwrites":   {MaxCount: 1, MaxValue: 2, MaxVoteOverwrites: 1},
			"weighted cost":     {MaxCount: 1, MaxValue: 2, CostFromWeight: true},
			"quadratic":         {MaxCount: 3, MaxValue: 4, CostExponent: 2, MaxTotalCost: 12},
			"padded max count":  {MaxCount: 9, MaxValue: 1, CostExponent: 1, MaxTotalCost: 2},
			"cost on a single":  {MaxCount: 1, MaxValue: 2, CostExponent: 1},
			"single with total": {MaxCount: 1, MaxValue: 2, MaxTotalCost: 1},
		} {
			_, _, ok := QuestionTypeFromBallotProtocol(bp, choices(3))
			c.Assert(ok, qt.IsFalse, qt.Commentf("%s was given a name it does not have", name))
		}
	})

	c.Run("a protocol that cannot carry the choice values is not singlechoice", func(c *qt.C) {
		// maxValue 2 admits values 0..2, but the question offers a choice valued 5
		_, _, ok := QuestionTypeFromBallotProtocol(
			&db.BallotProtocol{MaxCount: 1, MaxValue: 2}, valuedChoices(0, 2, 5))
		c.Assert(ok, qt.IsFalse)
	})

	c.Run("nothing to recognise", func(c *qt.C) {
		_, _, ok := QuestionTypeFromBallotProtocol(nil, choices(3))
		c.Assert(ok, qt.IsFalse)
		_, _, ok = QuestionTypeFromBallotProtocol(&db.BallotProtocol{MaxCount: 1}, nil)
		c.Assert(ok, qt.IsFalse)
	})
}

// TestBallotShapeUnambiguous guards the property the reverse direction rests on: the named types
// never share a protocol image, so recognition is a function rather than a first-match heuristic.
// If a future field change breaks this, recognition starts depending on candidate order and this
// fails before anything subtler does.
func TestBallotShapeUnambiguous(t *testing.T) {
	c := qt.New(t)
	for n := 1; n <= 8; n++ {
		for _, cs := range [][]db.Choice{choices(n), valuedChoices(0, 2, 5)[:min(n, 3)]} {
			type named struct {
				qType string
				bp    db.BallotProtocol
			}
			var protos []named
			add := func(qType string, setup db.QuestionTypeSetup) {
				bp, err := BallotProtocolFromType(qType, setup, cs)
				c.Assert(err, qt.IsNil)
				protos = append(protos, named{qType, *bp})
			}
			// every named type well-defined over cs. A single multichoice representative suffices:
			// maxChoices only changes its MaxTotalCost, and multichoice is the only type with
			// maxValue 1, so no value of it can collide with another type's image.
			add(db.VotingTypeSingleChoice, db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 1})
			add(db.VotingTypeMultiChoice, db.QuestionTypeSetup{MaxChoices: uint32(n)})
			if n >= 2 { // ranked needs at least two choices
				add(db.VotingTypeRanked, db.QuestionTypeSetup{})
			}
			add(db.VotingTypeCumulative, db.QuestionTypeSetup{Budget: 10, CostExponent: 2})
			for i, a := range protos {
				for _, b := range protos[i+1:] {
					c.Assert(a.bp, qt.Not(qt.Equals), b.bp,
						qt.Commentf("n=%d %s == %s", n, a.qType, b.qType))
				}
			}
		}
	}
}

// TestResolveBallotShapeRoundTrip is the invariant the whole change exists to establish: a
// question authored through its named type comes back describing the same type, and carries the
// protocol that type derives. Nothing a client states about the ballot is lost or altered.
func TestResolveBallotShapeRoundTrip(t *testing.T) {
	c := qt.New(t)
	for _, in := range []BallotShapeInput{
		{Type: db.VotingTypeSingleChoice, TypeSetup: db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 1}, Choices: choices(1)},
		{
			Type: db.VotingTypeSingleChoice, TypeSetup: db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 1},
			Choices: valuedChoices(0, 2, 5),
		},
		{Type: db.VotingTypeMultiChoice, TypeSetup: db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 1}, Choices: choices(1)},
		{Type: db.VotingTypeMultiChoice, TypeSetup: db.QuestionTypeSetup{MinChoices: 0, MaxChoices: 2}, Choices: choices(4)},
		{Type: db.VotingTypeMultiChoice, TypeSetup: db.QuestionTypeSetup{MinChoices: 4, MaxChoices: 4}, Choices: choices(4)},
		{Type: db.VotingTypeRanked, Choices: choices(3)},
		{Type: db.VotingTypeCumulative, TypeSetup: db.QuestionTypeSetup{Budget: 10, CostExponent: 2}, Choices: choices(3)},
	} {
		shape, err := ResolveBallotShape(in)
		c.Assert(err, qt.IsNil)
		c.Assert(shape.Type, qt.Equals, in.Type, qt.Commentf("%+v", in))
		c.Assert(shape.TypeSetup, qt.Equals, in.TypeSetup, qt.Commentf("%+v", in))
		want, err := BallotProtocolFromType(in.Type, in.TypeSetup, in.Choices)
		c.Assert(err, qt.IsNil)
		c.Assert(*shape.Protocol, qt.Equals, *want, qt.Commentf("%+v", in))
	}

	// singlechoice is the exception: typeSetup is optional for it, and a ballot of one field the
	// voter always fills has no minimum a client can state, so the stored setup is canonical
	// whatever came in — never the {minChoices: 0} an omitted typeSetup would otherwise leave.
	for _, setup := range []db.QuestionTypeSetup{{}, {MinChoices: 0, MaxChoices: 1}, {MinChoices: 1, MaxChoices: 1}} {
		shape, err := ResolveBallotShape(BallotShapeInput{
			Type: db.VotingTypeSingleChoice, TypeSetup: setup, Choices: choices(4),
		})
		c.Assert(err, qt.IsNil)
		c.Assert(shape.Type, qt.Equals, db.VotingTypeSingleChoice)
		c.Assert(shape.TypeSetup, qt.Equals, db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 1}, qt.Commentf("%+v", setup))
	}
}

// TestResolveBallotShapeProtocolWins covers the half of the reconciliation a client can be
// surprised by: a supplied protocol is what the election will be, so the type is derived from it
// and emptied when the protocol has no name. Where both halves name a ballot and disagree there is
// nothing to prefer, so the request is refused instead of the edit being dropped.
func TestResolveBallotShapeProtocolWins(t *testing.T) {
	c := qt.New(t)

	c.Run("an unnamed protocol empties the type", func(c *qt.C) {
		// a shape with no named type at all — vote overwrites — labelled singlechoice: the
		// protocol is kept, the type it cannot be named is dropped
		unnamed := &db.BallotProtocol{MaxCount: 1, MaxValue: 2, MaxVoteOverwrites: 1}
		shape, err := ResolveBallotShape(BallotShapeInput{
			Type:      db.VotingTypeSingleChoice,
			TypeSetup: db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 1},
			Protocol:  unnamed,
			Choices:   choices(3),
		})
		c.Assert(err, qt.IsNil)
		c.Assert(shape.Type, qt.Equals, "")
		c.Assert(shape.TypeSetup, qt.Equals, db.QuestionTypeSetup{})
		c.Assert(*shape.Protocol, qt.Equals, *unnamed)
	})

	c.Run("a ranked protocol is now named, not emptied", func(c *qt.C) {
		// the permutation ballot saas-integrator-demo sends is recognised as ranked; with no
		// stated type it resolves to ranked rather than to the empty type it used to
		ranked := &db.BallotProtocol{MaxCount: 3, MaxValue: 2, UniqueValues: true}
		shape, err := ResolveBallotShape(BallotShapeInput{
			Protocol: ranked,
			Choices:  choices(3),
		})
		c.Assert(err, qt.IsNil)
		c.Assert(shape.Type, qt.Equals, db.VotingTypeRanked)
		c.Assert(shape.TypeSetup, qt.Equals, db.QuestionTypeSetup{})
		c.Assert(*shape.Protocol, qt.Equals, *ranked)
	})

	c.Run("a ranked protocol mislabelled singlechoice is refused", func(c *qt.C) {
		// the demo's old request shape (type singlechoice + ranked protocol) is now a
		// disagreement, since ranked is a named type: send it as type ranked instead
		_, err := ResolveBallotShape(BallotShapeInput{
			Type:      db.VotingTypeSingleChoice,
			TypeSetup: db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 1},
			Protocol:  &db.BallotProtocol{MaxCount: 3, MaxValue: 2, UniqueValues: true},
			Choices:   choices(3),
		})
		c.Assert(err, qt.Not(qt.IsNil))
	})

	c.Run("two named halves that disagree are refused", func(c *qt.C) {
		_, err := ResolveBallotShape(BallotShapeInput{
			Type:      db.VotingTypeMultiChoice,
			TypeSetup: db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 2},
			Protocol:  &db.BallotProtocol{MaxCount: 1, MaxValue: 2},
			Choices:   choices(3),
		})
		c.Assert(err, qt.Not(qt.IsNil))
	})

	c.Run("the stale-echo edit is refused rather than dropped", func(c *qt.C) {
		// a client reads a draft, raises maxChoices and PUTs the whole body back: the protocol it
		// echoes still encodes the old maximum, and used to win in silence
		_, err := ResolveBallotShape(BallotShapeInput{
			Type:      db.VotingTypeMultiChoice,
			TypeSetup: db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 3},
			Protocol:  &db.BallotProtocol{MaxCount: 4, MaxValue: 1, CostExponent: 1, MaxTotalCost: 2},
			Choices:   choices(4),
		})
		c.Assert(err, qt.Not(qt.IsNil))
	})

	c.Run("an honest echo of both halves is accepted", func(c *qt.C) {
		shape, err := ResolveBallotShape(BallotShapeInput{
			Type:      db.VotingTypeMultiChoice,
			TypeSetup: db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 2},
			Protocol:  &db.BallotProtocol{MaxCount: 4, MaxValue: 1, CostExponent: 1, MaxTotalCost: 2},
			Choices:   choices(4),
		})
		c.Assert(err, qt.IsNil)
		c.Assert(shape.Type, qt.Equals, db.VotingTypeMultiChoice)
		c.Assert(shape.TypeSetup, qt.Equals, db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 2})
	})

	c.Run("the type is inferred from a protocol alone", func(c *qt.C) {
		shape, err := ResolveBallotShape(BallotShapeInput{
			Protocol: &db.BallotProtocol{MaxCount: 4, MaxValue: 1, CostExponent: 1, MaxTotalCost: 2},
			Choices:  choices(4),
		})
		c.Assert(err, qt.IsNil)
		c.Assert(shape.Type, qt.Equals, db.VotingTypeMultiChoice)
		c.Assert(shape.TypeSetup, qt.Equals, db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 2})
	})

	c.Run("minChoices is clamped to the resolved maximum", func(c *qt.C) {
		shape, err := ResolveBallotShape(BallotShapeInput{
			Type:      db.VotingTypeMultiChoice,
			TypeSetup: db.QuestionTypeSetup{MinChoices: 4, MaxChoices: 2},
			Choices:   choices(4),
		})
		c.Assert(err, qt.IsNil)
		c.Assert(shape.TypeSetup, qt.Equals, db.QuestionTypeSetup{MinChoices: 2, MaxChoices: 2})
	})

	c.Run("a question with no choices is left alone", func(c *qt.C) {
		shape, err := ResolveBallotShape(BallotShapeInput{Type: db.VotingTypeSingleChoice})
		c.Assert(err, qt.IsNil)
		c.Assert(shape.Type, qt.Equals, db.VotingTypeSingleChoice)
		c.Assert(shape.Protocol, qt.IsNil)
	})

	c.Run("an unnamed type with no protocol is an error", func(c *qt.C) {
		_, err := ResolveBallotShape(BallotShapeInput{Type: "quadratic", Choices: choices(2)})
		c.Assert(err, qt.Not(qt.IsNil))
	})
}

// TestValidateBallotProtocol pins that the guard rejects exactly the ballots no voter could
// satisfy, and nothing else — a raw protocol exists to express shapes that have no name, so
// over-validating it would close the door this API deliberately leaves open.
func TestValidateBallotProtocol(t *testing.T) {
	c := qt.New(t)

	c.Run("unsatisfiable", func(c *qt.C) {
		for name, bp := range map[string]*db.BallotProtocol{
			"empty":                      {},
			"issue 619 in raw form":      {MaxCount: 4, MaxValue: 1, UniqueValues: true},
			"two fields, one value":      {MaxCount: 2, MaxValue: 0, UniqueValues: true},
			"one short of a permutation": {MaxCount: 4, MaxValue: 2, UniqueValues: true},
			// one value short of a permutation, at the top of the uint32 range
			"one short at the uint32 ceiling": {MaxCount: math.MaxUint32, MaxValue: math.MaxUint32 - 2, UniqueValues: true},
		} {
			c.Assert(ValidateBallotProtocol(bp), qt.Not(qt.IsNil), qt.Commentf("%s", name))
		}
	})

	c.Run("satisfiable", func(c *qt.C) {
		for name, bp := range map[string]*db.BallotProtocol{
			"ranked (the demo)":     {MaxCount: 3, MaxValue: 2, UniqueValues: true},
			"unique over one field": {MaxCount: 1, MaxValue: 0, UniqueValues: true},
			"singlechoice":          {MaxCount: 1, MaxValue: 2},
			"multichoice":           {MaxCount: 4, MaxValue: 1, CostExponent: 1, MaxTotalCost: 2},
			"cumulative/quadratic":  {MaxCount: 3, MaxValue: 0, CostExponent: 2, MaxTotalCost: 12},
			"quadratic":             {MaxCount: 3, MaxValue: 4, CostExponent: 2, MaxTotalCost: 12},
			// exactly enough values for the fields, where maxValue+1 overflows uint32: computed in
			// uint32 it wraps to 0 and this permutation reads as unsatisfiable
			"permutation at the uint32 ceiling": {MaxCount: math.MaxUint32, MaxValue: math.MaxUint32, UniqueValues: true},
		} {
			c.Assert(ValidateBallotProtocol(bp), qt.IsNil, qt.Commentf("%s", name))
		}
		c.Assert(ValidateBallotProtocol(nil), qt.IsNil)
	})
}

// TestEffectiveQuestionType covers the question the plan gate has to ask: not what a stored
// question calls itself, but what its ballot actually is.
func TestEffectiveQuestionType(t *testing.T) {
	c := qt.New(t)

	c.Run("no protocol falls back to the stored type", func(c *qt.C) {
		c.Assert(EffectiveQuestionType(&db.VotingProcessQuestion{
			Type: db.VotingTypeMultiChoice, Choices: choices(3),
		}), qt.Equals, db.VotingTypeMultiChoice)
	})

	c.Run("a legacy question whose type contradicts its protocol", func(c *qt.C) {
		// stored before the halves were reconciled: labelled singlechoice, mints a ranking. The
		// effective type comes from the ballot, not the label, so it is ranked.
		c.Assert(EffectiveQuestionType(&db.VotingProcessQuestion{
			Type:           db.VotingTypeSingleChoice,
			Choices:        choices(3),
			BallotProtocol: &db.BallotProtocol{MaxCount: 3, MaxValue: 2, UniqueValues: true},
		}), qt.Equals, db.VotingTypeRanked)
	})

	c.Run("a raw protocol that is a named shape", func(c *qt.C) {
		// the case the plan gate stops missing: no type stated, but this is a singlechoice
		c.Assert(EffectiveQuestionType(&db.VotingProcessQuestion{
			Choices:        choices(3),
			BallotProtocol: &db.BallotProtocol{MaxCount: 1, MaxValue: 2},
		}), qt.Equals, db.VotingTypeSingleChoice)
	})
}
