package db

import (
	"context"
	"fmt"
	"time"

	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// SetCensusMembership creates or updates a census membership in the database.
// If the membership already exists (same participantNo and censusID), it updates it.
// If it doesn't exist, it creates a new one.
func (ms *MongoStorage) SetCensusMembership(membership *CensusMembership) error {
	// validate required fields
	if len(membership.ParticipantNo) == 0 || len(membership.CensusID) == 0 {
		return ErrInvalidData
	}

	// check that the published census exists
	census, err := ms.Census(membership.CensusID)
	if err != nil {
		return fmt.Errorf("failed to get published census: %w", err)
	}
	// check that the org exists
	_, _, err = ms.Organization(census.OrgAddress, false)
	if err != nil {
		if err == ErrNotFound {
			return ErrInvalidData
		}
		return fmt.Errorf("organization not found: %w", err)
	}
	// check that the participant exists
	if _, err := ms.OrgParticipantByNo(census.OrgAddress, membership.ParticipantNo); err != nil {
		return fmt.Errorf("failed to get org participant: %w", err)
	}
	// prepare filter for upsert
	filter := bson.M{
		"participantNo": membership.ParticipantNo,
		"censusId":      membership.CensusID,
	}

	// set timestamps
	now := time.Now()
	membership.UpdatedAt = now
	if membership.CreatedAt.IsZero() {
		membership.CreatedAt = now
	}

	// create update document
	updateDoc := bson.M{
		"$set": membership,
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// perform upsert operation
	opts := options.Update().SetUpsert(true)
	if _, err := ms.censusMemberships.UpdateOne(ctx, filter, updateDoc, opts); err != nil {
		return fmt.Errorf("failed to set census membership: %w", err)
	}

	return nil
}

// CensusMembership retrieves a census membership from the database based on
// participantNo and censusID. Returns ErrNotFound if the membership doesn't exist.
func (ms *MongoStorage) CensusMembership(censusId, participantNo string) (*CensusMembership, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// validate input
	if len(participantNo) == 0 || len(censusId) == 0 {
		return nil, ErrInvalidData
	}

	// prepare filter for upsert
	filter := bson.M{
		"participantNo": participantNo,
		"censusId":      censusId,
	}

	// find the membership
	membership := &CensusMembership{}
	err := ms.censusMemberships.FindOne(ctx, filter).Decode(membership)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get census membership: %w", err)
	}

	return membership, nil
}

// DelCensusMembership removes a census membership from the database.
// Returns nil if the membership was successfully deleted or didn't exist.
func (ms *MongoStorage) DelCensusMembership(censusId, participantNo string) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// validate input
	if len(participantNo) == 0 || len(censusId) == 0 {
		return ErrInvalidData
	}

	// prepare filter for upsert
	filter := bson.M{
		"participantNo": participantNo,
		"censusId":      censusId,
	}

	// delete the membership
	_, err := ms.censusMemberships.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to delete census membership: %w", err)
	}

	return nil
}

// SetBulkCensusMembership creates or updates an org Participant and a census membership in the database.
// If the membership already exists (same participantNo and censusID), it updates it.
// If it doesn't exist, it creates a new one.
func (ms *MongoStorage) SetBulkCensusMembership(
	salt, censusId string, orgParticipants []OrgParticipant,
) (*mongo.BulkWriteResult, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if len(orgParticipants) == 0 {
		return nil, nil
	}
	if len(censusId) == 0 {
		return nil, ErrInvalidData
	}

	census, err := ms.Census(censusId)
	if err != nil {
		return nil, fmt.Errorf("failed to get published census: %w", err)
	}

	org := ms.organizations.FindOne(ctx, bson.M{"_id": census.OrgAddress})
	if org.Err() != nil {
		return nil, ErrNotFound
	}

	time := time.Now()

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	var bulkParticipantsOps []mongo.WriteModel
	var bulkMemebrshipOps []mongo.WriteModel

	for _, participant := range orgParticipants {
		participantFilter := bson.M{
			"participantNo": participant.ParticipantNo,
			"orgAddress":    census.OrgAddress,
		}
		participant.OrgAddress = census.OrgAddress
		participant.CreatedAt = time
		if participant.Email != "" {
			// store only the hashed email
			participant.HashedEmail = internal.HashOrgData(census.OrgAddress, participant.Email)
			participant.Email = ""
		}
		if participant.Phone != "" {
			// store only the hashed phone
			participant.HashedPhone = internal.HashOrgData(census.OrgAddress, participant.Phone)
			participant.Phone = ""
		}
		if participant.Password != "" {
			participant.HashedPass = internal.HashPassword(salt, participant.Password)
			participant.Password = ""
		}

		// Create the update document for the participant
		updateParticipantsDoc, err := dynamicUpdateDocument(participant, nil)
		if err != nil {
			return nil, err
		}

		// Create the upsert model for the bulk operation
		upsertParticipansModel := mongo.NewUpdateOneModel().
			SetFilter(participantFilter).     // AND condition filter
			SetUpdate(updateParticipantsDoc). // Update document
			SetUpsert(true)                   // Ensure upsert behavior

		// Add the operation to the bulkOps array
		bulkParticipantsOps = append(bulkParticipantsOps, upsertParticipansModel)

		membershipFilter := bson.M{
			"participantNo": participant.ParticipantNo,
			"censusId":      censusId,
		}
		membershipDoc := &CensusMembership{
			ParticipantNo: participant.ParticipantNo,
			CensusID:      censusId,
			CreatedAt:     time,
		}

		// Create the update document for the membership
		updateMembershipDoc, err := dynamicUpdateDocument(membershipDoc, nil)
		if err != nil {
			return nil, err
		}
		// Create the upsert model for the bulk operation
		upsertMembershipModel := mongo.NewUpdateOneModel().
			SetFilter(membershipFilter).    // AND condition filter
			SetUpdate(updateMembershipDoc). // Update document
			SetUpsert(true)                 // Ensure upsert behavior
		bulkMemebrshipOps = append(bulkMemebrshipOps, upsertMembershipModel)

	}

	// Execute the bulk write operations
	_, err = ms.orgParticipants.BulkWrite(ctx, bulkParticipantsOps)
	if err != nil {
		return nil, fmt.Errorf("failed to perform bulk operation: %w", err)
	}
	resultMemb, err := ms.censusMemberships.BulkWrite(ctx, bulkMemebrshipOps)
	if err != nil {
		return nil, fmt.Errorf("failed to perform bulk operation: %w", err)
	}

	return resultMemb, nil
}
