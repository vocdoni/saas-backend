package api

import (
	"fmt"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// fakeElections is an electionRootLookup over a fixed set of elections, counting the lookups.
type fakeElections struct {
	roots   map[string]internal.HexBytes
	lookups int
	err     error
}

func (f *fakeElections) lookup(id internal.HexBytes) (internal.HexBytes, bool, error) {
	f.lookups++
	if f.err != nil {
		return nil, false, f.err
	}
	root, ok := f.roots[string(id)]
	return root, ok, nil
}

func TestMatchOrphanElections(t *testing.T) {
	c := qt.New(t)
	idA, idB, idOther := internal.HexBytes{0x0a}, internal.HexBytes{0x0b}, internal.HexBytes{0x0c}
	rootQ1, rootQ2 := internal.HexBytes{0x02, 0x01}, internal.HexBytes{0x02, 0x02}
	q1, q2, q3 := bson.NewObjectID(), bson.NewObjectID(), bson.NewObjectID()
	// questions with the same origin and envelope type share their candidate ids: only the census
	// root tells their elections apart
	ids := []internal.HexBytes{idOther, idA, idB}

	c.Run("adopts by census root", func(c *qt.C) {
		elections := &fakeElections{roots: map[string]internal.HexBytes{
			string(idOther): {0x03, 0x09}, // another process of the organization
			string(idA):     rootQ2,
			string(idB):     rootQ1,
		}}
		adopted, err := matchOrphanElections([]orphanCandidate{
			{question: q1, root: rootQ1, ids: ids},
			{question: q2, root: rootQ2, ids: ids},
			{question: q3, root: internal.HexBytes{0x02, 0x03}, ids: ids},
		}, elections.lookup)
		c.Assert(err, qt.IsNil)
		c.Assert(adopted, qt.DeepEquals, map[bson.ObjectID]internal.HexBytes{q1: idB, q2: idA})
		c.Assert(elections.lookups, qt.Equals, len(ids))
	})

	c.Run("missing elections are not adopted", func(c *qt.C) {
		elections := &fakeElections{roots: map[string]internal.HexBytes{}}
		adopted, err := matchOrphanElections([]orphanCandidate{{question: q1, root: rootQ1, ids: ids}}, elections.lookup)
		c.Assert(err, qt.IsNil)
		c.Assert(adopted, qt.HasLen, 0)
	})

	c.Run("a candidate without a root never matches", func(c *qt.C) {
		elections := &fakeElections{roots: map[string]internal.HexBytes{string(idA): nil}}
		adopted, err := matchOrphanElections([]orphanCandidate{{question: q1, ids: ids}}, elections.lookup)
		c.Assert(err, qt.IsNil)
		c.Assert(adopted, qt.HasLen, 0)
		c.Assert(elections.lookups, qt.Equals, 0)
	})

	c.Run("an election is given to one question only", func(c *qt.C) {
		elections := &fakeElections{roots: map[string]internal.HexBytes{string(idA): rootQ1}}
		adopted, err := matchOrphanElections([]orphanCandidate{
			{question: q1, root: rootQ1, ids: ids},
			{question: q2, root: rootQ1, ids: ids},
		}, elections.lookup)
		c.Assert(err, qt.IsNil)
		c.Assert(adopted, qt.DeepEquals, map[bson.ObjectID]internal.HexBytes{q1: idA})
	})

	c.Run("lookup errors are returned", func(c *qt.C) {
		elections := &fakeElections{err: fmt.Errorf("node unavailable")}
		_, err := matchOrphanElections([]orphanCandidate{{question: q1, root: rootQ1, ids: ids}}, elections.lookup)
		c.Assert(err, qt.ErrorMatches, "node unavailable")
	})
}
