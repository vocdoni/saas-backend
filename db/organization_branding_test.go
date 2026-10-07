package db

import (
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestSetOrganizationBrandingPaid: only the first call stamps; the timestamp never moves.
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

// TestClaimOrganizationBrandingConcurrent: of concurrent claimants exactly one wins, or
// branding is charged twice.
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

// TestClaimOrganizationBrandingTakeover: a claim is taken over only when stale and as observed,
// and never once branding is paid.
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

// TestReleaseOrganizationBrandingClaim: a release removes only the exact claim it wrote.
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

// TestReleaseOrganizationBranding: the release a refund runs is idempotent, so a delete retried
// after it can run it again, and it never touches branding another process holds.
func TestReleaseOrganizationBranding(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress}), qt.IsNil)
	payer, other := bson.NewObjectID(), bson.NewObjectID()
	_, err := testDB.ClaimOrganizationBranding(testOrgAddress, payer, bson.NilObjectID)
	c.Assert(err, qt.IsNil)
	stamped, err := testDB.SetOrganizationBrandingPaid(testOrgAddress, time.Now())
	c.Assert(err, qt.IsNil)
	c.Assert(stamped, qt.IsTrue)

	// another process's release leaves the payer's branding alone
	c.Assert(testDB.ReleaseOrganizationBranding(testOrgAddress, other), qt.IsNil)
	org, err := testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsFalse)

	for range 2 { // the second run is a retry's: a no-op
		c.Assert(testDB.ReleaseOrganizationBranding(testOrgAddress, payer), qt.IsNil)
		org, err = testDB.Organization(testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(org.BrandingPaidAt.IsZero(), qt.IsTrue)
		c.Assert(org.BrandingClaimedBy, qt.Equals, bson.NilObjectID)
	}
}

// TestSetOrganizationKeepsBrandingState: a stale organization save does not resurrect a
// released branding claim.
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
