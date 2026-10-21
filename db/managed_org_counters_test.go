package db

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
)

// TestSumManagedCounters exercises the /integrator dashboard aggregation against real Mongo:
// managed orgs are counted and their 2FA sends summed across the integrator's managed orgs
// only, and an integrator with no managed orgs reads as zero rather than erroring.
func TestSumManagedCounters(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	integrator := common.Address{0xA0}
	managed1 := common.Address{0xA1}
	managed2 := common.Address{0xA2}
	other := common.Address{0xB0} // unrelated standalone org — must be excluded

	c.Assert(testDB.SetOrganization(&Organization{Address: integrator, Country: "ES"}), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: managed1, ManagedBy: integrator, Country: "ES"}), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: managed2, ManagedBy: integrator, Country: "ES"}), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: other, Country: "ES"}), qt.IsNil)

	incN := func(fn func(common.Address) error, addr common.Address, n int) {
		for range n {
			c.Assert(fn(addr), qt.IsNil)
		}
	}
	// 2FA sends: managed1 = 3 sms + 5 emails, managed2 = 2 sms + 1 email, other = 9 sms.
	incN(testDB.IncrementOrganizationSentSMSCounter, managed1, 3)
	incN(testDB.IncrementOrganizationSentEmailsCounter, managed1, 5)
	incN(testDB.IncrementOrganizationSentSMSCounter, managed2, 2)
	incN(testDB.IncrementOrganizationSentEmailsCounter, managed2, 1)
	incN(testDB.IncrementOrganizationSentSMSCounter, other, 9)

	counters, err := testDB.SumManagedCounters(integrator)
	c.Assert(err, qt.IsNil)
	c.Assert(counters.ManagedOrgs, qt.Equals, 2) // the unrelated org is excluded
	c.Assert(counters.SentSMS, qt.Equals, 5)     // 3 + 2, the unrelated org's 9 are excluded
	c.Assert(counters.SentEmails, qt.Equals, 6)  // 5 + 1

	// an integrator with no managed orgs reads as zero, not an error
	none, err := testDB.SumManagedCounters(other)
	c.Assert(err, qt.IsNil)
	c.Assert(none, qt.DeepEquals, ManagedCounters{})
}
