package db

import (
	"testing"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/bson"
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
}
