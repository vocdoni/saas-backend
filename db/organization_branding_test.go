package db

import (
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
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

// TestClaimOrganizationBrandingConcurrent is the race the claim exists for: several drafts
// of one organization quoting branding at the same moment. Exactly one may win, because the
// loser is what tells the API to re-price without branding — two winners means the
// organization is charged €149 twice for a once-per-organization add-on.
func TestClaimOrganizationBrandingConcurrent(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress}), qt.IsNil)

	const claimants = 16
	var wg sync.WaitGroup
	won := make([]bool, claimants)
	errs := make([]error, claimants)
	ids := make([]bson.ObjectID, claimants)
	for i := range claimants {
		ids[i] = bson.NewObjectID()
		wg.Go(func() {
			// every claimant observed an unclaimed organization, as they all read it before
			// any of them wrote
			won[i], errs[i] = testDB.ClaimOrganizationBranding(testOrgAddress, ids[i], bson.NilObjectID)
		})
	}
	wg.Wait()

	winners := 0
	for i, err := range errs {
		c.Assert(err, qt.IsNil)
		if won[i] {
			winners++
		}
	}
	c.Assert(winners, qt.Equals, 1)

	// the stored claim is the winner's, and re-claiming it is idempotent (a retry of the
	// same draft must not be told it lost)
	org, err := testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	winner := org.BrandingClaimedBy
	c.Assert(winner, qt.Not(qt.Equals), bson.NilObjectID)
	c.Assert(org.BrandingClaimedAt.IsZero(), qt.IsFalse)
	again, err := testDB.ClaimOrganizationBranding(testOrgAddress, winner, bson.NilObjectID)
	c.Assert(err, qt.IsNil)
	c.Assert(again, qt.IsTrue)
}

// TestClaimOrganizationBrandingTakeover pins the two ways a claim changes hands and the two
// ways it cannot: a caller that observed the current claimant may take it over, one that
// observed a stale value may not, and no one may claim branding the organization has paid.
func TestClaimOrganizationBrandingTakeover(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress}), qt.IsNil)

	first, second, third := bson.NewObjectID(), bson.NewObjectID(), bson.NewObjectID()
	won, err := testDB.ClaimOrganizationBranding(testOrgAddress, first, bson.NilObjectID)
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)

	// a caller that observed no claim at all cannot take over a claimed one
	won, err = testDB.ClaimOrganizationBranding(testOrgAddress, second, bson.NilObjectID)
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsFalse)

	// having observed first's claim and judged it releasable, second takes it over
	won, err = testDB.ClaimOrganizationBranding(testOrgAddress, second, first)
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)

	// third observed the same releasable claim but lost the race to second: its CAS no
	// longer matches, which is what stops both from being charged
	won, err = testDB.ClaimOrganizationBranding(testOrgAddress, third, first)
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsFalse)

	// once the organization has paid branding, nothing can claim it again
	stamped, err := testDB.SetOrganizationBrandingPaid(testOrgAddress, time.Now())
	c.Assert(err, qt.IsNil)
	c.Assert(stamped, qt.IsTrue)
	won, err = testDB.ClaimOrganizationBranding(testOrgAddress, second, second)
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsFalse)

	_, err = testDB.ClaimOrganizationBranding(testOrgAddress, bson.NilObjectID, bson.NilObjectID)
	c.Assert(err, qt.ErrorIs, ErrInvalidData)
}
