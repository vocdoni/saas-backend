package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

func TestAnnualPeriodForYearlyPlan(t *testing.T) {
	c := qt.New(t)

	start := time.Date(2025, time.January, 15, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, time.January, 15, 0, 0, 0, 0, time.UTC)
	sub := OrganizationSubscription{
		StartDate:   start,
		RenewalDate: end,
	}

	gotStart, gotEnd, ok := ComputeAnnualPeriod(sub, BillingPeriodAnnual, time.Now())
	c.Assert(ok, qt.IsTrue)
	c.Assert(gotStart, qt.Equals, start)
	c.Assert(gotEnd, qt.Equals, end)
}

func TestAnnualPeriodForMonthlyPlan(t *testing.T) {
	c := qt.New(t)

	start := time.Date(2024, time.January, 15, 0, 0, 0, 0, time.UTC)
	sub := OrganizationSubscription{
		StartDate: start,
	}
	now := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)

	gotStart, gotEnd, ok := ComputeAnnualPeriod(sub, BillingPeriodMonthly, now)
	c.Assert(ok, qt.IsTrue)
	c.Assert(gotStart, qt.Equals, time.Date(2026, time.January, 15, 0, 0, 0, 0, time.UTC))
	c.Assert(gotEnd, qt.Equals, time.Date(2027, time.January, 15, 0, 0, 0, 0, time.UTC))
}
