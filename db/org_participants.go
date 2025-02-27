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

// BulkAddOrgParticipants adds multiple census participants to the database in a single operation
// and updates already existing participants (decided by combination of participantNo and the censusID)
// reqires an existing organization
func (ms *MongoStorage) BulkUpsertOrgParticipants(
	orgAddress, salt string,
	orgParticipants []OrgParticipant,
) (*mongo.BulkWriteResult, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if len(orgParticipants) == 0 {
		return nil, nil
	}
	if len(orgAddress) == 0 {
		return nil, ErrInvalidData
	}
	org := ms.organizations.FindOne(ctx, bson.M{"_id": orgAddress})
	if org.Err() != nil {
		return nil, ErrNotFound
	}

	time := time.Now()

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	var bulkOps []mongo.WriteModel

	for _, participant := range orgParticipants {
		filter := bson.M{
			"participantNo": participant.ParticipantNo,
			"orgAddress":    orgAddress,
		}
		participant.OrgAddress = orgAddress
		participant.CreatedAt = time
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
	result, err := ms.orgParticipants.BulkWrite(ctx, bulkOps)
	if err != nil {
		return nil, fmt.Errorf("failed to perform bulk operation: %w", err)
	}

	return result, nil
}
