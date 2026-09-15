package apicommon

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestMultilingualTextUnmarshal(t *testing.T) {
	c := qt.New(t)

	type wrapper struct {
		V *MultilingualText `json:"v"`
	}

	unmarshal := func(s string) (*MultilingualText, error) {
		var w wrapper
		err := json.Unmarshal([]byte(`{"v":`+s+`}`), &w)
		return w.V, err
	}

	// plain string → normalised to {"default": "..."}
	got, err := unmarshal(`"hello"`)
	c.Assert(err, qt.IsNil)
	c.Assert(*got, qt.DeepEquals, MultilingualText{"default": "hello"})

	// object with default key → stored as-is
	got, err = unmarshal(`{"default":"hi","es":"hola"}`)
	c.Assert(err, qt.IsNil)
	c.Assert(*got, qt.DeepEquals, MultilingualText{"default": "hi", "es": "hola"})

	// object without default key → error
	_, err = unmarshal(`{"en":"hi"}`)
	c.Assert(err, qt.IsNotNil)

	// object with non-string value → error (rejected by map[string]string decode)
	_, err = unmarshal(`{"default":"hi","count":3}`)
	c.Assert(err, qt.IsNotNil)

	// completely invalid JSON → error
	_, err = unmarshal(`not-json`)
	c.Assert(err, qt.IsNotNil)

	// absent key → nil pointer
	var w wrapper
	c.Assert(json.Unmarshal([]byte(`{}`), &w), qt.IsNil)
	c.Assert(w.V, qt.IsNil)
}

func TestBuildOrgMeta(t *testing.T) {
	c := qt.New(t)

	name := MultilingualText{"default": "Acme"}
	logo := MultilingualText{"default": "https://acme.com/logo.png"}
	desc := MultilingualText{"default": "We make things"}

	// only name
	m := BuildOrgMeta(nil, &name, nil, nil, nil)
	c.Assert(m["name"], qt.DeepEquals, map[string]string{"default": "Acme"})
	_, hasLogo := m["logo"]
	c.Assert(hasLogo, qt.IsFalse)

	// all three fields
	m = BuildOrgMeta(nil, &name, &logo, &desc, nil)
	c.Assert(m["name"], qt.DeepEquals, map[string]string{"default": "Acme"})
	c.Assert(m["logo"], qt.DeepEquals, map[string]string{"default": "https://acme.com/logo.png"})
	c.Assert(m["description"], qt.DeepEquals, map[string]string{"default": "We make things"})

	// db accessors must resolve the shorthand values before any Mongo round-trip
	org := &db.Organization{Meta: m}
	c.Assert(org.DisplayName(), qt.Equals, "Acme")
	c.Assert(org.LogoURL(), qt.Equals, "https://acme.com/logo.png")

	// explicit meta wins over shorthand
	explicit := map[string]any{"name": MultilingualText{"default": "Override"}, "extra": "val"}
	m = BuildOrgMeta(nil, &name, nil, nil, explicit)
	c.Assert(m["name"], qt.DeepEquals, MultilingualText{"default": "Override"})
	c.Assert(m["extra"], qt.Equals, "val")

	// only explicit meta, no shorthand
	m = BuildOrgMeta(nil, nil, nil, nil, map[string]any{"foo": "bar"})
	c.Assert(m["foo"], qt.Equals, "bar")
	_, hasName := m["name"]
	c.Assert(hasName, qt.IsFalse)

	// all nil → empty map (not nil)
	m = BuildOrgMeta(nil, nil, nil, nil, nil)
	c.Assert(m, qt.IsNotNil)
	c.Assert(m, qt.HasLen, 0)

	// base keys are preserved and shorthands override them; explicit wins last
	base := map[string]any{"name": MultilingualText{"default": "Old"}, "keep": "me"}
	m = BuildOrgMeta(base, &name, nil, nil, nil)
	c.Assert(m["name"], qt.DeepEquals, map[string]string{"default": "Acme"})
	c.Assert(m["keep"], qt.Equals, "me")
}

func TestMultilingualFromAny(t *testing.T) {
	c := qt.New(t)

	// MultilingualText (in-memory, set at creation time)
	mt := MultilingualText{"default": "hello"}
	got := multilingualFromAny(mt)
	c.Assert(got, qt.IsNotNil)
	c.Assert(*got, qt.DeepEquals, mt)

	// map[string]string (named type)
	ms := map[string]string{"default": "world"}
	got = multilingualFromAny(ms)
	c.Assert(got, qt.IsNotNil)
	c.Assert(*got, qt.DeepEquals, MultilingualText{"default": "world"})

	// map[string]any with string values (the BSON-decoded form, see db/mongo.go)
	ma := map[string]any{"default": "bson", "es": "bson-es"}
	got = multilingualFromAny(ma)
	c.Assert(got, qt.IsNotNil)
	c.Assert(*got, qt.DeepEquals, MultilingualText{"default": "bson", "es": "bson-es"})

	// map[string]any with a non-string value → nil
	bad := map[string]any{"default": 42}
	got = multilingualFromAny(bad)
	c.Assert(got, qt.IsNil)

	// plain string (legacy storage) → normalised to {"default": "..."}
	got = multilingualFromAny("legacy name")
	c.Assert(got, qt.IsNotNil)
	c.Assert(*got, qt.DeepEquals, MultilingualText{"default": "legacy name"})

	// nil → nil
	got = multilingualFromAny(nil)
	c.Assert(got, qt.IsNil)

	// unknown type → nil
	got = multilingualFromAny(123)
	c.Assert(got, qt.IsNil)
}

// TestOrganizationFromDBAfterMongoRoundTrip is the regression test for #679: the
// name/logo/description shorthands used to vanish from every read, because the driver
// decoded untyped subdocuments as bson.M and multilingualFromAny only matches
// map[string]any.
func TestOrganizationFromDBAfterMongoRoundTrip(t *testing.T) {
	c := qt.New(t)

	stored := db.Organization{
		Address: common.HexToAddress("0xc98ec39e73dd24945397dfbdbd7721373bd4af70"),
		Meta: BuildOrgMeta(nil,
			&MultilingualText{"default": "TestOrg", "es": "OrgDePrueba"},
			&MultilingualText{"default": "https://acme.org/logo.png"},
			&MultilingualText{"default": "We make things"},
			nil),
	}
	raw, err := bson.Marshal(stored)
	c.Assert(err, qt.IsNil)

	// reproduce the client's decode path (see db/mongo.go)
	dec := bson.NewDecoder(bson.NewDocumentReader(bytes.NewReader(raw)))
	dec.DefaultDocumentMap()
	var decoded db.Organization
	c.Assert(dec.Decode(&decoded), qt.IsNil)

	got := OrganizationFromDB(&decoded, nil)
	c.Assert(got.Name, qt.IsNotNil)
	c.Assert(*got.Name, qt.DeepEquals, MultilingualText{"default": "TestOrg", "es": "OrgDePrueba"})
	c.Assert(got.Logo, qt.IsNotNil)
	c.Assert(*got.Logo, qt.DeepEquals, MultilingualText{"default": "https://acme.org/logo.png"})
	c.Assert(got.Description, qt.IsNotNil)
	c.Assert(*got.Description, qt.DeepEquals, MultilingualText{"default": "We make things"})

	// the shorthands must agree with the meta map they mirror
	c.Assert(got.Meta["name"], qt.DeepEquals, map[string]any{"default": "TestOrg", "es": "OrgDePrueba"})

	// and the display-name helper used for emails and on-chain account names
	c.Assert(OrgDisplayName(decoded.Meta, decoded.Address.String()), qt.Equals, "TestOrg")
}
