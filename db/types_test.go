package db

import (
	"bytes"
	"testing"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestOrganizationDisplayNameAndLogoURL(t *testing.T) {
	t.Run("MetaShapes", func(t *testing.T) {
		c := qt.New(t)

		// legacy plain-string storage
		org := &Organization{Meta: map[string]any{
			"name": "Acme",
			"logo": "https://acme.org/logo.png",
		}}
		c.Assert(org.DisplayName(), qt.Equals, "Acme")
		c.Assert(org.LogoURL(), qt.Equals, "https://acme.org/logo.png")

		// locale map as set in memory by the API write path
		org = &Organization{Meta: map[string]any{
			"name": map[string]string{"default": "Acme"},
			"logo": map[string]string{"default": "https://acme.org/logo.png"},
		}}
		c.Assert(org.DisplayName(), qt.Equals, "Acme")
		c.Assert(org.LogoURL(), qt.Equals, "https://acme.org/logo.png")

		// missing, empty and unexpected shapes
		c.Assert((*Organization)(nil).DisplayName(), qt.Equals, "")
		c.Assert((&Organization{}).DisplayName(), qt.Equals, "")
		c.Assert((&Organization{Meta: map[string]any{}}).LogoURL(), qt.Equals, "")
		c.Assert((&Organization{Meta: map[string]any{"name": 42}}).DisplayName(), qt.Equals, "")
	})

	t.Run("BsonRoundTrip", func(t *testing.T) {
		c := qt.New(t)

		// A locale-map name/logo must survive a BSON round-trip, where nested
		// maps decode as map[string]any instead of their original Go type
		org := Organization{Meta: map[string]any{
			"name": map[string]string{"default": "Acme"},
			"logo": map[string]string{"default": "https://acme.org/logo.png"},
		}}
		raw, err := bson.Marshal(org)
		c.Assert(err, qt.IsNil)
		var decoded Organization
		c.Assert(bson.Unmarshal(raw, &decoded), qt.IsNil)

		c.Assert(decoded.DisplayName(), qt.Equals, "Acme")
		c.Assert(decoded.LogoURL(), qt.Equals, "https://acme.org/logo.png")
	})

	t.Run("DefaultDocumentMDecode", func(t *testing.T) {
		c := qt.New(t)

		// Our mongo.Client is configured with DefaultDocumentM: true (see
		// db/mongo.go), which makes untyped embedded documents decode as the
		// named type bson.M rather than the unnamed map[string]any. Reproduce
		// that exact decode path here instead of the plain bson.Unmarshal used
		// by BsonRoundTrip above, which doesn't set this option.
		org := Organization{Meta: map[string]any{
			"name": map[string]string{"default": "Acme"},
			"logo": map[string]string{"default": "https://acme.org/logo.png"},
		}}
		raw, err := bson.Marshal(org)
		c.Assert(err, qt.IsNil)

		dec := bson.NewDecoder(bson.NewDocumentReader(bytes.NewReader(raw)))
		dec.DefaultDocumentM()
		var decoded Organization
		c.Assert(dec.Decode(&decoded), qt.IsNil)

		_, ok := decoded.Meta["name"].(bson.M)
		c.Assert(ok, qt.IsTrue)

		c.Assert(decoded.DisplayName(), qt.Equals, "Acme")
		c.Assert(decoded.LogoURL(), qt.Equals, "https://acme.org/logo.png")
	})
}
