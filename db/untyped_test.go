package db

import (
	"bytes"
	"testing"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// decodeDefaultDocumentM reproduces the decode path of our mongo.Client, which is
// configured with DefaultDocumentM (see db/mongo.go): untyped embedded documents
// come back as the named type bson.M rather than the unnamed map[string]any.
func decodeDefaultDocumentM(c *qt.C, raw []byte, v any) {
	c.Helper()
	dec := bson.NewDecoder(bson.NewDocumentReader(bytes.NewReader(raw)))
	dec.DefaultDocumentM()
	c.Assert(dec.Decode(v), qt.IsNil)
}

func TestUntypedDocNormalizesDriverTypes(t *testing.T) {
	type doc struct {
		D UntypedDoc `bson:"d"`
	}

	nested := map[string]any{
		"locale": map[string]string{"default": "Acme", "es": "Acme ES"},
		"list": []any{
			"plain",
			map[string]any{"inner": map[string]string{"deep": "value"}},
		},
		"count": int64(3),
		"flag":  true,
	}

	// assertNormalized walks the decoded document and fails on any driver type,
	// at any depth. bson.M/bson.A share their underlying type with the plain Go
	// ones, so only a type switch (not DeepEquals) can tell them apart.
	var assertNormalized func(c *qt.C, v any)
	assertNormalized = func(c *qt.C, v any) {
		c.Helper()
		switch t := v.(type) {
		case bson.M, bson.D, bson.A:
			c.Fatalf("driver type %T survived normalization: %v", v, v)
		case map[string]any:
			for _, val := range t {
				assertNormalized(c, val)
			}
		case []any:
			for _, val := range t {
				assertNormalized(c, val)
			}
		default:
			// scalar: nothing to walk
		}
	}

	assertContent := func(c *qt.C, got UntypedDoc) {
		c.Helper()
		assertNormalized(c, map[string]any(got))

		locale, ok := got["locale"].(map[string]any)
		c.Assert(ok, qt.IsTrue)
		c.Assert(locale["default"], qt.Equals, "Acme")
		c.Assert(locale["es"], qt.Equals, "Acme ES")

		list, ok := got["list"].([]any)
		c.Assert(ok, qt.IsTrue)
		c.Assert(list, qt.HasLen, 2)
		c.Assert(list[0], qt.Equals, "plain")
		elem, ok := list[1].(map[string]any)
		c.Assert(ok, qt.IsTrue)
		inner, ok := elem["inner"].(map[string]any)
		c.Assert(ok, qt.IsTrue)
		c.Assert(inner["deep"], qt.Equals, "value")

		// scalars are untouched
		c.Assert(got["count"], qt.Equals, int64(3))
		flag, ok := got["flag"].(bool)
		c.Assert(ok, qt.IsTrue)
		c.Assert(flag, qt.IsTrue)
	}

	raw, err := bson.Marshal(doc{D: nested})
	qt.Assert(t, err, qt.IsNil)

	t.Run("DefaultDocumentM", func(t *testing.T) {
		c := qt.New(t)
		var got doc
		decodeDefaultDocumentM(c, raw, &got)
		assertContent(c, got.D)
	})

	// the default registry decodes embedded documents as bson.D instead; the
	// normalization must cover that shape too.
	t.Run("DefaultDocumentD", func(t *testing.T) {
		c := qt.New(t)
		var got doc
		c.Assert(bson.Unmarshal(raw, &got), qt.IsNil)
		assertContent(c, got.D)
	})

	t.Run("Null", func(t *testing.T) {
		c := qt.New(t)
		raw, err := bson.Marshal(bson.M{"d": nil})
		c.Assert(err, qt.IsNil)
		var got doc
		decodeDefaultDocumentM(c, raw, &got)
		c.Assert(got.D, qt.IsNil)
	})

	t.Run("Empty", func(t *testing.T) {
		c := qt.New(t)
		raw, err := bson.Marshal(bson.M{"d": bson.M{}})
		c.Assert(err, qt.IsNil)
		var got doc
		decodeDefaultDocumentM(c, raw, &got)
		c.Assert(got.D, qt.Not(qt.IsNil))
		c.Assert(got.D, qt.HasLen, 0)
	})
}
