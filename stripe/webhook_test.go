package stripe

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v82"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/test"
)

func TestSubscriptionCreatesUsageSnapshot(t *testing.T) {
	c := qt.New(t)

	ctx := context.Background()
	dbContainer, err := test.StartMongoContainer(ctx)
	c.Assert(err, qt.IsNil)
	defer func() {
		c.Assert(dbContainer.Terminate(ctx), qt.IsNil)
	}()

	mongoURI, err := dbContainer.ConnectionString(ctx)
	c.Assert(err, qt.IsNil)

	testDB, err := db.New(mongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)
	defer testDB.Close()

	plan := &db.Plan{
		Name:     "test",
		StripeID: "prod_test",
		Organization: db.PlanLimits{
			MaxProcesses:  10,
			MaxSentEmails: 100,
			MaxSentSMS:    100,
		},
	}
	_, err = testDB.SetPlan(plan)
	c.Assert(err, qt.IsNil)

	orgAddress := common.Address{0x01, 0x23, 0x45, 0x67, 0x89}
	org := &db.Organization{
		Address: orgAddress,
		Counters: db.OrganizationCounters{
			Processes:  3,
			SentSMS:    2,
			SentEmails: 4,
		},
	}
	c.Assert(testDB.SetOrganization(org), qt.IsNil)

	service := &Service{
		client:      nil,
		db:          testDB,
		lockManager: NewLockManager(),
		config:      &Config{},
	}

	start := time.Date(2026, time.January, 15, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(1, 0, 0)
	subscriptionInfo := &SubscriptionInfo{
		ID:            "sub_1",
		Status:        stripeapi.SubscriptionStatusActive,
		BillingPeriod: db.BillingPeriodAnnual,
		ProductID:     plan.StripeID,
		OrgAddress:    orgAddress,
		Customer: &stripeapi.Customer{
			Email:    "test@example.com",
			Metadata: map[string]string{},
		},
		StartDate: start,
		EndDate:   end,
	}

	c.Assert(service.handleSubscriptionCreateOrUpdate(subscriptionInfo, org), qt.IsNil)

	snapshot, err := testDB.GetUsageSnapshot(orgAddress, start)
	c.Assert(err, qt.IsNil)
	c.Assert(snapshot.Baseline.Processes, qt.Equals, 3)
	c.Assert(snapshot.Baseline.SentSMS, qt.Equals, 2)
	c.Assert(snapshot.Baseline.SentEmails, qt.Equals, 4)
}
