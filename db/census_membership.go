package db

import (
	"context"
	"fmt"
	"time"

	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.vocdoni.io/dvote/log"
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
func (ms *MongoStorage) CensusMembership(censusID, participantNo string) (*CensusMembership, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// validate input
	if len(participantNo) == 0 || len(censusID) == 0 {
		return nil, ErrInvalidData
	}

	// prepare filter for upsert
	filter := bson.M{
		"participantNo": participantNo,
		"censusId":      censusID,
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
func (ms *MongoStorage) DelCensusMembership(censusID, participantNo string) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// validate input
	if len(participantNo) == 0 || len(censusID) == 0 {
		return ErrInvalidData
	}

	// prepare filter for upsert
	filter := bson.M{
		"participantNo": participantNo,
		"censusId":      censusID,
	}

	// delete the membership
	_, err := ms.censusMemberships.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to delete census membership: %w", err)
	}

	return nil
}

// BulkCensusMembershipStatus is returned by SetBylkCensusMembership to provide the output.
type BulkCensusMembershipStatus struct {
	Progress int `json:"progress"`
	Total    int `json:"total"`
	Added    int `json:"added"`
}

// SetBulkCensusMembership creates or updates an org Participant and a census membership in the database.
// If the membership already exists (same participantNo and censusID), it updates it.
// If it doesn't exist, it creates a new one.
// Processes participants in batches of 200 entries.
// Returns a channel that sends the percentage of participants processed every 10 seconds.
// This function must be called in a goroutine.
func (ms *MongoStorage) SetBulkCensusMembership(
	salt, censusID string, orgParticipants []OrgParticipant,
) (chan *BulkCensusMembershipStatus, error) {
	progressChan := make(chan *BulkCensusMembershipStatus, 10)

	if len(orgParticipants) == 0 {
		close(progressChan)
		return progressChan, nil
	}
	if len(censusID) == 0 {
		close(progressChan)
		return progressChan, ErrInvalidData
	}

	// Use the context for database operations
	census, err := ms.Census(censusID)
	if err != nil {
		close(progressChan)
		return progressChan, fmt.Errorf("failed to get published census: %w", err)
	}

	if _, _, err := ms.Organization(census.OrgAddress, false); err != nil {
		close(progressChan)
		return progressChan, err
	}

	// Timestamp for all participants and memberships
	currentTime := time.Now()

	// Start a goroutine to process the participants and send progress updates
	go func() {
		defer close(progressChan)

		// Process participants in batches of 200
		batchSize := 200
		totalParticipants := len(orgParticipants)
		processedParticipants := 0
		addedParticipants := 0

		// Create a ticker to send progress updates every 10 seconds
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		// Send initial progress
		progressChan <- &BulkCensusMembershipStatus{
			Progress: 0,
			Total:    totalParticipants,
			Added:    addedParticipants,
		}

		// Create a context for the entire operation
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Start a goroutine to send progress updates
		go func() {
			for {
				select {
				case <-ticker.C:
					// Calculate and send progress percentage
					if totalParticipants > 0 {
						progress := (processedParticipants * 100) / totalParticipants
						progressChan <- &BulkCensusMembershipStatus{
							Progress: progress,
							Total:    totalParticipants,
							Added:    addedParticipants,
						}
					}
				case <-ctx.Done():
					return
				}
			}
		}()

		for i := 0; i < totalParticipants; i += batchSize {
			// Calculate end index for current batch
			end := i + batchSize
			if end > totalParticipants {
				end = totalParticipants
			}

			// Prepare bulk operations for this batch
			var bulkParticipantsOps []mongo.WriteModel
			var bulkMembershipOps []mongo.WriteModel

			// Process current batch
			for _, participant := range orgParticipants[i:end] {
				participantFilter := bson.M{
					"participantNo": participant.ParticipantNo,
					"orgAddress":    census.OrgAddress,
				}
				participant.OrgAddress = census.OrgAddress
				participant.CreatedAt = currentTime
				if participant.Email != "" && internal.ValidEmail(participant.Email) {
					// store only the hashed email
					participant.HashedEmail = internal.HashOrgData(census.OrgAddress, participant.Email)
					participant.Email = ""
				}
				if participant.Phone != "" {
					pn, err := internal.SanitizeAndVerifyPhoneNumber(participant.Phone)
					if err != nil {
						log.Warnw("invalid phone number", "phone", participant.Phone)
						participant.Phone = ""
					} else {
						// store only the hashed phone
						participant.HashedPhone = internal.HashOrgData(census.OrgAddress, pn)
						participant.Phone = ""
					}
				}
				if participant.Password != "" {
					participant.HashedPass = internal.HashPassword(salt, participant.Password)
					participant.Password = ""
				}

				// Create the update document for the participant
				updateParticipantsDoc, err := dynamicUpdateDocument(participant, nil)
				if err != nil {
					log.Warnw("failed to create update document for participant", "error", err, "participantNo", participant.ParticipantNo)
					continue // Skip this participant but continue with others
				}

				// Create the upsert model for the bulk operation
				upsertParticipantsModel := mongo.NewUpdateOneModel().
					SetFilter(participantFilter).     // AND condition filter
					SetUpdate(updateParticipantsDoc). // Update document
					SetUpsert(true)                   // Ensure upsert behavior

				// Add the operation to the bulkOps array
				bulkParticipantsOps = append(bulkParticipantsOps, upsertParticipantsModel)

				membershipFilter := bson.M{
					"participantNo": participant.ParticipantNo,
					"censusId":      censusID,
				}
				membershipDoc := &CensusMembership{
					ParticipantNo: participant.ParticipantNo,
					CensusID:      censusID,
					CreatedAt:     currentTime,
				}

				// Create the update document for the membership
				updateMembershipDoc, err := dynamicUpdateDocument(membershipDoc, nil)
				if err != nil {
					log.Warnw("failed to create update document for membership", "error", err, "participantNo", participant.ParticipantNo)
					continue // Skip this participant but continue with others
				}
				// Create the upsert model for the bulk operation
				upsertMembershipModel := mongo.NewUpdateOneModel().
					SetFilter(membershipFilter).    // AND condition filter
					SetUpdate(updateMembershipDoc). // Update document
					SetUpsert(true)                 // Ensure upsert behavior
				bulkMembershipOps = append(bulkMembershipOps, upsertMembershipModel)
			}

			// Only lock the mutex during the actual database operations
			ms.keysLock.Lock()

			// Create a new context for each batch
			batchCtx, batchCancel := context.WithTimeout(context.Background(), 20*time.Second)

			// Execute the bulk write operations for this batch
			_, err = ms.orgParticipants.BulkWrite(batchCtx, bulkParticipantsOps)
			if err != nil {
				log.Warnw("failed to perform bulk operation on participants", "error", err)
				// Continue anyway - we'll try to add the memberships
			} else {
				_, err := ms.censusMemberships.BulkWrite(batchCtx, bulkMembershipOps)
				if err != nil {
					log.Warnw("failed to perform bulk operation on memberships", "error", err)
					// Continue anyway - we've already processed some participants
				} else {
					// Only sum to addedParticipants if everything worked
					addedParticipants += len(bulkParticipantsOps)
				}
			}
			// Release the lock as soon as possible
			ms.keysLock.Unlock()

			batchCancel()

			// Update processed count
			processedParticipants += (end - i)
		}

		// Send final progress (100%)
		progressChan <- &BulkCensusMembershipStatus{
			Progress: 100,
			Total:    totalParticipants,
			Added:    addedParticipants,
		}
	}()

	return progressChan, nil
}

// CensusMemberships retrieves all the census memberships for a given census.
func (ms *MongoStorage) CensusMemberships(censusID string) ([]CensusMembership, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// validate input
	if len(censusID) == 0 {
		return nil, ErrInvalidData
	}

	// prepare filter for upsert
	filter := bson.M{
		"censusId": censusID,
	}

	// find the membership
	cursor, err := ms.censusMemberships.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to get census memberships: %w", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()
	var memberships []CensusMembership
	if err := cursor.All(ctx, &memberships); err != nil {
		return nil, fmt.Errorf("failed to get census memberships: %w", err)
	}

	return memberships, nil
}
