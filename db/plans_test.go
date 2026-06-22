package db

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestPlans(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	t.Run("SetPlan", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		plan := &Plan{
			Name:     "Test Plan",
			StripeID: "stripeID",
		}
		_, err := testDB.SetPlan(plan)
		c.Assert(err, qt.IsNil)
	})

	t.Run("GetPlan", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		planID := uint64(123)
		// Test not found plan
		plan, err := testDB.Plan(planID)
		c.Assert(err, qt.Equals, ErrNotFound)
		c.Assert(plan, qt.IsNil)

		plan = &Plan{
			Name:     "Test Plan",
			StripeID: "stripeID",
		}
		planID, err = testDB.SetPlan(plan)
		c.Assert(err, qt.IsNil)

		// Test found plan
		planDB, err := testDB.Plan(planID)
		c.Assert(err, qt.IsNil)
		c.Assert(planDB, qt.Not(qt.IsNil))
		c.Assert(planDB.ID, qt.Equals, plan.ID)
	})

	t.Run("DeletePlan", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		// Create a new plan and delete it
		plan := &Plan{
			Name:     "Test Plan",
			StripeID: "stripeID",
		}
		id, err := testDB.SetPlan(plan)
		c.Assert(err, qt.IsNil)

		err = testDB.DelPlan(plan)
		c.Assert(err, qt.IsNil)

		// Test not found plan
		_, err = testDB.Plan(id)
		c.Assert(err, qt.Equals, ErrNotFound)
	})

	t.Run("FreeIntegratorPlan", func(_ *testing.T) {
		// Note: DeleteAllDocuments intentionally preserves the `plans` collection, and migration
		// 0012 seeds a free integrator plan, so we don't assume an empty slate here. We seed an
		// explicit one too and assert the selector returns a zero-priced, managed-org-granting plan
		// (robust to which matching plan FindOne returns).
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		// non-matching plans must never be selected
		_, err := testDB.SetPlan(&Plan{
			Name:             "Paid Integrator",
			MonthlyPrice:     1000,
			YearlyPrice:      10000,
			IntegratorLimits: IntegratorLimits{MaxManagedOrgs: 10},
		})
		c.Assert(err, qt.IsNil)
		_, err = testDB.SetPlan(&Plan{Name: "Free Basic"})
		c.Assert(err, qt.IsNil)

		// a free integrator plan: zero-priced and grants managed-org capacity
		_, err = testDB.SetPlan(&Plan{
			Name:             "Free Integrator",
			IntegratorLimits: IntegratorLimits{MaxManagedOrgs: 1, MaxManagedProcesses: 5, MaxManagedCensusSize: 100},
		})
		c.Assert(err, qt.IsNil)

		plan, err := testDB.FreeIntegratorPlan()
		c.Assert(err, qt.IsNil)
		c.Assert(plan.MonthlyPrice, qt.Equals, int64(0))
		c.Assert(plan.YearlyPrice, qt.Equals, int64(0))
		c.Assert(plan.IntegratorLimits.MaxManagedOrgs > 0, qt.IsTrue)
	})
}
