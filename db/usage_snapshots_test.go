package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

func TestUsageSnapshotUpsertAndFetch(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	periodStart := time.Now().UTC().Truncate(time.Second)
	periodEnd := periodStart.Add(365 * 24 * time.Hour)

	snapshot := &UsageSnapshot{
		OrgAddress:    testOrgAddress,
		PeriodStart:   periodStart,
		PeriodEnd:     periodEnd,
		BillingPeriod: BillingPeriodAnnual,
		Baseline: UsageSnapshotBaseline{
			Processes:  10,
			SentSMS:    5,
			SentEmails: 7,
		},
	}

	c.Assert(testDB.UpsertUsageSnapshot(snapshot), qt.IsNil)
	got, err := testDB.GetUsageSnapshot(testOrgAddress, periodStart)
	c.Assert(err, qt.IsNil)
	c.Assert(got.Baseline.Processes, qt.Equals, 10)
	c.Assert(got.Baseline.SentSMS, qt.Equals, 5)
	c.Assert(got.Baseline.SentEmails, qt.Equals, 7)

	// Upsert again with different baseline; should be idempotent.
	snapshot2 := &UsageSnapshot{
		OrgAddress:    testOrgAddress,
		PeriodStart:   periodStart,
		PeriodEnd:     periodEnd,
		BillingPeriod: BillingPeriodAnnual,
		Baseline: UsageSnapshotBaseline{
			Processes:  100,
			SentSMS:    50,
			SentEmails: 70,
		},
	}
	c.Assert(testDB.UpsertUsageSnapshot(snapshot2), qt.IsNil)
	got2, err := testDB.GetUsageSnapshot(testOrgAddress, periodStart)
	c.Assert(err, qt.IsNil)
	c.Assert(got2.Baseline.Processes, qt.Equals, 10)
	c.Assert(got2.Baseline.SentSMS, qt.Equals, 5)
	c.Assert(got2.Baseline.SentEmails, qt.Equals, 7)
}
