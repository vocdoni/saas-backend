package db

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TestManagedOrgAggregates exercises the live integrator shared-pool readers against real
// Mongo: members and 2FA sends are summed across the integrator's managed orgs only, and an
// integrator with no managed orgs reads as zero rather than erroring.
func TestManagedOrgAggregates(t *testing.T) {
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

	addMember := func(addr common.Address, suffix string) {
		_, err := testDB.SetOrgMember("test_salt", &OrgMember{
			ID:           primitive.NewObjectID(),
			OrgAddress:   addr,
			MemberNumber: "mn-" + suffix,
			Email:        "m-" + suffix + "@x.test",
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		})
		c.Assert(err, qt.IsNil)
	}
	// 2 members in managed1, 1 in managed2, 3 in the unrelated org.
	addMember(managed1, "1a")
	addMember(managed1, "1b")
	addMember(managed2, "2a")
	addMember(other, "o1")
	addMember(other, "o2")
	addMember(other, "o3")

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

	members, err := testDB.CountMembersManagedBy(integrator)
	c.Assert(err, qt.IsNil)
	c.Assert(members, qt.Equals, int64(3)) // 2 + 1, the unrelated org's 3 are excluded

	sms, err := testDB.SumSentSMSManagedBy(integrator)
	c.Assert(err, qt.IsNil)
	c.Assert(sms, qt.Equals, 5) // 3 + 2, the unrelated org's 9 are excluded

	emails, err := testDB.SumSentEmailsManagedBy(integrator)
	c.Assert(err, qt.IsNil)
	c.Assert(emails, qt.Equals, 6) // 5 + 1

	// an integrator with no managed orgs reads as zero, not an error
	none, err := testDB.CountMembersManagedBy(other)
	c.Assert(err, qt.IsNil)
	c.Assert(none, qt.Equals, int64(0))
}
