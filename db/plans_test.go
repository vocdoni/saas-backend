package db

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestPlans(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.Reset(), qt.IsNil) })
	t.Run("SetPlan", func(t *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)

		plan := &Plan{
			Name:     "Test Plan",
			StripeID: "stripeID",
		}
		_, err := testDB.SetPlan(plan)
		c.Assert(err, qt.IsNil)
	})

	t.Run("GetPlan", func(t *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)

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

	t.Run("DeletePlan", func(t *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)

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
}
