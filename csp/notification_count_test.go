package csp

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/test"
)

func TestCountSentNotificationOnlyCountsDelivered(t *testing.T) {
	c := qt.New(t)
	testDB, err := db.New(testMongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)

	orgAddr := common.Address{0xC1}
	c.Assert(testDB.SetOrganization(&db.Organization{Address: orgAddr}), qt.IsNil)
	counters := func() db.OrganizationCounters {
		org, err := testDB.Organization(orgAddr)
		c.Assert(err, qt.IsNil)
		return org.Counters
	}

	// given-up challenges (expired, permanently failed, no service configured)
	// must not use up the org's sent quota
	countSentNotification(testDB, &notifications.NotificationChallenge{
		Type: notifications.EmailChallenge, OrgAddress: orgAddr, Success: false,
	})
	countSentNotification(testDB, &notifications.NotificationChallenge{
		Type: notifications.SMSChallenge, OrgAddress: orgAddr, Success: false,
	})
	c.Assert(counters().SentEmails, qt.Equals, 0)
	c.Assert(counters().SentSMS, qt.Equals, 0)

	// delivered challenges are counted
	countSentNotification(testDB, &notifications.NotificationChallenge{
		Type: notifications.EmailChallenge, OrgAddress: orgAddr, Success: true,
	})
	countSentNotification(testDB, &notifications.NotificationChallenge{
		Type: notifications.SMSChallenge, OrgAddress: orgAddr, Success: true,
	})
	c.Assert(counters().SentEmails, qt.Equals, 1)
	c.Assert(counters().SentSMS, qt.Equals, 1)
}
