package pricing

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestBreakdownStoredAsServed: a breakdown is stored in MongoDB under the names the API serves
// it with, the embedded quote's fields inlined next to its own, and a process breakdown carries
// neither census field while a headroom one carries both.
func TestBreakdownStoredAsServed(t *testing.T) {
	c := qt.New(t)
	quote, err := Compute(QuoteInput{CensusSize: 600, SMSTwoFA: true})
	c.Assert(err, qt.IsNil)
	process := Breakdown{
		Shortfall: Shortfall{Quote: quote, BalanceCents: -500, ShortfallCents: 29_700},
		Currency:  "eur", ChargeCents: 29_700,
	}
	headroom := process
	headroom.PaidCents, headroom.CensusSize = 26_200, 600

	keys := func(b Breakdown) (served, stored []string) {
		raw, err := json.Marshal(b)
		c.Assert(err, qt.IsNil)
		var object map[string]any
		c.Assert(json.Unmarshal(raw, &object), qt.IsNil)
		doc, err := bson.Marshal(b)
		c.Assert(err, qt.IsNil)
		elements, err := bson.Raw(doc).Elements()
		c.Assert(err, qt.IsNil)
		for _, element := range elements {
			stored = append(stored, element.Key())
		}
		slices.Sort(stored)
		return slices.Sorted(maps.Keys(object)), stored
	}
	served, stored := keys(process)
	c.Assert(stored, qt.DeepEquals, served)
	c.Assert(served, qt.DeepEquals, []string{
		"balanceCents", "chargeCents", "currency", "lines", "quoteRecommended", "quoteRequired", "shortfallCents", "totalCents",
	})
	served, stored = keys(headroom)
	c.Assert(stored, qt.DeepEquals, served)
	c.Assert(served, qt.Contains, "paidCents")
	c.Assert(served, qt.Contains, "censusSize")

	// a line too, and the stored document reads back as it was written
	doc, err := bson.Marshal(headroom)
	c.Assert(err, qt.IsNil)
	amount, ok := bson.Raw(doc).Lookup("lines", "0", "amountCents").AsInt64OK()
	c.Assert(ok, qt.IsTrue)
	c.Assert(amount, qt.Equals, int64(quote.Lines[0].AmountCents))
	var read Breakdown
	c.Assert(bson.Unmarshal(doc, &read), qt.IsNil)
	c.Assert(read, qt.DeepEquals, headroom)
}
