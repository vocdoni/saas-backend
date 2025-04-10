package db

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
)

// WithTransaction executes the provided function within a MongoDB transaction.
// It handles starting, committing, and aborting the transaction as needed.
// The function passed should use the provided session context for all MongoDB operations.
func (ms *MongoStorage) WithTransaction(ctx context.Context, fn func(sessCtx mongo.SessionContext) error) error {
	// Create a session
	session, err := ms.DBClient.StartSession()
	if err != nil {
		return fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	// Define the transaction function
	txnFn := func(sessCtx mongo.SessionContext) (any, error) {
		// Execute the provided function within the transaction
		if err := fn(sessCtx); err != nil {
			return nil, err
		}
		return nil, nil
	}

	// Start the transaction with a timeout
	txnCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Execute the transaction
	_, err = session.WithTransaction(txnCtx, txnFn)
	if err != nil {
		return fmt.Errorf("transaction failed: %w", err)
	}

	return nil
}
