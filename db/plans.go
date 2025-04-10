package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// nextPlanID internal method returns the next available subscription ID. If an error
// occurs, it returns the error.
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
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var planID uint64
	// Execute the operation within a transaction
	err := ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		nextID, err := ms.nextPlanID(sessCtx)
		if err != nil {
			return err
		}

		if plan.ID > 0 {
			if plan.ID >= nextID {
				return ErrInvalidData
			}
			updateDoc, err := dynamicUpdateDocument(plan, nil)
			if err != nil {
				return err
			}
			// Set upsert to true to create the document if it doesn't exist
			if _, err := ms.plans.UpdateOne(sessCtx, bson.M{"_id": plan.ID}, updateDoc); err != nil {
				return err
			}
			planID = plan.ID
		} else {
			plan.ID = nextID
			if _, err := ms.plans.InsertOne(sessCtx, plan); err != nil {
				return err
			}
			planID = nextID
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return planID, nil
}

// Plan method returns the plan with the given ID. If the
// plan doesn't exist, it returns the specific error.
func (ms *MongoStorage) Plan(planID uint64) (*Plan, error) {
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read operations don't need transactions, but we'll use a session for consistency
	session, err := ms.DBClient.StartSession()
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	var plan *Plan
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		// Find the plan in the database
		filter := bson.M{"_id": planID}
		plan = &Plan{}
		err := ms.plans.FindOne(sessCtx, filter).Decode(plan)
		if err != nil {
			if err == mongo.ErrNoDocuments {
				return ErrNotFound // Plan not found
			}
			return errors.New("failed to get plan")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return plan, nil
}

// PlanByStripeID method returns the plan with the given stripe ID. If the
// plan doesn't exist, it returns the specific error.
func (ms *MongoStorage) PlanByStripeID(stripeID string) (*Plan, error) {
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read operations don't need transactions, but we'll use a session for consistency
	session, err := ms.DBClient.StartSession()
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	var plan *Plan
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		// Find the plan in the database
		filter := bson.M{"stripeID": stripeID}
		plan = &Plan{}
		err := ms.plans.FindOne(sessCtx, filter).Decode(plan)
		if err != nil {
			if err == mongo.ErrNoDocuments {
				return ErrNotFound // Plan not found
			}
			return errors.New("failed to get plan")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return plan, nil
}

// DefaultPlan method returns the default plan plan. If the
// plan doesn't exist, it returns the specific error.
func (ms *MongoStorage) DefaultPlan() (*Plan, error) {
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read operations don't need transactions, but we'll use a session for consistency
	session, err := ms.DBClient.StartSession()
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	var plan *Plan
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		// Find the plan in the database
		filter := bson.M{"default": true}
		plan = &Plan{}
		err := ms.plans.FindOne(sessCtx, filter).Decode(plan)
		if err != nil {
			if err == mongo.ErrNoDocuments {
				return ErrNotFound // Plan not found
			}
			return errors.New("failed to get plan")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return plan, nil
}

// Plans method returns all plans from the database.
func (ms *MongoStorage) Plans() ([]*Plan, error) {
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read operations don't need transactions, but we'll use a session for consistency
	session, err := ms.DBClient.StartSession()
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	var plans []*Plan
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		// Find all plans in the database
		cursor, err := ms.plans.Find(sessCtx, bson.M{})
		if err != nil {
			return err
		}
		defer func() {
			if err := cursor.Close(sessCtx); err != nil {
				log.Warnw("failed to close plans cursor", "error", err)
			}
		}()

		// Iterate over the cursor and decode each plan
		plans = []*Plan{}
		for cursor.Next(sessCtx) {
			plan := &Plan{}
			if err := cursor.Decode(plan); err != nil {
				return err
			}
			plans = append(plans, plan)
		}
		return cursor.Err()
	})
	if err != nil {
		return nil, err
	}
	return plans, nil
}

// DelPlan method deletes the plan with the given ID. If the
// plan doesn't exist, it returns the specific error.
func (ms *MongoStorage) DelPlan(plan *Plan) error {
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Execute the operation within a transaction
	return ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		// Delete the plan from the database
		_, err := ms.plans.DeleteOne(sessCtx, bson.M{"_id": plan.ID})
		return err
	})
}
