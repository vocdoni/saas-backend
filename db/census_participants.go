package db

import (
	"context"
	"fmt"
	"time"

	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// CreateCensusParticipants creates a new censusParticipants for an organization
func (ms *MongoStorage) SetCensusParticipant(censusParticipant *CensusParticipant) (string, error) {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if censusParticipant.ID != primitive.NilObjectID {
		// if the censusParticipant exists, update it with the new data
		updateDoc, err := dynamicUpdateDocument(censusParticipant, nil)
		if err != nil {
			return "", err
		}
		_, err = ms.censusParticipants.UpdateOne(ctx, bson.M{"_id": censusParticipant.ID}, updateDoc)
		if err != nil {
			return "", err
		}
	} else {
		// if the censusParticipant doesn't exist, create it
		censusParticipant.ID = primitive.NewObjectID()
		censusParticipant.CreatedAt = time.Now()
		if _, err := ms.censusParticipants.InsertOne(ctx, censusParticipant); err != nil {
			return "", fmt.Errorf("failed to create censusParticipant: %w", err)
		}
	}

	return censusParticipant.ID.String(), nil
}

// DeleteCensusParticipants removes a censusParticipants and all its participants
func (ms *MongoStorage) DelCensusParticipant(participantID string) error {
	objID, err := primitive.ObjectIDFromHex(participantID)
	if err != nil {
		return ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// delete the censusParticipants from the database using the ID
	filter := bson.M{"_id": objID}
	_, err = ms.censusParticipants.DeleteOne(ctx, filter)
	return err
}

// CensusParticipants retrieves a censusParticipants from the DB based on it ID
func (ms *MongoStorage) CensusParticipant(participantID string) (*CensusParticipant, error) {
	objID, err := primitive.ObjectIDFromHex(participantID)
	if err != nil {
		return nil, ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	censusParticipant := &CensusParticipant{}
	err = ms.censusParticipants.FindOne(ctx, bson.M{"_id": objID}).Decode(censusParticipant)
	if err != nil {
		return nil, fmt.Errorf("failed to get censusParticipants: %w", err)
	}

	return censusParticipant, nil
}

// BulkAddCensusParticipants adds multiple census participants to the database in a single operation
// and updates already existing participants (decided by combination of participantID and the censusID)
func (ms *MongoStorage) BulkUpsertCensusParticipants(
	orgAddress, censusId, salt string,
	censusParticipants []CensusParticipant,
) (*mongo.BulkWriteResult, error) {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dbCensusId, err := primitive.ObjectIDFromHex(censusId)
	if err != nil {
		return nil, fmt.Errorf("invalid census ID: %w", err)
	}
	time := time.Now()

	var bulkOps []mongo.WriteModel

	for _, participant := range censusParticipants {
		filter := bson.M{
			"participantId": participant.ParticipantID,
			"censusId":      participant.CensusID.Hex(),
		}
		participant.CensusID = dbCensusId
		participant.CreatedAt = time
		if participant.Email != "" {
			participant.HashedEmail = internal.HashOrgData(orgAddress, participant.Email)
		}
		if participant.Phone != "" {
			participant.HashedPhone = internal.HashOrgData(orgAddress, participant.Phone)
		}
		if participant.Password != "" {
			participant.HashedPass = internal.HashPassword(salt, participant.Password)
		}

		// Create the update document for the participant
		updateDoc, err := dynamicUpdateDocument(participant, nil)
		if err != nil {
			return nil, err
		}

		// Create the upsert model for the bulk operation
		upsertModel := mongo.NewUpdateOneModel().
			SetFilter(filter).    // AND condition filter
			SetUpdate(updateDoc). // Update document
			SetUpsert(true)       // Ensure upsert behavior

		// Add the operation to the bulkOps array
		bulkOps = append(bulkOps, upsertModel)
	}

	// Execute the bulk write operation
	result, err := ms.censusParticipants.BulkWrite(ctx, bulkOps)
	if err != nil {
		return nil, fmt.Errorf("failed to perform bulk operation: %w", err)
	}

	return result, nil
}
