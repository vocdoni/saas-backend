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
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		// nothing seeded yet
		_, err := testDB.FreeIntegratorPlan()
		c.Assert(err, qt.Equals, ErrNotFound)

		// a paid integrator plan must not be selected
		_, err = testDB.SetPlan(&Plan{
			Name:             "Paid Integrator",
			MonthlyPrice:     1000,
			YearlyPrice:      10000,
			IntegratorLimits: IntegratorLimits{MaxManagedOrgs: 10},
		})
		c.Assert(err, qt.IsNil)

		// a free non-integrator plan must not be selected
		_, err = testDB.SetPlan(&Plan{Name: "Free Basic"})
		c.Assert(err, qt.IsNil)

		// the free integrator plan: zero-priced and grants managed-org capacity
		freeID, err := testDB.SetPlan(&Plan{
			Name:             "Free Integrator",
			IntegratorLimits: IntegratorLimits{MaxManagedOrgs: 1, MaxManagedProcesses: 5, MaxManagedCensusSize: 100},
		})
		c.Assert(err, qt.IsNil)

		plan, err := testDB.FreeIntegratorPlan()
		c.Assert(err, qt.IsNil)
		c.Assert(plan.ID, qt.Equals, freeID)
		c.Assert(plan.IntegratorLimits.MaxManagedOrgs, qt.Equals, 1)
	})
}
