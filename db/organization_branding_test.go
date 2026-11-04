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
	set, err := testDB.SetOrganizationAddOnPaid(testOrgAddress, OrgAddOnBranding, first)
	c.Assert(err, qt.IsNil)
	c.Assert(set, qt.IsTrue)
	org, err = testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsFalse)

	// a second payment does not re-stamp (reports false) and does not move the timestamp
	stamped := org.BrandingPaidAt
	set, err = testDB.SetOrganizationAddOnPaid(testOrgAddress, OrgAddOnBranding, time.Now().Add(time.Hour))
	c.Assert(err, qt.IsNil)
	c.Assert(set, qt.IsFalse)
	org, err = testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.Equal(stamped), qt.IsTrue)

	// invalid inputs are rejected
	_, err = testDB.SetOrganizationAddOnPaid(testAnotherOrgAddress, OrgAddOnBranding, time.Time{})
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
			won[i], errs[i] = claimAddOn(OrgAddOnBranding, ids[i], bson.NilObjectID)
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
	again, err := claimAddOn(OrgAddOnBranding, winner, bson.NilObjectID)
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
	won, err := claimAddOn(OrgAddOnBranding, first, bson.NilObjectID)
	c.Assert(err, qt.IsNil)
	c.Assert(won.IsZero(), qt.IsFalse)

	// a caller that observed no claim at all cannot take over a claimed one
	won, err = claimAddOn(OrgAddOnBranding, second, bson.NilObjectID)
	c.Assert(err, qt.IsNil)
	c.Assert(won.IsZero(), qt.IsTrue)

	// a fresh claim is not taken over even by a caller that observed it: its claimant may be
	// between refreshing the claim and storing the payment that backs it
	won, err = claimAddOn(OrgAddOnBranding, second, first)
	c.Assert(err, qt.IsNil)
	c.Assert(won.IsZero(), qt.IsTrue)

	// once stale, second takes over first's claim, which it observed and judged releasable
	restore := OrgAddOnClaimStaleAfter
	OrgAddOnClaimStaleAfter = -time.Minute
	defer func() { OrgAddOnClaimStaleAfter = restore }()
	won, err = claimAddOn(OrgAddOnBranding, second, first)
	c.Assert(err, qt.IsNil)
	c.Assert(won.IsZero(), qt.IsFalse)

	// third observed the same releasable claim but lost the race to second: its CAS no
	// longer matches, which is what stops both from being charged
	won, err = claimAddOn(OrgAddOnBranding, third, first)
	c.Assert(err, qt.IsNil)
	c.Assert(won.IsZero(), qt.IsTrue)

	// once the organization has paid branding, nothing can claim it again
	stamped, err := testDB.SetOrganizationAddOnPaid(testOrgAddress, OrgAddOnBranding, time.Now())
	c.Assert(err, qt.IsNil)
	c.Assert(stamped, qt.IsTrue)
	won, err = claimAddOn(OrgAddOnBranding, second, second)
	c.Assert(err, qt.IsNil)
	c.Assert(won.IsZero(), qt.IsTrue)

	_, err = claimAddOn(OrgAddOnBranding, bson.NilObjectID, bson.NilObjectID)
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

	first, err := claimAddOn(OrgAddOnBranding, claimant, bson.NilObjectID)
	c.Assert(err, qt.IsNil)
	c.Assert(first.IsZero(), qt.IsFalse)
	time.Sleep(2 * time.Millisecond)
	refreshed, err := claimAddOn(OrgAddOnBranding, claimant, claimant)
	c.Assert(err, qt.IsNil)
	c.Assert(refreshed.After(first), qt.IsTrue)

	// the first attempt's release no longer matches: the refresh belongs to another attempt
	c.Assert(releaseAddOnClaim(OrgAddOnBranding, claimant, first), qt.IsNil)
	c.Assert(claimedBy(), qt.Equals, claimant)
	// nor does another process's
	c.Assert(releaseAddOnClaim(OrgAddOnBranding, bson.NewObjectID(), refreshed), qt.IsNil)
	c.Assert(claimedBy(), qt.Equals, claimant)
	// the attempt that wrote it releases it, and the add-on is claimable again
	c.Assert(releaseAddOnClaim(OrgAddOnBranding, claimant, refreshed), qt.IsNil)
	c.Assert(claimedBy(), qt.Equals, bson.NilObjectID)

	// a paid add-on is never released by an abandoned attempt
	claimedAt, err := claimAddOn(OrgAddOnBranding, claimant, bson.NilObjectID)
	c.Assert(err, qt.IsNil)
	stamped, err := testDB.SetOrganizationAddOnPaid(testOrgAddress, OrgAddOnBranding, time.Now())
	c.Assert(err, qt.IsNil)
	c.Assert(stamped, qt.IsTrue)
	c.Assert(releaseAddOnClaim(OrgAddOnBranding, claimant, claimedAt), qt.IsNil)
	c.Assert(claimedBy(), qt.Equals, claimant)

	err = releaseAddOnClaim(OrgAddOnBranding, claimant, time.Time{})
	c.Assert(err, qt.ErrorIs, ErrInvalidData)
}

// TestReleaseOrganizationBranding: the release a refund runs is idempotent, so a delete retried
// after it can run it again, and it never touches branding another process holds.
func TestReleaseOrganizationBranding(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress}), qt.IsNil)
	payer, other := bson.NewObjectID(), bson.NewObjectID()
	_, err := claimAddOn(OrgAddOnBranding, payer, bson.NilObjectID)
	c.Assert(err, qt.IsNil)
	stamped, err := testDB.SetOrganizationAddOnPaid(testOrgAddress, OrgAddOnBranding, time.Now())
	c.Assert(err, qt.IsNil)
	c.Assert(stamped, qt.IsTrue)

	// another process's release leaves the payer's branding alone
	c.Assert(testDB.ReleaseOrganizationAddOn(OrgAddOnClaim{
		OrgAddress: testOrgAddress, AddOn: OrgAddOnBranding, ProcessID: other, ClaimedBefore: time.Time{},
	}), qt.IsNil)
	org, err := testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsFalse)

	// pinned to claims taken before a time: a claim the payer took at or after it stays
	c.Assert(testDB.ReleaseOrganizationAddOn(OrgAddOnClaim{
		OrgAddress: testOrgAddress, AddOn: OrgAddOnBranding, ProcessID: payer, ClaimedBefore: org.BrandingClaimedAt,
	}), qt.IsNil)
	org, err = testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsFalse)

	for range 2 { // the second run is a retry's: a no-op
		c.Assert(testDB.ReleaseOrganizationAddOn(OrgAddOnClaim{
			OrgAddress: testOrgAddress, AddOn: OrgAddOnBranding, ProcessID: payer,
			ClaimedBefore: org.BrandingClaimedAt.Add(time.Millisecond),
		}), qt.IsNil)
		org, err = testDB.Organization(testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(org.BrandingPaidAt.IsZero(), qt.IsTrue)
		c.Assert(org.BrandingClaimedBy, qt.Equals, bson.NilObjectID)
	}
}

// TestSetOrganizationBrandingPaidForClaim: a stamp derived from a claimant's payment lands only
// while that exact claim stands: never after the claimant's credit-back released it, nor over the
// claim it took again since, so it never leaves a stamp no claimant could undo.
func TestSetOrganizationBrandingPaidForClaim(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress}), qt.IsNil)
	holder := bson.NewObjectID()
	stampFor := func(claimedAt time.Time) bool {
		stamped, err := testDB.SetOrganizationAddOnPaidForClaim(OrgAddOnClaim{
			OrgAddress: testOrgAddress, AddOn: OrgAddOnBranding, ProcessID: holder, ClaimedAt: claimedAt,
		})
		c.Assert(err, qt.IsNil)
		return stamped
	}
	judged, err := claimAddOn(OrgAddOnBranding, holder, bson.NilObjectID)
	c.Assert(err, qt.IsNil)

	// the holder's credit-back released the claim it was judged by: nothing to stamp
	c.Assert(testDB.ReleaseOrganizationAddOn(OrgAddOnClaim{
		OrgAddress: testOrgAddress, AddOn: OrgAddOnBranding, ProcessID: holder, ClaimedBefore: judged.Add(time.Millisecond),
	}), qt.IsNil)
	c.Assert(stampFor(judged), qt.IsFalse)
	org, err := testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsTrue)

	// it claimed again since: the stale judgement still stamps nothing, the live claim does
	time.Sleep(2 * time.Millisecond)
	again, err := claimAddOn(OrgAddOnBranding, holder, bson.NilObjectID)
	c.Assert(err, qt.IsNil)
	c.Assert(stampFor(judged), qt.IsFalse)
	c.Assert(stampFor(again), qt.IsTrue)
	c.Assert(stampFor(again), qt.IsFalse) // stamped once
	org, err = testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsFalse)

	_, err = testDB.SetOrganizationAddOnPaidForClaim(OrgAddOnClaim{OrgAddress: testOrgAddress, AddOn: OrgAddOnBranding})
	c.Assert(err, qt.ErrorIs, ErrInvalidData)
}

// TestSetOrganizationKeepsBrandingState: a stale organization save does not resurrect a
// released branding claim.
func TestSetOrganizationKeepsBrandingState(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress}), qt.IsNil)

	processID := bson.NewObjectID()
	claimedAt, err := claimAddOn(OrgAddOnBranding, processID, bson.NilObjectID)
	c.Assert(err, qt.IsNil)
	c.Assert(claimedAt.IsZero(), qt.IsFalse)
	stale, err := testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(stale.BrandingClaimedBy, qt.Equals, processID)

	c.Assert(releaseAddOnClaim(OrgAddOnBranding, processID, claimedAt), qt.IsNil)
	stale.Website = "https://example.org"
	c.Assert(testDB.SetOrganization(stale), qt.IsNil)

	org, err := testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.Website, qt.Equals, "https://example.org")
	c.Assert(org.BrandingClaimedBy, qt.Equals, bson.NilObjectID)
	c.Assert(org.BrandingClaimedAt.IsZero(), qt.IsTrue)
}

// claimAddOn claims addOn of testOrgAddress for processID, taking over observed if stale.
func claimAddOn(addOn OrgAddOn, processID, observed bson.ObjectID) (time.Time, error) {
	return testDB.ClaimOrganizationAddOn(OrgAddOnClaim{
		OrgAddress: testOrgAddress, AddOn: addOn, ProcessID: processID, Observed: observed,
	})
}

// releaseAddOnClaim releases processID's claim on addOn of testOrgAddress taken at claimedAt.
func releaseAddOnClaim(addOn OrgAddOn, processID bson.ObjectID, claimedAt time.Time) error {
	return testDB.ReleaseOrganizationAddOnClaim(OrgAddOnClaim{
		OrgAddress: testOrgAddress, AddOn: addOn, ProcessID: processID, ClaimedAt: claimedAt,
	})
}

// TestOrgAddOnsAreIndependent: each once-per-organization add-on has its own claim and stamp, so
// claiming, stamping or releasing one never touches the other; an unknown add-on is refused.
func TestOrgAddOnsAreIndependent(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress}), qt.IsNil)
	brandingHolder, urlHolder := bson.NewObjectID(), bson.NewObjectID()

	claimedAt, err := claimAddOn(OrgAddOnBranding, brandingHolder, bson.NilObjectID)
	c.Assert(err, qt.IsNil)
	c.Assert(claimedAt.IsZero(), qt.IsFalse)
	claimedAt, err = claimAddOn(OrgAddOnCustomURL, urlHolder, bson.NilObjectID)
	c.Assert(err, qt.IsNil)
	c.Assert(claimedAt.IsZero(), qt.IsFalse)
	stamped, err := testDB.SetOrganizationAddOnPaid(testOrgAddress, OrgAddOnCustomURL, time.Now())
	c.Assert(err, qt.IsNil)
	c.Assert(stamped, qt.IsTrue)

	org, err := testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	brandingClaimant, _ := org.AddOnClaim(OrgAddOnBranding)
	urlClaimant, _ := org.AddOnClaim(OrgAddOnCustomURL)
	c.Assert(brandingClaimant, qt.Equals, brandingHolder)
	c.Assert(urlClaimant, qt.Equals, urlHolder)
	c.Assert(org.AddOnPaidAt(OrgAddOnCustomURL).IsZero(), qt.IsFalse)
	c.Assert(org.AddOnPaidAt(OrgAddOnBranding).IsZero(), qt.IsTrue)

	// releasing the custom URL leaves branding's claim alone
	c.Assert(testDB.ReleaseOrganizationAddOn(OrgAddOnClaim{
		OrgAddress: testOrgAddress, AddOn: OrgAddOnCustomURL, ProcessID: urlHolder, ClaimedBefore: time.Time{},
	}), qt.IsNil)
	org, err = testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.CustomURLPaidAt.IsZero(), qt.IsTrue)
	c.Assert(org.CustomURLClaimedBy, qt.Equals, bson.NilObjectID)
	c.Assert(org.BrandingClaimedBy, qt.Equals, brandingHolder)

	_, err = claimAddOn("signedCertificate", bson.NewObjectID(), bson.NilObjectID)
	c.Assert(err, qt.ErrorIs, ErrInvalidData)
}
