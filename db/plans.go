package db

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// nextPlanID internal method returns the next available subsbscription ID. If an error
// occurs, it returns the error. This method must be called with the keysLock
// held.
func (ms *MongoStorage) nextPlanID(ctx context.Context) (uint64, error) {
	var plan Plan
	opts := options.FindOne().SetSort(bson.D{{Key: "_id", Value: -1}})
	if err := ms.plans.FindOne(ctx, bson.M{}, opts).Decode(&plan); err != nil {
		if err == mongo.ErrNoDocuments {
			return 1, nil
		}
		return 0, err
	}
	return plan.ID + 1, nil
}

// SetPlan method creates or updates the plan in the database.
// If the plan already exists, it updates the fields that have changed.
func (ms *MongoStorage) SetPlan(plan *Plan) (uint64, error) {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nextID, err := ms.nextPlanID(ctx)
	if err != nil {
		return 0, err
	}
	if plan.ID > 0 {
		if plan.ID >= nextID {
			return 0, ErrInvalidData
		}
		updateDoc, err := dynamicUpdateDocument(plan, nil)
		if err != nil {
			return 0, err
		}
		// set upsert to true to create the document if it doesn't exist
		if _, err := ms.plans.UpdateOne(ctx, bson.M{"_id": plan.ID}, updateDoc); err != nil {
			return 0, err
		}
	} else {
		plan.ID = nextID
		if _, err := ms.plans.InsertOne(ctx, plan); err != nil {
			return 0, err
		}
	}
	return plan.ID, nil
}

// Plan method returns the plan with the given ID. If the
// plan doesn't exist, it returns the specific error.
func (ms *MongoStorage) Plan(planID uint64) (*Plan, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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

// PlanByStripeID method returns the plan with the given stripe ID. If the
// plan doesn't exist, it returns the specific error.
func (ms *MongoStorage) PlanByStripeID(stripeID string) (*Plan, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// find the plan in the database
	filter := bson.M{"stripeID": stripeID}
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
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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

// Plans method returns all plans from the database.
func (ms *MongoStorage) Plans() ([]*Plan, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// delete the organization from the database
	_, err := ms.plans.DeleteOne(ctx, bson.M{"_id": plan.ID})
	return err
}
