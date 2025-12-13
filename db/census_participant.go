package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// validateCensusParticipant validates that a census participant can be created
// by checking that the census exists, the organization exists, and the member exists
func (ms *MongoStorage) validateCensusParticipant(participant *CensusParticipant) (common.Address, error) {
	// validate required fields
	if len(participant.ParticipantID) == 0 || len(participant.CensusID) == 0 {
		return common.Address{}, ErrInvalidData
	}

	// check that the published census exists
	census, err := ms.Census(participant.CensusID)
	if err != nil {
		return common.Address{}, fmt.Errorf("failed to get published census: %w", err)
	}

	// check that the org exists
	_, err = ms.Organization(census.OrgAddress)
	if err != nil {
		if err == ErrNotFound {
			return common.Address{}, ErrInvalidData
		}
		return common.Address{}, fmt.Errorf("organization not found: %w", err)
	}

	// check that the member exists
	if _, err := ms.OrgMember(census.OrgAddress, participant.ParticipantID); err != nil {
		return common.Address{}, fmt.Errorf("failed to get org member: %w", err)
	}

	return census.OrgAddress, nil
}

// SetCensusParticipant creates or updates a census participant in the database.
// If the participant already exists (same participantID and censusID), it updates it.
// If it doesn't exist, it creates a new one.
func (ms *MongoStorage) SetCensusParticipant(participant *CensusParticipant) error {
	// Validate the participant
	_, err := ms.validateCensusParticipant(participant)
	if err != nil {
		return err
	}

	// prepare filter for upsert
	filter := bson.M{
		"participantID": participant.ParticipantID,
		"censusId":      participant.CensusID,
	}

	// set timestamps
	now := time.Now()
	participant.UpdatedAt = now
	if participant.CreatedAt.IsZero() {
		participant.CreatedAt = now
	}

	// create update document
	updateDoc := bson.M{
		"$set": participant,
	}

	// Perform database operation
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	opts := options.Update().SetUpsert(true)
	if _, err := ms.censusParticipants.UpdateOne(ctx, filter, updateDoc, opts); err != nil {
		return fmt.Errorf("failed to set census participant: %w", err)
	}

	return nil
}

// CensusParticipant retrieves a census participant from the database based on
// participantID and censusID. Returns ErrNotFound if the participant doesn't exist.
func (ms *MongoStorage) CensusParticipant(censusID, id string) (*CensusParticipant, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// validate input
	if len(id) == 0 || len(censusID) == 0 {
		return nil, ErrInvalidData
	}

	// prepare filter for find
	filter := bson.M{
		"participantID": id,
		"censusId":      censusID,
	}

	// find the participant
	participant := &CensusParticipant{}
	err := ms.censusParticipants.FindOne(ctx, filter).Decode(participant)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get census participant by id: %w", err)
	}

	return participant, nil
}

// CensusParticipantByMemberNumber retrieves a census participant from the database based on
// memberNumber and censusID. Returns ErrNotFound if the participant doesn't exist.
func (ms *MongoStorage) CensusParticipantByMemberNumber(
	censusID string,
	memberNumber string,
	orgAddress common.Address,
) (*CensusParticipant, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// validate input
	if len(memberNumber) == 0 || len(censusID) == 0 {
		return nil, ErrInvalidData
	}

	orgMember, err := ms.OrgMemberByMemberNumber(orgAddress, memberNumber)
	if err != nil {
		if err == mongo.ErrNoDocuments || err == ErrNotFound {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get org member: %w", err)
	}

	// prepare filter for find
	filter := bson.M{
		"participantID": orgMember.ID.Hex(),
		"censusId":      censusID,
	}

	// find the participant
	participant := &CensusParticipant{}
	err = ms.censusParticipants.FindOne(ctx, filter).Decode(participant)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get census participant by member number: %w", err)
	}

	return participant, nil
}

// censusParticipantByLoginHash retrieves a census participant from the database based on
// the login data hash and censusID. Returns ErrNotFound if the participant doesn't exist.
// TODO add the index
func (ms *MongoStorage) censusParticipantByLoginHash(
	censusID string,
	loginHash []byte,
) (*CensusParticipant, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// validate input
	if len(loginHash) == 0 || len(censusID) == 0 {
		return nil, ErrInvalidData
	}

	// prepare filter for find
	filter := bson.M{
		"loginHash": loginHash,
		"censusId":  censusID,
	}

	// find the participant
	participant := &CensusParticipant{}
	err := ms.censusParticipants.FindOne(ctx, filter).Decode(participant)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return participant, nil
}

// CensusParticipantByLoginHash retrieves a census participant from the database based on
// the member email or phone. Returns ErrNotFound if the participant doesn't exist.
func (ms *MongoStorage) censusParticipantByEmailOrPhone(
	censusID string,
	authFields OrgMemberAuthFields,
	member OrgMember,
) (*CensusParticipant, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// prepare filter for find
	var filter bson.M
	switch {
	case member.Email != "" && member.Phone.IsEmpty():
		filter = bson.M{
			"loginHashEmail": HashAuthTwoFaFields(member, authFields, OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail}),
			"censusId":       censusID,
		}
	case member.Email == "" && !member.Phone.IsEmpty():
		filter = bson.M{
			"loginHashPhone": HashAuthTwoFaFields(member, authFields, OrgMemberTwoFaFields{OrgMemberTwoFaFieldPhone}),
			"censusId":       censusID,
		}
	default:
		return nil, fmt.Errorf("member has neither phone nor email")
	}

	// find the participant
	participant := &CensusParticipant{}
	err := ms.censusParticipants.FindOne(ctx, filter).Decode(participant)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	return participant, nil
}

// CensusParticipantByLoginHashOrEmailorPhone is an extension of CensusParticipantByLoginHash for
// the case when the census has 2FA enabled with both email and phone but only one of them is available.
// It calculates the login hash for the available field and tries to find the participant.
func (ms *MongoStorage) CensusParticipantByLoginHashOrEmailorPhone(
	censusID string,
	authFields OrgMemberAuthFields,
	twoFaFields OrgMemberTwoFaFields,
	member OrgMember,
) (*CensusParticipant, error) {
	// validate input
	if len(censusID) == 0 {
		return nil, ErrInvalidData
	}

	participant, err := ms.censusParticipantByLoginHash(censusID, HashAuthTwoFaFields(member, authFields, twoFaFields))
	if errors.Is(err, ErrNotFound) && len(twoFaFields) > 0 {
		participant, err = ms.censusParticipantByEmailOrPhone(censusID, authFields, member)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get census participant by login hash, email or phone: %w", err)
	}

	return participant, nil
}

// DelCensusParticipant removes a census participant from the database.
// Returns nil if the participant was successfully deleted or didn't exist.
func (ms *MongoStorage) DelCensusParticipant(censusID, participantID string) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// validate input
	if len(participantID) == 0 || len(censusID) == 0 {
		return ErrInvalidData
	}

	// prepare filter for upsert
	filter := bson.M{
		"participantID": participantID,
		"censusId":      censusID,
	}

	// delete the participant
	_, err := ms.censusParticipants.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to delete census participant: %w", err)
	}

	return nil
}

// BulkCensusParticipantStatus is returned by SetBylkCensusParticipant to provide the output.
type BulkCensusParticipantStatus struct {
	Progress int `json:"progress"`
	Total    int `json:"total"`
	Added    int `json:"added"`
}

// createCensusParticipantBulkOperations creates the bulk write operations for members and participants
func createCensusParticipantBulkOperations(
	orgMembers []*OrgMember,
	org *Organization,
	censusID primitive.ObjectID,
	salt string,
	currentTime time.Time,
) (orgMembersOps []mongo.WriteModel, censusParticipantOps []mongo.WriteModel) {
	var bulkOrgMembersOps []mongo.WriteModel
	var bulkCensusParticipantsOps []mongo.WriteModel

	for _, m := range orgMembers {
		// Prepare the member
		orgMember, _ := prepareOrgMember(org, m, salt, currentTime)
		// TODO: handle prepareOrgMember []error, pass them back to client

		// Create member filter and update document
		memberFilter := bson.M{
			"_id":        orgMember.ID,
			"orgAddress": orgMember.OrgAddress,
		}

		updateOrgMembersDoc, err := dynamicUpdateDocument(orgMember, nil)
		if err != nil {
			log.Warnw("failed to create update document for member",
				"error", err, "ID", orgMember.ID)
			continue // Skip this member but continue with others
		}

		// Create member upsert model
		upsertOrgMembersModel := mongo.NewUpdateOneModel().
			SetFilter(memberFilter).
			SetUpdate(updateOrgMembersDoc).
			SetUpsert(true)
		bulkOrgMembersOps = append(bulkOrgMembersOps, upsertOrgMembersModel)

		// Create participant filter and document
		censusParticipantsFilter := bson.M{
			"participantID": orgMember.ID.Hex(),
			"censusId":      censusID.Hex(),
		}

		// Create document for $set operation (without CreatedAt)
		participantDoc := &CensusParticipant{
			ParticipantID: orgMember.ID.Hex(),
			CensusID:      censusID.Hex(),
			UpdatedAt:     currentTime,
		}

		// Create participant update document
		updateParticipantDoc, err := dynamicUpdateDocument(participantDoc, nil)
		if err != nil {
			log.Warnw("failed to create update document for participant",
				"error", err, "participantID", orgMember.ID.Hex())
			continue
		}

		// Extract the $set part from the update document with type checking
		setDoc, ok := updateParticipantDoc["$set"].(bson.M)
		if !ok {
			log.Warnw("failed to extract $set document for participant",
				"error", "invalid $set type", "participantID", orgMember.ID.Hex())
			continue
		}

		// Create combined update document with both $set and $setOnInsert
		combinedUpdateDoc := bson.M{
			"$set": setDoc,
			"$setOnInsert": bson.M{
				"createdAt": currentTime,
			},
		}

		// Create participant upsert model
		upsertCensusParticipantsModel := mongo.NewUpdateOneModel().
			SetFilter(censusParticipantsFilter).
			SetUpdate(combinedUpdateDoc).
			SetUpsert(true)
		bulkCensusParticipantsOps = append(bulkCensusParticipantsOps, upsertCensusParticipantsModel)
	}

	return bulkOrgMembersOps, bulkCensusParticipantsOps
}

// processBatch processes a batch of members and returns the number added
func (ms *MongoStorage) processBatch(
	bulkOrgMembersOps []mongo.WriteModel,
	bulkCensusParticipantOps []mongo.WriteModel,
) int {
	if len(bulkOrgMembersOps) == 0 {
		return 0
	}

	// Only lock the mutex during the actual database operations
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	// Create a new context for the batch
	batchCtx, batchCancel := context.WithTimeout(context.Background(), batchTimeout)
	defer batchCancel()

	// Execute the bulk write operations for org members
	_, err := ms.orgMembers.BulkWrite(batchCtx, bulkOrgMembersOps)
	if err != nil {
		log.Warnw("failed to perform bulk operation on members", "error", err)
		return 0
	}

	// Execute the bulk write operations for census participants
	_, err = ms.censusParticipants.BulkWrite(batchCtx, bulkCensusParticipantOps)
	if err != nil {
		log.Warnw("failed to perform bulk operation on participants", "error", err)
		return 0
	}

	return len(bulkOrgMembersOps)
}

// startProgressReporter starts a goroutine that reports progress periodically
func startProgressReporter(
	ctx context.Context,
	progressChan chan<- *BulkCensusParticipantStatus,
	totalOrgMembers int,
	processedOrgMembers *int,
	addedOrgMembers *int,
) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Calculate and send progress percentage
			if totalOrgMembers > 0 {
				progress := (*processedOrgMembers * 100) / totalOrgMembers
				progressChan <- &BulkCensusParticipantStatus{
					Progress: progress,
					Total:    totalOrgMembers,
					Added:    *addedOrgMembers,
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// validateBulkCensusParticipant validates the input parameters for bulk census participant
// and returns the census if valid
func (ms *MongoStorage) validateBulkCensusParticipant(
	censusID string,
	orgMembersSize int,
) (*Census, error) {
	// Early returns for invalid input
	if orgMembersSize == 0 {
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

// processBatches processes members in batches and sends progress updates
func (ms *MongoStorage) processBatches(
	orgMembers []*OrgMember,
	org *Organization,
	census *Census,
	salt string,
	progressChan chan<- *BulkCensusParticipantStatus,
) {
	defer close(progressChan)

	// Process members in batches of 200
	batchSize := 200
	totalOrgMembers := len(orgMembers)
	processedOrgMembers := 0
	addedOrgMembers := 0
	currentTime := time.Now()

	// Send initial progress
	progressChan <- &BulkCensusParticipantStatus{
		Progress: 0,
		Total:    totalOrgMembers,
		Added:    addedOrgMembers,
	}

	// Create a context for the entire operation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start progress reporter in a separate goroutine
	go startProgressReporter(ctx, progressChan, totalOrgMembers, &processedOrgMembers, &addedOrgMembers)

	// Process members in batches
	for i := 0; i < totalOrgMembers; i += batchSize {
		// Calculate end index for current batch
		end := i + batchSize
		if end > totalOrgMembers {
			end = totalOrgMembers
		}

		// Create bulk operations for this batch
		bulkOrgMembersOps, bulkCensusParticipantOps := createCensusParticipantBulkOperations(
			orgMembers[i:end],
			org,
			census.ID,
			salt,
			currentTime,
		)

		// Process the batch and get number of added members
		added := ms.processBatch(bulkOrgMembersOps, bulkCensusParticipantOps)
		addedOrgMembers += added

		// Update processed count
		processedOrgMembers += (end - i)
	}

	// Send final progress (100%)
	progressChan <- &BulkCensusParticipantStatus{
		Progress: 100,
		Total:    totalOrgMembers,
		Added:    addedOrgMembers,
	}
}

// SetBulkCensusOrgMemberParticipant creates or updates an org member and a census participant in the database.
// If the participant already exists (same participantID and censusID), it updates it.
// If it doesn't exist, it creates a new one.
// Processes members in batches of 200 entries.
// Returns a channel that sends the percentage of members processed every 10 seconds.
// This function must be called in a goroutine.
func (ms *MongoStorage) SetBulkCensusOrgMemberParticipant(
	org *Organization, salt, censusID string, orgMembers []*OrgMember,
) (chan *BulkCensusParticipantStatus, error) {
	progressChan := make(chan *BulkCensusParticipantStatus, 10)

	// Validate input parameters
	census, err := ms.validateBulkCensusParticipant(censusID, len(orgMembers))
	if err != nil {
		close(progressChan)
		return progressChan, err
	}

	// If no members, return empty channel
	if census == nil {
		close(progressChan)
		return progressChan, nil
	}

	// Start processing in a goroutine
	go ms.processBatches(orgMembers, org, census, salt, progressChan)

	return progressChan, nil
}

func (ms *MongoStorage) setBulkCensusParticipant(
	ctx context.Context, censusID, groupID string, orgAddress common.Address,
	authFields OrgMemberAuthFields, twoFaFields OrgMemberTwoFaFields,
) (int64, error) {
	_, members, err := ms.ListOrganizationMemberGroup(groupID, orgAddress, 0, 0)
	if err != nil {
		return 0, fmt.Errorf("error retrieving group members: %w", err)
	}
	if len(members) == 0 {
		return 0, nil // nothing to do
	}

	// prepare filter for upsert
	currentTime := time.Now()

	docs := make([]mongo.WriteModel, 0, len(members))
	for _, member := range members {
		// Create participant filter and document
		id := member.ID.Hex()
		censusParticipantsFilter := bson.M{
			"participantID": id,
			"censusId":      censusID,
		}
		participantDoc := &CensusParticipant{
			ParticipantID: id,
			LoginHash:     HashAuthTwoFaFields(*member, authFields, twoFaFields),
			CensusID:      censusID,
			UpdatedAt:     currentTime,
		}

		if len(twoFaFields) == 2 && member.Email != "" {
			participantDoc.LoginHashEmail = HashAuthTwoFaFields(*member, authFields, OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail})
		}
		if len(twoFaFields) == 2 && !member.Phone.IsEmpty() {
			participantDoc.LoginHashPhone = HashAuthTwoFaFields(*member, authFields, OrgMemberTwoFaFields{OrgMemberTwoFaFieldPhone})
		}
		// Create participant update document
		updateParticipantDoc, err := dynamicUpdateDocument(participantDoc, nil)
		if err != nil {
			log.Warnw("failed to create update document for participant",
				"error", err, "participantID", id)
			continue
		}

		// Extract the $set part from the update document with type checking
		setDoc, ok := updateParticipantDoc["$set"].(bson.M)
		if !ok {
			log.Warnw("failed to extract $set document for participant",
				"error", "invalid $set type", "participantID", member.ID.Hex())
			continue
		}

		// Create combined update document with both $set and $setOnInsert
		combinedUpdateDoc := bson.M{
			"$set": setDoc,
			"$setOnInsert": bson.M{
				"createdAt": currentTime,
			},
		}

		// Create participant upsert model
		upsertCensusParticipantsModel := mongo.NewUpdateOneModel().
			SetFilter(censusParticipantsFilter).
			SetUpdate(combinedUpdateDoc).
			SetUpsert(true)
		docs = append(docs, upsertCensusParticipantsModel)
	}
	// Unordered makes it continue on errors (e.g., one dup)
	bulkOpts := options.BulkWrite().SetOrdered(false)

	results, err := ms.censusParticipants.BulkWrite(ctx, docs, bulkOpts)
	return results.UpsertedCount, err
}

// CensusParticipants retrieves all the census participants for a given census.
func (ms *MongoStorage) CensusParticipants(censusID string) ([]CensusParticipant, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// validate input
	if len(censusID) == 0 {
		return nil, ErrInvalidData
	}

	// prepare filter for upsert
	filter := bson.M{
		"censusId": censusID,
	}

	// find the participant
	cursor, err := ms.censusParticipants.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to get census participants: %w", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()
	var participants []CensusParticipant
	if err := cursor.All(ctx, &participants); err != nil {
		return nil, fmt.Errorf("failed to parse census participants: %w", err)
	}

	return participants, nil
}
