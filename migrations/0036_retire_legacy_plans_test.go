package migrations

import (
	"testing"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestWithoutBillingEmail(t *testing.T) {
	c := qt.New(t)

	raw, err := bson.Marshal(bson.D{{Key: "email", Value: "a@b.c"}, {Key: "planID", Value: "p"}})
	c.Assert(err, qt.IsNil)
	got, err := withoutBillingEmail(bson.RawValue{Type: bson.TypeEmbeddedDocument, Value: raw})
	c.Assert(err, qt.IsNil)
	doc, ok := got.DocumentOK()
	c.Assert(ok, qt.IsTrue)
	_, err = doc.LookupErr("email")
	c.Assert(err, qt.IsNotNil)
	c.Assert(doc.Lookup("planID").StringValue(), qt.Equals, "p")

	null := bson.RawValue{Type: bson.TypeNull}
	got, err = withoutBillingEmail(null)
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.DeepEquals, null)

	// a document whose elements cannot be read is refused, never archived with its email
	bad := bson.RawValue{Type: bson.TypeEmbeddedDocument, Value: []byte{9, 0, 0, 0, 0x02, 'e', 0, 0xff, 0}}
	_, err = withoutBillingEmail(bad)
	c.Assert(err, qt.IsNotNil)
}
