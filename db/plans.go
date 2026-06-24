package db

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// SetPlan creates or replaces the plan, keyed by its Stripe product ID (plan.ID). Plans are
// synced from Stripe rather than authored locally: the caller always holds the complete plan,
// so the whole document is replaced (a full ReplaceOne upsert, not a partial update). This
// matters because zero-valued fields are meaningful here — e.g. a free plan's monthlyPrice/
// yearlyPrice of 0 must be persisted so selectors like FreeIntegratorPlan can match on them.
// An empty ID is rejected.
func (ms *MongoStorage) SetPlan(plan *Plan) error {
	if plan.ID == "" {
		return ErrInvalidData
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.plans.ReplaceOne(ctx, bson.M{"_id": plan.ID}, plan, options.Replace().SetUpsert(true)); err != nil {
		return err
	}
	return nil
}

// Plan method returns the plan with the given ID (its Stripe product ID). If the
// plan doesn't exist, it returns the specific error.
func (ms *MongoStorage) Plan(planID string) (*Plan, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// find the plan in the database
	filter := bson.M{"_id": planID}
	plan := &Plan{}
	err := ms.plans.FindOne(ctx, filter).Decode(plan)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound // Plan not found
		}
		return nil, errors.New("failed to get plan")
	}
	return plan, nil
}

// DefaultPlan method returns the default plan plan. If the
// plan doesn't exist, it returns the specific error.
func (ms *MongoStorage) DefaultPlan() (*Plan, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// find the plan in the database
	filter := bson.M{"default": true}
	plan := &Plan{}
	err := ms.plans.FindOne(ctx, filter).Decode(plan)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound // Plan not found
		}
		return nil, errors.New("failed to get plan")
	}
	return plan, nil
}

// FreeIntegratorPlan returns the free integrator plan: a zero-priced plan that grants
// managed-organization capacity. It is the plan assigned to organizations created through
// the integrator portal so they become integrators on the free tier without a checkout.
func (ms *MongoStorage) FreeIntegratorPlan() (*Plan, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// a free plan (no recurring price) that grants at least one managed org
	filter := bson.M{
		"integratorLimits.maxManagedOrgs": bson.M{"$gt": 0},
		"monthlyPrice":                    int64(0),
		"yearlyPrice":                     int64(0),
	}
	plan := &Plan{}
	err := ms.plans.FindOne(ctx, filter).Decode(plan)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, errors.New("failed to get free integrator plan")
	}
	return plan, nil
}

// Plans method returns all plans from the database.
func (ms *MongoStorage) Plans() ([]*Plan, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// find all plans in the database
	cursor, err := ms.plans.Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("failed to close plans file", "error", err)
		}
	}()

	// iterate over the cursor and decode each plan
	var plans []*Plan
	for cursor.Next(ctx) {
		plan := &Plan{}
		if err := cursor.Decode(plan); err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	if err := cursor.Err(); err != nil {
		return nil, err
	}
	return plans, nil
}

// DelPlan method deletes the plan with the given ID. If the
// plan doesn't exist, it returns the specific error.
func (ms *MongoStorage) DelPlan(plan *Plan) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// delete the organization from the database
	_, err := ms.plans.DeleteOne(ctx, bson.M{"_id": plan.ID})
	return err
}
