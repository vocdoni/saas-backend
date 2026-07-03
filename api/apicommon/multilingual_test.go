package apicommon

import (
	"encoding/json"
	"testing"

	qt "github.com/frankban/quicktest"
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
	m := BuildOrgMeta(&name, nil, nil, nil)
	c.Assert(m["name"], qt.DeepEquals, MultilingualText{"default": "Acme"})
	_, hasLogo := m["logo"]
	c.Assert(hasLogo, qt.IsFalse)

	// all three fields
	m = BuildOrgMeta(&name, &logo, &desc, nil)
	c.Assert(m["name"], qt.DeepEquals, MultilingualText{"default": "Acme"})
	c.Assert(m["logo"], qt.DeepEquals, MultilingualText{"default": "https://acme.com/logo.png"})
	c.Assert(m["description"], qt.DeepEquals, MultilingualText{"default": "We make things"})

	// explicit meta wins over shorthand
	explicit := map[string]any{"name": MultilingualText{"default": "Override"}, "extra": "val"}
	m = BuildOrgMeta(&name, nil, nil, explicit)
	c.Assert(m["name"], qt.DeepEquals, MultilingualText{"default": "Override"})
	c.Assert(m["extra"], qt.Equals, "val")

	// only explicit meta, no shorthand
	m = BuildOrgMeta(nil, nil, nil, map[string]any{"foo": "bar"})
	c.Assert(m["foo"], qt.Equals, "bar")
	_, hasName := m["name"]
	c.Assert(hasName, qt.IsFalse)

	// all nil → empty map (not nil)
	m = BuildOrgMeta(nil, nil, nil, nil)
	c.Assert(m, qt.IsNotNil)
	c.Assert(m, qt.HasLen, 0)
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

	// map[string]any with string values (BSON-decoded form)
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
