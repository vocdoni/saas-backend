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
	won := make([]time.Time, claimants)
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
		if !won[i].IsZero() {
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
	c.Assert(again.IsZero(), qt.IsFalse)
}

// TestClaimOrganizationBrandingTakeover pins the ways a claim changes hands and the ways it
// cannot: a caller that observed the current claimant may take it over once the claim is
// stale, never while it is fresh; one that observed an outdated claimant may not; and no one
// may claim branding the organization has paid.
func TestClaimOrganizationBrandingTakeover(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress}), qt.IsNil)

	first, second, third := bson.NewObjectID(), bson.NewObjectID(), bson.NewObjectID()
	won, err := testDB.ClaimOrganizationBranding(testOrgAddress, first, bson.NilObjectID)
	c.Assert(err, qt.IsNil)
	c.Assert(won.IsZero(), qt.IsFalse)

	// a caller that observed no claim at all cannot take over a claimed one
	won, err = testDB.ClaimOrganizationBranding(testOrgAddress, second, bson.NilObjectID)
	c.Assert(err, qt.IsNil)
	c.Assert(won.IsZero(), qt.IsTrue)

	// a fresh claim is not taken over even by a caller that observed it: its claimant may be
	// between refreshing the claim and storing the payment that backs it
	won, err = testDB.ClaimOrganizationBranding(testOrgAddress, second, first)
	c.Assert(err, qt.IsNil)
	c.Assert(won.IsZero(), qt.IsTrue)

	// once stale, second takes over first's claim, which it observed and judged releasable
	restore := BrandingClaimStaleAfter
	BrandingClaimStaleAfter = -time.Minute
	defer func() { BrandingClaimStaleAfter = restore }()
	won, err = testDB.ClaimOrganizationBranding(testOrgAddress, second, first)
	c.Assert(err, qt.IsNil)
	c.Assert(won.IsZero(), qt.IsFalse)

	// third observed the same releasable claim but lost the race to second: its CAS no
	// longer matches, which is what stops both from being charged
	won, err = testDB.ClaimOrganizationBranding(testOrgAddress, third, first)
	c.Assert(err, qt.IsNil)
	c.Assert(won.IsZero(), qt.IsTrue)

	// once the organization has paid branding, nothing can claim it again
	stamped, err := testDB.SetOrganizationBrandingPaid(testOrgAddress, time.Now())
	c.Assert(err, qt.IsNil)
	c.Assert(stamped, qt.IsTrue)
	won, err = testDB.ClaimOrganizationBranding(testOrgAddress, second, second)
	c.Assert(err, qt.IsNil)
	c.Assert(won.IsZero(), qt.IsTrue)

	_, err = testDB.ClaimOrganizationBranding(testOrgAddress, bson.NilObjectID, bson.NilObjectID)
	c.Assert(err, qt.ErrorIs, ErrInvalidData)
}

// TestReleaseOrganizationBrandingClaim: a refused paying attempt gives back exactly the claim it
// wrote — not a newer one a concurrent attempt of the same draft refreshed it to, not another
// process's, and never an add-on the organization has paid.
func TestReleaseOrganizationBrandingClaim(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress}), qt.IsNil)
	claimant := bson.NewObjectID()
	claimedBy := func() bson.ObjectID {
		org, err := testDB.Organization(testOrgAddress)
		c.Assert(err, qt.IsNil)
		return org.BrandingClaimedBy
	}

	first, err := testDB.ClaimOrganizationBranding(testOrgAddress, claimant, bson.NilObjectID)
	c.Assert(err, qt.IsNil)
	c.Assert(first.IsZero(), qt.IsFalse)
	time.Sleep(2 * time.Millisecond)
	refreshed, err := testDB.ClaimOrganizationBranding(testOrgAddress, claimant, claimant)
	c.Assert(err, qt.IsNil)
	c.Assert(refreshed.After(first), qt.IsTrue)

	// the first attempt's release no longer matches: the refresh belongs to another attempt
	c.Assert(testDB.ReleaseOrganizationBrandingClaim(testOrgAddress, claimant, first), qt.IsNil)
	c.Assert(claimedBy(), qt.Equals, claimant)
	// nor does another process's
	c.Assert(testDB.ReleaseOrganizationBrandingClaim(testOrgAddress, bson.NewObjectID(), refreshed), qt.IsNil)
	c.Assert(claimedBy(), qt.Equals, claimant)
	// the attempt that wrote it releases it, and the add-on is claimable again
	c.Assert(testDB.ReleaseOrganizationBrandingClaim(testOrgAddress, claimant, refreshed), qt.IsNil)
	c.Assert(claimedBy(), qt.Equals, bson.NilObjectID)

	// a paid add-on is never released by an abandoned attempt
	claimedAt, err := testDB.ClaimOrganizationBranding(testOrgAddress, claimant, bson.NilObjectID)
	c.Assert(err, qt.IsNil)
	stamped, err := testDB.SetOrganizationBrandingPaid(testOrgAddress, time.Now())
	c.Assert(err, qt.IsNil)
	c.Assert(stamped, qt.IsTrue)
	c.Assert(testDB.ReleaseOrganizationBrandingClaim(testOrgAddress, claimant, claimedAt), qt.IsNil)
	c.Assert(claimedBy(), qt.Equals, claimant)

	err = testDB.ReleaseOrganizationBrandingClaim(testOrgAddress, claimant, time.Time{})
	c.Assert(err, qt.ErrorIs, ErrInvalidData)
}

// TestSetOrganizationKeepsBrandingState: a read-modify-write of the organization (an org PUT,
// a subscription webhook) must not write the branding claim back from a stale read, or it
// would resurrect a claim released in between.
func TestSetOrganizationKeepsBrandingState(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress}), qt.IsNil)

	processID := bson.NewObjectID()
	claimedAt, err := testDB.ClaimOrganizationBranding(testOrgAddress, processID, bson.NilObjectID)
	c.Assert(err, qt.IsNil)
	c.Assert(claimedAt.IsZero(), qt.IsFalse)
	stale, err := testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(stale.BrandingClaimedBy, qt.Equals, processID)

	c.Assert(testDB.ReleaseOrganizationBrandingClaim(testOrgAddress, processID, claimedAt), qt.IsNil)
	stale.Website = "https://example.org"
	c.Assert(testDB.SetOrganization(stale), qt.IsNil)

	org, err := testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.Website, qt.Equals, "https://example.org")
	c.Assert(org.BrandingClaimedBy, qt.Equals, bson.NilObjectID)
	c.Assert(org.BrandingClaimedAt.IsZero(), qt.IsTrue)
}
