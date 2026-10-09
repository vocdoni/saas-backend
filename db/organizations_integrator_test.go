package db

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestSetOrganizationIntegratorLimits(t *testing.T) {
	c := qt.New(t)
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	c.Assert(testDB.SetOrganization(&Organization{
		Address:  testOrgAddress,
		Counters: OrganizationCounters{SentSMS: 3},
	}), qt.IsNil)

	// setting the override writes only integratorLimits
	c.Assert(testDB.SetOrganizationIntegratorLimits(testOrgAddress, &IntegratorLimits{MaxManagedOrgs: 5}), qt.IsNil)
	org, err := testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.IntegratorLimits, qt.DeepEquals, &IntegratorLimits{MaxManagedOrgs: 5})
	c.Assert(org.Counters.SentSMS, qt.Equals, 3)

	// a counter changed after the override is not touched by a later override update
	c.Assert(testDB.IncrementOrganizationSentSMSCounter(testOrgAddress), qt.IsNil)
	c.Assert(testDB.SetOrganizationIntegratorLimits(testOrgAddress, &IntegratorLimits{MaxManagedOrgs: 7}), qt.IsNil)
	org, err = testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.IntegratorLimits, qt.DeepEquals, &IntegratorLimits{MaxManagedOrgs: 7})
	c.Assert(org.Counters.SentSMS, qt.Equals, 4)

	// nil removes the override
	c.Assert(testDB.SetOrganizationIntegratorLimits(testOrgAddress, nil), qt.IsNil)
	org, err = testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.IntegratorLimits, qt.IsNil)

	// unknown organization
	c.Assert(testDB.SetOrganizationIntegratorLimits(testAnotherOrgAddress, &IntegratorLimits{MaxManagedOrgs: 1}),
		qt.ErrorIs, ErrNotFound)
}
