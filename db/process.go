package db

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// CreateProcess creates a new process for an organization
func (ms *MongoStorage) SetProcess(process *Process) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := ms.processes.InsertOne(ctx, process); err != nil {
		return fmt.Errorf("failed to create process: %w", err)
	}

	return nil
}

// DeleteProcess removes a process and all its participants
func (ms *MongoStorage) DelProcess(processID string) error {
	objID, err := primitive.ObjectIDFromHex(processID)
	if err != nil {
		return ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// delete the process from the database using the ID
	filter := bson.M{"_id": objID}
	_, err = ms.processes.DeleteOne(ctx, filter)
	return err
}

// Process retrieves a process from the DB based on it ID
func (ms *MongoStorage) Process(processID string) (*Process, error) {
	objID, err := primitive.ObjectIDFromHex(processID)
	if err != nil {
		return nil, ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	process := &Process{}
	err = ms.processes.FindOne(ctx, bson.M{"_id": objID}).Decode(process)
	if err != nil {
		return nil, fmt.Errorf("failed to get process: %w", err)
	}

	return process, nil
}
