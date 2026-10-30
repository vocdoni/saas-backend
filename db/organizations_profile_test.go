package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

// TestUpdateOrganizationProfile covers the profile update used by the PUT handler: it must
// write only the profile fields, so counters incremented after the organization was read
// survive, and it must never create an organization that does not exist.
func TestUpdateOrganizationProfile(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	t.Run("keeps counters incremented after the read", func(t *testing.T) {
		c := qt.New(t)
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		c.Assert(testDB.SetOrganization(&Organization{
			Address:   testOrgAddress,
			Website:   "https://old.example",
			CreatedAt: time.Now(),
			Counters:  OrganizationCounters{SentSMS: 5},
		}), qt.IsNil)

		// the snapshot an update handler reads before doing its work
		snapshot, err := testDB.Organization(testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(snapshot.Counters.SentSMS, qt.Equals, 5)

		// counters move while the request is in flight
		for range 5 {
			c.Assert(testDB.IncrementOrganizationSentSMSCounter(testOrgAddress), qt.IsNil)
		}
		c.Assert(testDB.IncrementOrganizationSentEmailsCounter(testOrgAddress), qt.IsNil)

		snapshot.Website = "https://new.example"
		snapshot.Country = "ES"
		c.Assert(testDB.UpdateOrganizationProfile(snapshot), qt.IsNil)

		stored, err := testDB.Organization(testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(stored.Website, qt.Equals, "https://new.example")
		c.Assert(stored.Country, qt.Equals, "ES")
		c.Assert(stored.Counters.SentSMS, qt.Equals, 10)
		c.Assert(stored.Counters.SentEmails, qt.Equals, 1)
	})

	t.Run("does not create a missing organization", func(t *testing.T) {
		c := qt.New(t)
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		err := testDB.UpdateOrganizationProfile(&Organization{
			Address: testNonExistentOrg,
			Website: "https://ghost.example",
		})
		c.Assert(err, qt.ErrorIs, ErrNotFound)

		_, err = testDB.Organization(testNonExistentOrg)
		c.Assert(err, qt.ErrorIs, ErrNotFound)
	})

	t.Run("empty profile leaves the organization as it is", func(t *testing.T) {
		c := qt.New(t)
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		c.Assert(testDB.SetOrganization(&Organization{
			Address:   testOrgAddress,
			Website:   "https://kept.example",
			CreatedAt: time.Now(),
		}), qt.IsNil)

		c.Assert(testDB.UpdateOrganizationProfile(&Organization{Address: testOrgAddress}), qt.IsNil)

		stored, err := testDB.Organization(testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(stored.Website, qt.Equals, "https://kept.example")
	})
}
