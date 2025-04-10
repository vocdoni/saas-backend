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

// validateCensusMembership validates that a census membership can be created
// by checking that the census exists, the organization exists, and the participant exists
func (ms *MongoStorage) validateCensusMembership(membership *CensusMembership) (string, error) {
	// validate required fields
	if len(membership.ParticipantNo) == 0 || len(membership.CensusID) == 0 {
		return "", ErrInvalidData
	}

	// check that the published census exists
	census, err := ms.Census(membership.CensusID)
	if err != nil {
		return "", fmt.Errorf("failed to get published census: %w", err)
	}

	// check that the org exists
	_, err = ms.Organization(census.OrgAddress)
	if err != nil {
		if err == ErrNotFound {
			return "", ErrInvalidData
		}
		return "", fmt.Errorf("organization not found: %w", err)
	}

	// check that the participant exists
	if _, err := ms.OrgParticipantByNo(census.OrgAddress, membership.ParticipantNo); err != nil {
		return "", fmt.Errorf("failed to get org participant: %w", err)
	}

	return census.OrgAddress, nil
}

// SetCensusMembership creates or updates a census membership in the database.
// If the membership already exists (same participantNo and censusID), it updates it.
// If it doesn't exist, it creates a new one.
func (ms *MongoStorage) SetCensusMembership(membership *CensusMembership) error {
	// Validate the membership before starting transaction
	_, err := ms.validateCensusMembership(membership)
	if err != nil {
		return err
	}

	// Set timestamps
	now := time.Now()
	membership.UpdatedAt = now
	if membership.CreatedAt.IsZero() {
		membership.CreatedAt = now
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Execute the operation within a transaction
	return ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		// Prepare filter for upsert
		filter := bson.M{
			"participantNo": membership.ParticipantNo,
			"censusId":      membership.CensusID,
		}

		// Create update document
		updateDoc := bson.M{
			"$set": membership,
		}

		opts := options.Update().SetUpsert(true)
		if _, err := ms.censusMemberships.UpdateOne(sessCtx, filter, updateDoc, opts); err != nil {
			return fmt.Errorf("failed to set census membership: %w", err)
		}

		return nil
	})
}

// CensusMembership retrieves a census membership from the database based on
// participantNo and censusID. Returns ErrNotFound if the membership doesn't exist.
func (ms *MongoStorage) CensusMembership(censusID, participantNo string) (*CensusMembership, error) {
	// Validate input
	if len(participantNo) == 0 || len(censusID) == 0 {
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

	var membership *CensusMembership
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		// Prepare filter
		filter := bson.M{
			"participantNo": participantNo,
			"censusId":      censusID,
		}

		// Find the membership
		membership = &CensusMembership{}
		err := ms.censusMemberships.FindOne(sessCtx, filter).Decode(membership)
		if err != nil {
			if err == mongo.ErrNoDocuments {
				return ErrNotFound
			}
			return fmt.Errorf("failed to get census membership: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return membership, nil
}

// DelCensusMembership removes a census membership from the database.
// Returns nil if the membership was successfully deleted or didn't exist.
func (ms *MongoStorage) DelCensusMembership(censusID, participantNo string) error {
	// Validate input
	if len(participantNo) == 0 || len(censusID) == 0 {
		return ErrInvalidData
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Execute the operation within a transaction
	return ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		// Prepare filter
		filter := bson.M{
			"participantNo": participantNo,
			"censusId":      censusID,
		}

		// Delete the membership
		_, err := ms.censusMemberships.DeleteOne(sessCtx, filter)
		if err != nil {
			return fmt.Errorf("failed to delete census membership: %w", err)
		}

		return nil
	})
}

// BulkCensusMembershipStatus is returned by SetBylkCensusMembership to provide the output.
type BulkCensusMembershipStatus struct {
	Progress int `json:"progress"`
	Total    int `json:"total"`
	Added    int `json:"added"`
}

// prepareParticipant processes a participant for storage by:
// - Setting the organization address
// - Setting the creation timestamp
// - Hashing sensitive data (email, phone, password)
// - Clearing the original sensitive data
func prepareParticipant(participant *OrgParticipant, orgAddress, salt string, currentTime time.Time) {
	participant.OrgAddress = orgAddress
	participant.CreatedAt = currentTime

	// Hash email if valid
	if participant.Email != "" && internal.ValidEmail(participant.Email) {
		participant.HashedEmail = internal.HashOrgData(orgAddress, participant.Email)
		participant.Email = ""
	}

	// Hash phone if valid
	if participant.Phone != "" {
		pn, err := internal.SanitizeAndVerifyPhoneNumber(participant.Phone)
		if err != nil {
			log.Warnw("invalid phone number", "phone", participant.Phone)
			participant.Phone = ""
		} else {
			participant.HashedPhone = internal.HashOrgData(orgAddress, pn)
			participant.Phone = ""
		}
	}

	// Hash password if present
	if participant.Password != "" {
		participant.HashedPass = internal.HashPassword(salt, participant.Password)
		participant.Password = ""
	}
}

// createBulkOperations creates the bulk write operations for participants and memberships
func createBulkOperations(
	participants []OrgParticipant,
	orgAddress string,
	censusID string,
	salt string,
	currentTime time.Time,
) (participantOps []mongo.WriteModel, membershipOps []mongo.WriteModel) {
	var bulkParticipantsOps []mongo.WriteModel
	var bulkMembershipOps []mongo.WriteModel

	for _, participant := range participants {
		// Prepare the participant
		prepareParticipant(&participant, orgAddress, salt, currentTime)

		// Create participant filter and update document
		participantFilter := bson.M{
			"participantNo": participant.ParticipantNo,
			"orgAddress":    orgAddress,
		}

		updateParticipantsDoc, err := dynamicUpdateDocument(participant, nil)
		if err != nil {
			log.Warnw("failed to create update document for participant",
				"error", err, "participantNo", participant.ParticipantNo)
			continue // Skip this participant but continue with others
		}

		// Create participant upsert model
		upsertParticipantsModel := mongo.NewUpdateOneModel().
			SetFilter(participantFilter).
			SetUpdate(updateParticipantsDoc).
			SetUpsert(true)
		bulkParticipantsOps = append(bulkParticipantsOps, upsertParticipantsModel)

		// Create membership filter and document
		membershipFilter := bson.M{
			"participantNo": participant.ParticipantNo,
			"censusId":      censusID,
		}
		membershipDoc := &CensusMembership{
			ParticipantNo: participant.ParticipantNo,
			CensusID:      censusID,
			CreatedAt:     currentTime,
		}

		// Create membership update document
		updateMembershipDoc, err := dynamicUpdateDocument(membershipDoc, nil)
		if err != nil {
			log.Warnw("failed to create update document for membership",
				"error", err, "participantNo", participant.ParticipantNo)
			continue
		}

		// Create membership upsert model
		upsertMembershipModel := mongo.NewUpdateOneModel().
			SetFilter(membershipFilter).
			SetUpdate(updateMembershipDoc).
			SetUpsert(true)
		bulkMembershipOps = append(bulkMembershipOps, upsertMembershipModel)
	}

	return bulkParticipantsOps, bulkMembershipOps
}

// processBatch processes a batch of participants and returns the number added
func (ms *MongoStorage) processBatch(
	sessCtx mongo.SessionContext,
	bulkParticipantsOps []mongo.WriteModel,
	bulkMembershipOps []mongo.WriteModel,
) int {
	if len(bulkParticipantsOps) == 0 {
		return 0
	}

	// Execute the bulk write operations for participants
	_, err := ms.orgParticipants.BulkWrite(sessCtx, bulkParticipantsOps)
	if err != nil {
		log.Warnw("failed to perform bulk operation on participants", "error", err)
		return 0
	}

	// Execute the bulk write operations for memberships
	_, err = ms.censusMemberships.BulkWrite(sessCtx, bulkMembershipOps)
	if err != nil {
		log.Warnw("failed to perform bulk operation on memberships", "error", err)
		return 0
	}

	return len(bulkParticipantsOps)
}

// startProgressReporter starts a goroutine that reports progress periodically
func startProgressReporter(
	ctx context.Context,
	progressChan chan<- *BulkCensusMembershipStatus,
	totalParticipants int,
	processedParticipants *int,
	addedParticipants *int,
) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Calculate and send progress percentage
			if totalParticipants > 0 {
				progress := (*processedParticipants * 100) / totalParticipants
				progressChan <- &BulkCensusMembershipStatus{
					Progress: progress,
					Total:    totalParticipants,
					Added:    *addedParticipants,
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// validateBulkCensusMembership validates the input parameters for bulk census membership
// and returns the census if valid
func (ms *MongoStorage) validateBulkCensusMembership(
	censusID string,
	orgParticipants []OrgParticipant,
) (*Census, error) {
	// Early returns for invalid input
	if len(orgParticipants) == 0 {
		return nil, nil // Not an error, just no work to do
	}
	if len(censusID) == 0 {
		return nil, ErrInvalidData
	}

	// Validate census and organization
	census, err := ms.Census(censusID)
	if err != nil {
		return nil, fmt.Errorf("failed to get published census: %w", err)
	}

	if _, err := ms.Organization(census.OrgAddress); err != nil {
		return nil, err
	}

	return census, nil
}

// processBatches processes participants in batches and sends progress updates
func (ms *MongoStorage) processBatches(
	orgParticipants []OrgParticipant,
	census *Census,
	censusID string,
	salt string,
	progressChan chan<- *BulkCensusMembershipStatus,
) {
	defer close(progressChan)

	// Process participants in batches of 200
	batchSize := 200
	totalParticipants := len(orgParticipants)
	processedParticipants := 0
	addedParticipants := 0
	currentTime := time.Now()

	// Send initial progress
	progressChan <- &BulkCensusMembershipStatus{
		Progress: 0,
		Total:    totalParticipants,
		Added:    addedParticipants,
	}

	// Create a context for the entire operation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start progress reporter in a separate goroutine
	go startProgressReporter(ctx, progressChan, totalParticipants, &processedParticipants, &addedParticipants)

	// Process participants in batches
	for i := 0; i < totalParticipants; i += batchSize {
		// Calculate end index for current batch
		end := i + batchSize
		if end > totalParticipants {
			end = totalParticipants
		}

		// Create bulk operations for this batch
		bulkParticipantsOps, bulkMembershipOps := createBulkOperations(
			orgParticipants[i:end],
			census.OrgAddress,
			censusID,
			salt,
			currentTime,
		)

		// Process the batch within a transaction
		batchCtx, batchCancel := context.WithTimeout(context.Background(), 20*time.Second)
		var added int

		// Use a transaction for each batch
		err := ms.WithTransaction(batchCtx, func(sessCtx mongo.SessionContext) error {
			added = ms.processBatch(sessCtx, bulkParticipantsOps, bulkMembershipOps)
			return nil
		})

		batchCancel()

		if err != nil {
			log.Warnw("failed to process batch", "error", err)
		} else {
			addedParticipants += added
		}

		// Update processed count
		processedParticipants += (end - i)
	}

	// Send final progress (100%)
	progressChan <- &BulkCensusMembershipStatus{
		Progress: 100,
		Total:    totalParticipants,
		Added:    addedParticipants,
	}
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

	// Validate input parameters
	census, err := ms.validateBulkCensusMembership(censusID, orgParticipants)
	if err != nil {
		close(progressChan)
		return progressChan, err
	}

	// If no participants, return empty channel
	if census == nil {
		close(progressChan)
		return progressChan, nil
	}

	// Start processing in a goroutine
	go ms.processBatches(orgParticipants, census, censusID, salt, progressChan)

	return progressChan, nil
}

// CensusMemberships retrieves all the census memberships for a given census.
func (ms *MongoStorage) CensusMemberships(censusID string) ([]CensusMembership, error) {
	// Validate input
	if len(censusID) == 0 {
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

	var memberships []CensusMembership
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		// Prepare filter
		filter := bson.M{
			"censusId": censusID,
		}

		// Find the memberships
		cursor, err := ms.censusMemberships.Find(sessCtx, filter)
		if err != nil {
			return fmt.Errorf("failed to get census memberships: %w", err)
		}
		defer func() {
			if err := cursor.Close(sessCtx); err != nil {
				log.Warnw("error closing cursor", "error", err)
			}
		}()

		memberships = []CensusMembership{}
		if err := cursor.All(sessCtx, &memberships); err != nil {
			return fmt.Errorf("failed to get census memberships: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return memberships, nil
}
