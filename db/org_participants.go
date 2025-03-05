package db

import (
	"context"
	"fmt"
	"time"

	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// CreateOrgParticipants creates a new orgParticipants for an organization
// reqires an existing organization
func (ms *MongoStorage) SetOrgParticipant(salt string, orgParticipant *OrgParticipant) (string, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if len(orgParticipant.OrgAddress) == 0 {
		return "", ErrInvalidData
	}

	// check that the org exists
	_, _, err := ms.Organization(orgParticipant.OrgAddress, false)
	if err != nil {
		if err == ErrNotFound {
			return "", ErrInvalidData
		}
		return "", fmt.Errorf("organization not found: %w", err)
	}

	if orgParticipant.Email != "" {
		// store only the hashed email
		orgParticipant.HashedEmail = internal.HashOrgData(orgParticipant.OrgAddress, orgParticipant.Email)
		orgParticipant.Email = ""
	}
	if orgParticipant.Phone != "" {
		// store only the hashed phone
		orgParticipant.HashedPhone = internal.HashOrgData(orgParticipant.OrgAddress, orgParticipant.Phone)
		orgParticipant.Phone = ""
	}
	if orgParticipant.Password != "" {
		// store only the hashed password
		orgParticipant.HashedPass = internal.HashPassword(salt, orgParticipant.Password)
		orgParticipant.Password = ""
	}

	if orgParticipant.ID != primitive.NilObjectID {
		// if the orgParticipant exists, update it with the new data
		orgParticipant.UpdatedAt = time.Now()
	} else {
		// if the orgParticipant doesn't exist, create the corresponding id
		orgParticipant.ID = primitive.NewObjectID()
		orgParticipant.CreatedAt = time.Now()
	}
	updateDoc, err := dynamicUpdateDocument(orgParticipant, nil)
	if err != nil {
		return "", err
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	filter := bson.M{"_id": orgParticipant.ID}
	opts := options.Update().SetUpsert(true)
	_, err = ms.orgParticipants.UpdateOne(ctx, filter, updateDoc, opts)
	if err != nil {
		return "", err
	}

	return orgParticipant.ID.Hex(), nil
}

// DeleteOrgParticipants removes a orgParticipants and all its participants
func (ms *MongoStorage) DelOrgParticipant(id string) error {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// delete the orgParticipants from the database using the ID
	filter := bson.M{"_id": objID}
	_, err = ms.orgParticipants.DeleteOne(ctx, filter)
	return err
}

// OrgParticipants retrieves a orgParticipants from the DB based on it ID
func (ms *MongoStorage) OrgParticipant(id string) (*OrgParticipant, error) {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	orgParticipant := &OrgParticipant{}
	if err = ms.orgParticipants.FindOne(ctx, bson.M{"_id": objID}).Decode(orgParticipant); err != nil {
		return nil, fmt.Errorf("failed to get orgParticipants: %w", err)
	}

	return orgParticipant, nil
}

// OrgParticipants retrieves a orgParticipants from the DB based on it ID
func (ms *MongoStorage) OrgParticipantByNo(orgAddress, participantNo string) (*OrgParticipant, error) {
	if len(participantNo) == 0 {
		return nil, ErrInvalidData
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	orgParticipant := &OrgParticipant{}
	if err := ms.orgParticipants.FindOne(
		ctx, bson.M{"orgAddress": orgAddress, "participantNo": participantNo},
	).Decode(orgParticipant); err != nil {
		return nil, fmt.Errorf("failed to get orgParticipants: %w", err)
	}

	return orgParticipant, nil
}

// BulkAddOrgParticipants adds multiple census participants to the database in batches of 1000 entries
// and updates already existing participants (decided by combination of participantNo and the censusID)
// reqires an existing organization
func (ms *MongoStorage) BulkUpsertOrgParticipants(
	orgAddress, salt string,
	orgParticipants []OrgParticipant,
) (*mongo.BulkWriteResult, error) {
	if len(orgParticipants) == 0 {
		return nil, nil
	}
	if len(orgAddress) == 0 {
		return nil, ErrInvalidData
	}

	// Create a context with a timeout for checking organization existence
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Check that the organization exists
	org := ms.organizations.FindOne(ctx, bson.M{"_id": orgAddress})
	if org.Err() != nil {
		return nil, ErrNotFound
	}

	// Timestamp for all participants
	currentTime := time.Now()

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	// Process participants in batches of 1000
	batchSize := 1000
	var finalResult *mongo.BulkWriteResult

	for i := 0; i < len(orgParticipants); i += batchSize {
		// Calculate end index for current batch
		end := i + batchSize
		if end > len(orgParticipants) {
			end = len(orgParticipants)
		}

		// Create a new context for each batch
		batchCtx, batchCancel := context.WithTimeout(context.Background(), 10*time.Second)

		// Prepare bulk operations for this batch
		var bulkOps []mongo.WriteModel

		// Process current batch
		for _, participant := range orgParticipants[i:end] {
			filter := bson.M{
				"participantNo": participant.ParticipantNo,
				"orgAddress":    orgAddress,
			}
			participant.OrgAddress = orgAddress
			participant.CreatedAt = currentTime
			if participant.Email != "" {
				// store only the hashed email
				participant.HashedEmail = internal.HashOrgData(orgAddress, participant.Email)
				participant.Email = ""
			}
			if participant.Phone != "" {
				// store only the hashed phone
				participant.HashedPhone = internal.HashOrgData(orgAddress, participant.Phone)
				participant.Phone = ""
			}
			if participant.Password != "" {
				participant.HashedPass = internal.HashPassword(salt, participant.Password)
				participant.Password = ""
			}

			// Create the update document for the participant
			updateDoc, err := dynamicUpdateDocument(participant, nil)
			if err != nil {
				batchCancel()
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

		// Execute the bulk write operation for this batch
		result, err := ms.orgParticipants.BulkWrite(batchCtx, bulkOps)
		batchCancel()

		if err != nil {
			return nil, fmt.Errorf("failed to perform bulk operation: %w", err)
		}

		// Merge results if this is not the first batch
		if finalResult == nil {
			finalResult = result
		} else {
			finalResult.InsertedCount += result.InsertedCount
			finalResult.MatchedCount += result.MatchedCount
			finalResult.ModifiedCount += result.ModifiedCount
			finalResult.DeletedCount += result.DeletedCount
			finalResult.UpsertedCount += result.UpsertedCount

			// Merge the upserted IDs
			for k, v := range result.UpsertedIDs {
				finalResult.UpsertedIDs[k] = v
			}
		}
	}

	return finalResult, nil
}

// OrgParticipants retrieves a orgParticipants from the DB based on it ID
func (ms *MongoStorage) OrgParticipants(orgAddress string) ([]OrgParticipant, error) {
	if len(orgAddress) == 0 {
		return nil, ErrInvalidData
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cursor, err := ms.orgParticipants.Find(ctx, bson.M{"orgAddress": orgAddress})
	if err != nil {
		return nil, fmt.Errorf("failed to get orgParticipants: %w", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	var orgParticipants []OrgParticipant
	if err = cursor.All(ctx, &orgParticipants); err != nil {
		return nil, fmt.Errorf("failed to get orgParticipants: %w", err)
	}

	return orgParticipants, nil
}

func (ms *MongoStorage) OrgParticipantsMemberships(
	orgAddress, censusId, bundleId string, electionIds []internal.HexBytes,
) ([]CensusMembershipParticipant, error) {
	if len(orgAddress) == 0 || len(censusId) == 0 {
		return nil, ErrInvalidData
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Optimized aggregation pipeline
	pipeline := mongo.Pipeline{
		{primitive.E{Key: "$match", Value: bson.D{{Key: "orgAddress", Value: orgAddress}}}},
		{primitive.E{Key: "$lookup", Value: bson.D{
			{Key: "from", Value: "censusMemberships"},
			{Key: "localField", Value: "participantNo"},
			{Key: "foreignField", Value: "participantNo"},
			{Key: "as", Value: "membership"},
		}}},
		{primitive.E{Key: "$unwind", Value: bson.D{{Key: "path", Value: "$membership"}}}},
		{primitive.E{Key: "$match", Value: bson.D{{Key: "membership.censusId", Value: censusId}}}},
		{primitive.E{Key: "$addFields", Value: bson.D{
			{Key: "bundleId", Value: bundleId},
			{Key: "electionIds", Value: electionIds}, // Store extra fields as an array
		}}},
		{primitive.E{Key: "$project", Value: bson.D{
			{Key: "hashedEmail", Value: 1},
			{Key: "hashedPhone", Value: 1},
			{Key: "participantNo", Value: 1},
			{Key: "bundleId", Value: 1},
			{Key: "electionIds", Value: 1},
		}}},
	}

	cursor, err := ms.orgParticipants.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("failed to get orgParticipants: %w", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	// Convert cursor to slice of OrgParticipants
	var participants []CensusMembershipParticipant
	if err := cursor.All(ctx, &participants); err != nil {
		return nil, fmt.Errorf("failed to get orgParticipants: %w", err)
	}

	return participants, nil
}
