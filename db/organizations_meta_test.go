package db

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

// TestReplaceOrganizationMetaKeeping covers the whole-map replace a manager's POST performs: the
// kept identity keys must keep their stored values, whatever the request carried, and everything
// else is replaced verbatim (values starting with "$" included).
func TestReplaceOrganizationMetaKeeping(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	keep := []string{"name", "logo"}

	c.Assert(testDB.SetOrganization(&Organization{
		Address: testOrgAddress,
		Meta: map[string]any{
			"name":   map[string]any{"default": "Acme"},
			"region": "Europe",
		},
	}), qt.IsNil)

	t.Run("keeps stored identity and replaces the rest", func(t *testing.T) {
		c := qt.New(t)
		// the request carries a different name and a logo that is not stored: both must be ignored
		err := testDB.ReplaceOrganizationMetaKeeping(testOrgAddress, map[string]any{
			"name":   map[string]any{"default": "Evil"},
			"logo":   map[string]any{"default": "https://evil.example/x.png"},
			"region": "Spain",
			"note":   "$meta.name",
			"nested": map[string]any{"ref": "$region"},
		}, keep)
		c.Assert(err, qt.IsNil)

		org, err := testDB.Organization(testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(org.Meta, qt.DeepEquals, map[string]any{
			"name":   map[string]any{"default": "Acme"},
			"region": "Spain",
			"note":   "$meta.name",
			"nested": map[string]any{"ref": "$region"},
		})
	})

	t.Run("stored identity missing stays missing", func(t *testing.T) {
		c := qt.New(t)
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress}), qt.IsNil)
		c.Assert(testDB.ReplaceOrganizationMetaKeeping(testOrgAddress, map[string]any{
			"name": map[string]any{"default": "Evil"},
			"tag":  "x",
		}, keep), qt.IsNil)

		org, err := testDB.Organization(testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(org.Meta, qt.DeepEquals, map[string]any{"tag": "x"})
	})

	t.Run("null meta", func(t *testing.T) {
		c := qt.New(t)
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress}), qt.IsNil)
		_, err := testDB.organizations.UpdateOne(t.Context(), map[string]any{"_id": testOrgAddress},
			map[string]any{"$set": map[string]any{"meta": nil}})
		c.Assert(err, qt.IsNil)

		c.Assert(testDB.ReplaceOrganizationMetaKeeping(testOrgAddress, map[string]any{"tag": "x"}, keep), qt.IsNil)
		org, err := testDB.Organization(testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(org.Meta, qt.DeepEquals, map[string]any{"tag": "x"})
	})
}

// TestMetaPathErrorsAreInvalidMetaPath checks that the paths MongoDB refuses come back as
// ErrInvalidMetaPath, so the API can answer 400 instead of 500.
func TestMetaPathErrorsAreInvalidMetaPath(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{
		Address: testOrgAddress,
		Meta:    map[string]any{"region": "Europe"},
	}), qt.IsNil)

	// a sub-key written into a string value
	err := testDB.UpdateOrganizationMeta(testOrgAddress, map[string]any{"region.es": "Europa"})
	c.Assert(err, qt.ErrorIs, ErrInvalidMetaPath)

	// two paths where one is a prefix of the other
	err = testDB.UpdateOrganizationMeta(testOrgAddress, map[string]any{"tag": "a", "tag.b": "b"})
	c.Assert(err, qt.ErrorIs, ErrInvalidMetaPath)

	err = testDB.DeleteOrganizationMetaKeys(testOrgAddress, []string{"tag", "tag.b"})
	c.Assert(err, qt.ErrorIs, ErrInvalidMetaPath)

	// a valid update is not mistaken for a path error
	c.Assert(testDB.UpdateOrganizationMeta(testOrgAddress, map[string]any{"tag": "a"}), qt.IsNil)
}
