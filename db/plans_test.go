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
}
