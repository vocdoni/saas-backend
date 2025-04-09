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

// SetOrgParticipant creates a new orgParticipant for an organization
// requires an existing organization
func (ms *MongoStorage) SetOrgParticipant(salt string, orgParticipant *OrgParticipant) (string, error) {
	if len(orgParticipant.OrgAddress) == 0 {
		return "", ErrInvalidData
	}

	// Check that the org exists before starting the transaction
	_, err := ms.Organization(orgParticipant.OrgAddress)
	if err != nil {
		if err == ErrNotFound {
			return "", ErrInvalidData
		}
		return "", fmt.Errorf("organization not found: %w", err)
	}

	// Process sensitive data
	if orgParticipant.Email != "" {
		// Store only the hashed email
		orgParticipant.HashedEmail = internal.HashOrgData(orgParticipant.OrgAddress, orgParticipant.Email)
		orgParticipant.Email = ""
	}
	if orgParticipant.Phone != "" {
		// Normalize and store only the hashed phone
		normalizedPhone, err := internal.SanitizeAndVerifyPhoneNumber(orgParticipant.Phone)
		if err == nil {
			orgParticipant.HashedPhone = internal.HashOrgData(orgParticipant.OrgAddress, normalizedPhone)
		}
		orgParticipant.Phone = ""
	}
	if orgParticipant.Password != "" {
		// Store only the hashed password
		orgParticipant.HashedPass = internal.HashPassword(salt, orgParticipant.Password)
		orgParticipant.Password = ""
	}

	// Set timestamps and ID
	if orgParticipant.ID != primitive.NilObjectID {
		// If the orgParticipant exists, update it with the new data
		orgParticipant.UpdatedAt = time.Now()
	} else {
		// If the orgParticipant doesn't exist, create the corresponding id
		orgParticipant.ID = primitive.NewObjectID()
		orgParticipant.CreatedAt = time.Now()
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Execute the operation within a transaction
	err = ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		updateDoc, err := dynamicUpdateDocument(orgParticipant, nil)
		if err != nil {
			return err
		}

		filter := bson.M{"_id": orgParticipant.ID}
		opts := options.Update().SetUpsert(true)
		_, err = ms.orgParticipants.UpdateOne(sessCtx, filter, updateDoc, opts)
		return err
	})
	if err != nil {
		return "", err
	}
	return orgParticipant.ID.Hex(), nil
}

// DelOrgParticipant removes an orgParticipant and all its participants
func (ms *MongoStorage) DelOrgParticipant(id string) error {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return ErrInvalidData
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Execute the operation within a transaction
	return ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		// Delete the orgParticipant from the database using the ID
		filter := bson.M{"_id": objID}
		_, err = ms.orgParticipants.DeleteOne(sessCtx, filter)
		return err
	})
}

// OrgParticipant retrieves an orgParticipant from the DB based on its ID
func (ms *MongoStorage) OrgParticipant(id string) (*OrgParticipant, error) {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, ErrInvalidData
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read operations don't need transactions, but we'll use a session for consistency
	session, err := ms.DBClient.StartSession()
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	var orgParticipant *OrgParticipant
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		orgParticipant = &OrgParticipant{}
		if err = ms.orgParticipants.FindOne(sessCtx, bson.M{"_id": objID}).Decode(orgParticipant); err != nil {
			return fmt.Errorf("failed to get orgParticipant: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return orgParticipant, nil
}

// OrgParticipantByNo retrieves an orgParticipant from the DB based on organization address and participant number
func (ms *MongoStorage) OrgParticipantByNo(orgAddress, participantNo string) (*OrgParticipant, error) {
	if len(participantNo) == 0 {
		return nil, ErrInvalidData
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read operations don't need transactions, but we'll use a session for consistency
	session, err := ms.DBClient.StartSession()
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	var orgParticipant *OrgParticipant
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		orgParticipant = &OrgParticipant{}
		if err := ms.orgParticipants.FindOne(
			sessCtx, bson.M{"orgAddress": orgAddress, "participantNo": participantNo},
		).Decode(orgParticipant); err != nil {
			return fmt.Errorf("failed to get orgParticipant: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return orgParticipant, nil
}

// BulkUpsertOrgParticipants adds multiple census participants to the database in batches of 1000 entries
// and updates already existing participants (decided by combination of participantNo and the censusID)
// requires an existing organization
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

	// Check that the organization exists before starting the transaction
	org := ms.organizations.FindOne(ctx, bson.M{"_id": orgAddress})
	if org.Err() != nil {
		return nil, ErrNotFound
	}

	// Timestamp for all participants
	currentTime := time.Now()

	// Process participants in batches of 1000
	batchSize := 1000
	var finalResult *mongo.BulkWriteResult

	// Execute each batch in its own transaction
	for i := 0; i < len(orgParticipants); i += batchSize {
		// Calculate end index for current batch
		end := i + batchSize
		if end > len(orgParticipants) {
			end = len(orgParticipants)
		}

		// Create a new context for each batch
		batchCtx, batchCancel := context.WithTimeout(context.Background(), 20*time.Second)

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
				// Store only the hashed email
				participant.HashedEmail = internal.HashOrgData(orgAddress, participant.Email)
				participant.Email = ""
			}
			if participant.Phone != "" {
				// Normalize and store only the hashed phone
				normalizedPhone, err := internal.SanitizeAndVerifyPhoneNumber(participant.Phone)
				if err == nil {
					participant.HashedPhone = internal.HashOrgData(orgAddress, normalizedPhone)
				}
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

		// Execute the bulk write operation for this batch within a transaction
		var result *mongo.BulkWriteResult
		err := ms.WithTransaction(batchCtx, func(sessCtx mongo.SessionContext) error {
			var err error
			result, err = ms.orgParticipants.BulkWrite(sessCtx, bulkOps)
			return err
		})

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

// OrgParticipants retrieves all orgParticipants for an organization from the DB
func (ms *MongoStorage) OrgParticipants(orgAddress string) ([]OrgParticipant, error) {
	if len(orgAddress) == 0 {
		return nil, ErrInvalidData
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read operations don't need transactions, but we'll use a session for consistency
	session, err := ms.DBClient.StartSession()
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	var orgParticipants []OrgParticipant
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		cursor, err := ms.orgParticipants.Find(sessCtx, bson.M{"orgAddress": orgAddress})
		if err != nil {
			return fmt.Errorf("failed to get orgParticipants: %w", err)
		}
		defer func() {
			if err := cursor.Close(sessCtx); err != nil {
				log.Warnw("error closing cursor", "error", err)
			}
		}()

		orgParticipants = []OrgParticipant{}
		if err = cursor.All(sessCtx, &orgParticipants); err != nil {
			return fmt.Errorf("failed to get orgParticipants: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return orgParticipants, nil
}

// OrgParticipantsMemberships retrieves participants with their census memberships
func (ms *MongoStorage) OrgParticipantsMemberships(
	orgAddress, censusID, bundleID string, electionIDs []internal.HexBytes,
) ([]CensusMembershipParticipant, error) {
	if len(orgAddress) == 0 || len(censusID) == 0 {
		return nil, ErrInvalidData
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read operations don't need transactions, but we'll use a session for consistency
	session, err := ms.DBClient.StartSession()
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	var participants []CensusMembershipParticipant
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
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
			{primitive.E{Key: "$match", Value: bson.D{{Key: "membership.censusId", Value: censusID}}}},
			{primitive.E{Key: "$addFields", Value: bson.D{
				{Key: "bundleId", Value: bundleID},
				{Key: "electionIds", Value: electionIDs}, // Store extra fields as an array
			}}},
			{primitive.E{Key: "$project", Value: bson.D{
				{Key: "hashedEmail", Value: 1},
				{Key: "hashedPhone", Value: 1},
				{Key: "participantNo", Value: 1},
				{Key: "bundleId", Value: 1},
				{Key: "electionIds", Value: 1},
			}}},
		}

		cursor, err := ms.orgParticipants.Aggregate(sessCtx, pipeline)
		if err != nil {
			return fmt.Errorf("failed to get orgParticipants: %w", err)
		}
		defer func() {
			if err := cursor.Close(sessCtx); err != nil {
				log.Warnw("error closing cursor", "error", err)
			}
		}()

		// Convert cursor to slice of CensusMembershipParticipant
		participants = []CensusMembershipParticipant{}
		if err := cursor.All(sessCtx, &participants); err != nil {
			return fmt.Errorf("failed to get orgParticipants: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return participants, nil
}
