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
			ID:   "prod_test",
			Name: "Test Plan",
		}
		c.Assert(testDB.SetPlan(plan), qt.IsNil)

		// a plan without an ID is rejected
		c.Assert(testDB.SetPlan(&Plan{Name: "No ID"}), qt.Equals, ErrInvalidData)
	})

	t.Run("GetPlan", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		// Test not found plan
		plan, err := testDB.Plan("prod_missing")
		c.Assert(err, qt.Equals, ErrNotFound)
		c.Assert(plan, qt.IsNil)

		plan = &Plan{
			ID:   "prod_test",
			Name: "Test Plan",
		}
		c.Assert(testDB.SetPlan(plan), qt.IsNil)

		// Test found plan
		planDB, err := testDB.Plan(plan.ID)
		c.Assert(err, qt.IsNil)
		c.Assert(planDB, qt.Not(qt.IsNil))
		c.Assert(planDB.ID, qt.Equals, plan.ID)
	})

	t.Run("DeletePlan", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		// Create a new plan and delete it
		plan := &Plan{
			ID:   "prod_test",
			Name: "Test Plan",
		}
		c.Assert(testDB.SetPlan(plan), qt.IsNil)

		c.Assert(testDB.DelPlan(plan), qt.IsNil)

		// Test not found plan
		_, err := testDB.Plan(plan.ID)
		c.Assert(err, qt.Equals, ErrNotFound)
	})

	t.Run("FreeIntegratorPlan", func(_ *testing.T) {
		// FreeIntegratorPlan selects a zero-priced plan that grants managed-org capacity. We seed
		// an explicit one and assert the selector returns it (robust to which matching plan FindOne
		// returns), plus non-matching plans that must never be selected.
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		// non-matching plans must never be selected
		c.Assert(testDB.SetPlan(&Plan{
			ID:               "prod_paid_integrator",
			Name:             "Paid Integrator",
			MonthlyPrice:     1000,
			YearlyPrice:      10000,
			IntegratorLimits: IntegratorLimits{MaxManagedOrgs: 10},
		}), qt.IsNil)
		c.Assert(testDB.SetPlan(&Plan{ID: "prod_free_basic", Name: "Free Basic"}), qt.IsNil)

		// a free integrator plan: zero-priced and grants managed-org capacity
		c.Assert(testDB.SetPlan(&Plan{
			ID:               "prod_free_integrator",
			Name:             "Free Integrator",
			IntegratorLimits: IntegratorLimits{MaxManagedOrgs: 1, MaxManagedProcesses: 5, MaxManagedCensusSize: 100},
		}), qt.IsNil)

		plan, err := testDB.FreeIntegratorPlan()
		c.Assert(err, qt.IsNil)
		c.Assert(plan.MonthlyPrice, qt.Equals, int64(0))
		c.Assert(plan.YearlyPrice, qt.Equals, int64(0))
		c.Assert(plan.IntegratorLimits.MaxManagedOrgs > 0, qt.IsTrue)
	})
}
