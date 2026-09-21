package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

// TestSetOrganizationBrandingPaid pins the once-per-organization semantics: the first
// call stamps and reports true, later calls (and concurrent racers) match nothing and
// report false, and the stored timestamp never moves.
func TestSetOrganizationBrandingPaid(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress}), qt.IsNil)

	// a fresh organization has no branding-paid stamp, so pricing would charge it
	org, err := testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsTrue)

	// first payment stamps it
	first := time.Now().UTC().Truncate(time.Millisecond)
	set, err := testDB.SetOrganizationBrandingPaid(testOrgAddress, first)
	c.Assert(err, qt.IsNil)
	c.Assert(set, qt.IsTrue)
	org, err = testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsFalse)

	// a second payment does not re-stamp (reports false) and does not move the timestamp
	stamped := org.BrandingPaidAt
	set, err = testDB.SetOrganizationBrandingPaid(testOrgAddress, time.Now().Add(time.Hour))
	c.Assert(err, qt.IsNil)
	c.Assert(set, qt.IsFalse)
	org, err = testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.Equal(stamped), qt.IsTrue)

	// invalid inputs are rejected
	_, err = testDB.SetOrganizationBrandingPaid(testAnotherOrgAddress, time.Time{})
	c.Assert(err, qt.ErrorIs, ErrInvalidData)
}
