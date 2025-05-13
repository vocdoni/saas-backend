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
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	if len(orgParticipant.OrgAddress) == 0 {
		return "", ErrInvalidData
	}

	// check that the org exists
	_, err := ms.Organization(orgParticipant.OrgAddress)
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
		// normalize and store only the hashed phone
		normalizedPhone, err := internal.SanitizeAndVerifyPhoneNumber(orgParticipant.Phone)
		if err == nil {
			orgParticipant.HashedPhone = internal.HashOrgData(orgParticipant.OrgAddress, normalizedPhone)
		}
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
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// delete the orgParticipants from the database using the ID
	filter := bson.M{"_id": objID}
	_, err = ms.orgParticipants.DeleteOne(ctx, filter)
	return err
}

// OrgParticipant retrieves a orgParticipant from the DB based on it ID
func (ms *MongoStorage) OrgParticipant(id string) (*OrgParticipant, error) {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, ErrInvalidData
	}

	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	orgParticipant := &OrgParticipant{}
	if err = ms.orgParticipants.FindOne(ctx, bson.M{"_id": objID}).Decode(orgParticipant); err != nil {
		return nil, fmt.Errorf("failed to get orgParticipants: %w", err)
	}

	return orgParticipant, nil
}

// OrgParticipantByNo retrieves a orgParticipant from the DB based on organization address and participant number
func (ms *MongoStorage) OrgParticipantByNo(orgAddress, participantNo string) (*OrgParticipant, error) {
	if len(participantNo) == 0 {
		return nil, ErrInvalidData
	}
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	orgParticipant := &OrgParticipant{}
	if err := ms.orgParticipants.FindOne(
		ctx, bson.M{"orgAddress": orgAddress, "participantNo": participantNo},
	).Decode(orgParticipant); err != nil {
		return nil, fmt.Errorf("failed to get orgParticipants: %w", err)
	}

	return orgParticipant, nil
}

// BulkOrgParticipantsStatus is returned by SetBulkOrgParticipants to provide the output.
type BulkOrgParticipantsStatus struct {
	Progress int `json:"progress"`
	Total    int `json:"total"`
	Added    int `json:"added"`
}

// validateBulkOrgParticipants validates the input parameters for bulk org participants
func (ms *MongoStorage) validateBulkOrgParticipants(
	orgAddress string,
	orgParticipants []OrgParticipant,
) (*Organization, error) {
	// Early returns for invalid input
	if len(orgParticipants) == 0 {
		return nil, nil // Not an error, just no work to do
	}
	if len(orgAddress) == 0 {
		return nil, ErrInvalidData
	}

	// Check that the organization exists
	org, err := ms.Organization(orgAddress)
	if err != nil {
		return nil, err
	}

	return org, nil
}

// prepareOrgParticipant processes a participant for storage
func prepareOrgParticipant(participant *OrgParticipant, orgAddress, salt string, currentTime time.Time) {
	participant.OrgAddress = orgAddress
	participant.CreatedAt = currentTime

	// Hash email if valid
	if participant.Email != "" {
		participant.HashedEmail = internal.HashOrgData(orgAddress, participant.Email)
		participant.Email = ""
	}

	// Hash phone if valid
	if participant.Phone != "" {
		normalizedPhone, err := internal.SanitizeAndVerifyPhoneNumber(participant.Phone)
		if err == nil {
			participant.HashedPhone = internal.HashOrgData(orgAddress, normalizedPhone)
		}
		participant.Phone = ""
	}

	// Hash password if present
	if participant.Password != "" {
		participant.HashedPass = internal.HashPassword(salt, participant.Password)
		participant.Password = ""
	}
}

// createOrgParticipantBulkOperations creates the bulk write operations for participants
func createOrgParticipantBulkOperations(
	participants []OrgParticipant,
	orgAddress string,
	salt string,
	currentTime time.Time,
) []mongo.WriteModel {
	var bulkOps []mongo.WriteModel

	for _, participant := range participants {
		// Prepare the participant
		prepareOrgParticipant(&participant, orgAddress, salt, currentTime)

		// Create filter and update document
		filter := bson.M{
			"participantNo": participant.ParticipantNo,
			"orgAddress":    orgAddress,
		}

		updateDoc, err := dynamicUpdateDocument(participant, nil)
		if err != nil {
			log.Warnw("failed to create update document for participant",
				"error", err, "participantNo", participant.ParticipantNo)
			continue // Skip this participant but continue with others
		}

		// Create upsert model
		upsertModel := mongo.NewUpdateOneModel().
			SetFilter(filter).
			SetUpdate(updateDoc).
			SetUpsert(true)
		bulkOps = append(bulkOps, upsertModel)
	}

	return bulkOps
}

// processOrgParticipantBatch processes a batch of participants and returns the number added
func (ms *MongoStorage) processOrgParticipantBatch(
	bulkOps []mongo.WriteModel,
) int {
	if len(bulkOps) == 0 {
		return 0
	}

	// Only lock the mutex during the actual database operations
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	// Create a new context for the batch
	batchCtx, batchCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer batchCancel()

	// Execute the bulk write operations
	_, err := ms.orgParticipants.BulkWrite(batchCtx, bulkOps)
	if err != nil {
		log.Warnw("failed to perform bulk operation on participants", "error", err)
		return 0
	}

	return len(bulkOps)
}

// startOrgParticipantProgressReporter starts a goroutine that reports progress periodically
func startOrgParticipantProgressReporter(
	ctx context.Context,
	progressChan chan<- *BulkOrgParticipantsStatus,
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
				progressChan <- &BulkOrgParticipantsStatus{
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

// processOrgParticipantBatches processes participants in batches and sends progress updates
func (ms *MongoStorage) processOrgParticipantBatches(
	orgParticipants []OrgParticipant,
	orgAddress string,
	salt string,
	progressChan chan<- *BulkOrgParticipantsStatus,
) {
	defer close(progressChan)

	// Process participants in batches of 200
	batchSize := 200
	totalParticipants := len(orgParticipants)
	processedParticipants := 0
	addedParticipants := 0
	currentTime := time.Now()

	// Send initial progress
	progressChan <- &BulkOrgParticipantsStatus{
		Progress: 0,
		Total:    totalParticipants,
		Added:    addedParticipants,
	}

	// Create a context for the entire operation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start progress reporter in a separate goroutine
	go startOrgParticipantProgressReporter(
		ctx,
		progressChan,
		totalParticipants,
		&processedParticipants,
		&addedParticipants,
	)

	// Process participants in batches
	for i := 0; i < totalParticipants; i += batchSize {
		// Calculate end index for current batch
		end := i + batchSize
		if end > totalParticipants {
			end = totalParticipants
		}

		// Create bulk operations for this batch
		bulkOps := createOrgParticipantBulkOperations(
			orgParticipants[i:end],
			orgAddress,
			salt,
			currentTime,
		)

		// Process the batch and get number of added participants
		added := ms.processOrgParticipantBatch(bulkOps)
		addedParticipants += added

		// Update processed count
		processedParticipants += (end - i)
	}

	// Send final progress (100%)
	progressChan <- &BulkOrgParticipantsStatus{
		Progress: 100,
		Total:    totalParticipants,
		Added:    addedParticipants,
	}
}

// SetBulkOrgParticipants adds multiple organization participants to the database in batches of 200 entries
// and updates already existing participants (decided by combination of participantNo and orgAddress)
// Requires an existing organization
// Returns a channel that sends the percentage of participants processed every 10 seconds.
// This function must be called in a goroutine.
func (ms *MongoStorage) SetBulkOrgParticipants(
	orgAddress, salt string,
	orgParticipants []OrgParticipant,
) (chan *BulkOrgParticipantsStatus, error) {
	progressChan := make(chan *BulkOrgParticipantsStatus, 10)

	// Validate input parameters
	org, err := ms.validateBulkOrgParticipants(orgAddress, orgParticipants)
	if err != nil {
		close(progressChan)
		return progressChan, err
	}

	// If no participants, return empty channel
	if org == nil {
		close(progressChan)
		return progressChan, nil
	}

	// Start processing in a goroutine
	go ms.processOrgParticipantBatches(orgParticipants, orgAddress, salt, progressChan)

	return progressChan, nil
}

// OrgParticipantsWithPagination retrieves paginated orgParticipants for an organization from the DB
func (ms *MongoStorage) OrgParticipants(orgAddress string, page, pageSize int) ([]OrgParticipant, error) {
	if len(orgAddress) == 0 {
		return nil, ErrInvalidData
	}
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Calculate skip value based on page and pageSize
	skip := (page - 1) * pageSize

	// Create filter
	filter := bson.M{"orgAddress": orgAddress}

	// Set up options for pagination
	findOptions := options.Find().
		SetSkip(int64(skip)).
		SetLimit(int64(pageSize))

	// Execute the find operation with pagination
	cursor, err := ms.orgParticipants.Find(ctx, filter, findOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to get orgParticipants: %w", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	// Decode results
	var orgParticipants []OrgParticipant
	if err = cursor.All(ctx, &orgParticipants); err != nil {
		return nil, fmt.Errorf("failed to decode orgParticipants: %w", err)
	}

	return orgParticipants, nil
}

func (ms *MongoStorage) DeleteOrgParticipants(orgAddress string, participantIDs []string) (int, error) {
	if len(orgAddress) == 0 {
		return 0, ErrInvalidData
	}
	if len(participantIDs) == 0 {
		return 0, nil
	}

	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// create the filter for the delete operation
	filter := bson.M{
		"orgAddress": orgAddress,
		"participantNo": bson.M{
			"$in": participantIDs,
		},
	}

	result, err := ms.orgParticipants.DeleteMany(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("failed to delete orgParticipants: %w", err)
	}

	return int(result.DeletedCount), nil
}
