package db

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestSetPlan(t *testing.T) {
	c := qt.New(t)
	defer resetDB(c)

	plan := &Plan{
		Name:     "Test Plan",
		StripeID: "stripeID",
	}
	_, err := db.SetPlan(plan)
	c.Assert(err, qt.IsNil)
}

func TestPlan(t *testing.T) {
	c := qt.New(t) // Create a new quicktest instance
	defer resetDB(c)
	planID := uint64(123)
	// Test not found plan
	plan, err := db.Plan(planID)
	c.Assert(err, qt.Equals, ErrNotFound)
	c.Assert(plan, qt.IsNil)
	plan = &Plan{
		Name:     "Test Plan",
		StripeID: "stripeID",
	}
	planID, err = db.SetPlan(plan)
	if err != nil {
		t.Error(err)
	}
	// Test found plan
	planDB, err := db.Plan(planID)
	c.Assert(err, qt.IsNil)
	c.Assert(planDB, qt.Not(qt.IsNil))
	c.Assert(planDB.ID, qt.Equals, plan.ID)
}

func TestDelPlan(t *testing.T) {
	c := qt.New(t)
	defer resetDB(c)
	// Create a new plan and delete it
	plan := &Plan{
		Name:     "Test Plan",
		StripeID: "stripeID",
	}
	id, err := db.SetPlan(plan)
	c.Assert(err, qt.IsNil)
	err = db.DelPlan(plan)
	c.Assert(err, qt.IsNil)

	// Test not found plan
	_, err = db.Plan(id)
	c.Assert(err, qt.Equals, ErrNotFound)
}
