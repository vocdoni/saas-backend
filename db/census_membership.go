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
// by checking that the census exists, the organization exists, and the member exists
func (ms *MongoStorage) validateCensusMembership(membership *CensusMembership) (string, error) {
	// validate required fields
	if len(membership.MemberID) == 0 || len(membership.CensusID) == 0 {
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

	// check that the member exists
	if _, err := ms.OrgMemberByID(census.OrgAddress, membership.MemberID); err != nil {
		return "", fmt.Errorf("failed to get org member: %w", err)
	}

	return census.OrgAddress, nil
}

// SetCensusMembership creates or updates a census membership in the database.
// If the membership already exists (same memberID and censusID), it updates it.
// If it doesn't exist, it creates a new one.
func (ms *MongoStorage) SetCensusMembership(membership *CensusMembership) error {
	// Validate the membership
	_, err := ms.validateCensusMembership(membership)
	if err != nil {
		return err
	}

	// prepare filter for upsert
	filter := bson.M{
		"memberID": membership.MemberID,
		"censusId": membership.CensusID,
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

	// Perform database operation
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	opts := options.Update().SetUpsert(true)
	if _, err := ms.censusMemberships.UpdateOne(ctx, filter, updateDoc, opts); err != nil {
		return fmt.Errorf("failed to set census membership: %w", err)
	}

	return nil
}

// CensusMembership retrieves a census membership from the database based on
// memberID and censusID. Returns ErrNotFound if the membership doesn't exist.
func (ms *MongoStorage) CensusMembership(censusID, memberID string) (*CensusMembership, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// validate input
	if len(memberID) == 0 || len(censusID) == 0 {
		return nil, ErrInvalidData
	}

	// prepare filter for upsert
	filter := bson.M{
		"memberID": memberID,
		"censusId": censusID,
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
func (ms *MongoStorage) DelCensusMembership(censusID, memberID string) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// validate input
	if len(memberID) == 0 || len(censusID) == 0 {
		return ErrInvalidData
	}

	// prepare filter for upsert
	filter := bson.M{
		"memberID": memberID,
		"censusId": censusID,
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

// prepareMember processes a member for storage by:
// - Setting the organization address
// - Setting the creation timestamp
// - Hashing sensitive data (email, phone, password)
// - Clearing the original sensitive data
func prepareMember(member *OrgMember, orgAddress, salt string, currentTime time.Time) {
	member.OrgAddress = orgAddress
	member.CreatedAt = currentTime

	// Hash phone if valid
	if member.Phone != "" {
		pn, err := internal.SanitizeAndVerifyPhoneNumber(member.Phone)
		if err != nil {
			log.Warnw("invalid phone number", "phone", member.Phone)
			member.Phone = ""
		} else {
			member.HashedPhone = internal.HashOrgData(orgAddress, pn)
			member.Phone = ""
		}
	}

	// Hash password if present
	if member.Password != "" {
		member.HashedPass = internal.HashPassword(salt, member.Password)
		member.Password = ""
	}
}

// createBulkOperations creates the bulk write operations for members and memberships
func createBulkOperations(
	orgMembers []OrgMember,
	orgAddress string,
	censusID string,
	salt string,
	currentTime time.Time,
) (orgMembersOps []mongo.WriteModel, censusMembershipOps []mongo.WriteModel) {
	var bulkOrgMembersOps []mongo.WriteModel
	var bulkCensusMembersOps []mongo.WriteModel

	for _, orgMember := range orgMembers {
		// Prepare the member
		prepareMember(&orgMember, orgAddress, salt, currentTime)

		// Create member filter and update document
		memberFilter := bson.M{
			"memberID":   orgMember.MemberID,
			"orgAddress": orgAddress,
		}

		updateOrgMembersDoc, err := dynamicUpdateDocument(orgMember, nil)
		if err != nil {
			log.Warnw("failed to create update document for member",
				"error", err, "memberID", orgMember.MemberID)
			continue // Skip this member but continue with others
		}

		// Create member upsert model
		upsertOrgMembersModel := mongo.NewUpdateOneModel().
			SetFilter(memberFilter).
			SetUpdate(updateOrgMembersDoc).
			SetUpsert(true)
		bulkOrgMembersOps = append(bulkOrgMembersOps, upsertOrgMembersModel)

		// Create membership filter and document
		censusMembersFilter := bson.M{
			"memberID": orgMember.MemberID,
			"censusId": censusID,
		}
		membershipDoc := &CensusMembership{
			MemberID:  orgMember.MemberID,
			CensusID:  censusID,
			CreatedAt: currentTime,
		}

		// Create membership update document
		updateMembershipDoc, err := dynamicUpdateDocument(membershipDoc, nil)
		if err != nil {
			log.Warnw("failed to create update document for membership",
				"error", err, "memberID", orgMember.MemberID)
			continue
		}

		// Create membership upsert model
		upsertCensusMembersModel := mongo.NewUpdateOneModel().
			SetFilter(censusMembersFilter).
			SetUpdate(updateMembershipDoc).
			SetUpsert(true)
		bulkCensusMembersOps = append(bulkCensusMembersOps, upsertCensusMembersModel)
	}

	return bulkOrgMembersOps, bulkCensusMembersOps
}

// processBatch processes a batch of members and returns the number added
func (ms *MongoStorage) processBatch(
	bulkOrgMembersOps []mongo.WriteModel,
	bulkCensusMembershipOps []mongo.WriteModel,
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

	// Execute the bulk write operations for census memberships
	_, err = ms.censusMemberships.BulkWrite(batchCtx, bulkCensusMembershipOps)
	if err != nil {
		log.Warnw("failed to perform bulk operation on memberships", "error", err)
		return 0
	}

	return len(bulkOrgMembersOps)
}

// startProgressReporter starts a goroutine that reports progress periodically
func startProgressReporter(
	ctx context.Context,
	progressChan chan<- *BulkCensusMembershipStatus,
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
				progressChan <- &BulkCensusMembershipStatus{
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

// validateBulkCensusMembership validates the input parameters for bulk census membership
// and returns the census if valid
func (ms *MongoStorage) validateBulkCensusMembership(
	censusID string,
	orgMembers []OrgMember,
) (*Census, error) {
	// Early returns for invalid input
	if len(orgMembers) == 0 {
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
	orgMembers []OrgMember,
	census *Census,
	censusID string,
	salt string,
	progressChan chan<- *BulkCensusMembershipStatus,
) {
	defer close(progressChan)

	// Process members in batches of 200
	batchSize := 200
	totalOrgMembers := len(orgMembers)
	processedOrgMembers := 0
	addedOrgMembers := 0
	currentTime := time.Now()

	// Send initial progress
	progressChan <- &BulkCensusMembershipStatus{
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
		bulkOrgMembersOps, bulkCensusMembershipOps := createBulkOperations(
			orgMembers[i:end],
			census.OrgAddress,
			censusID,
			salt,
			currentTime,
		)

		// Process the batch and get number of added members
		added := ms.processBatch(bulkOrgMembersOps, bulkCensusMembershipOps)
		addedOrgMembers += added

		// Update processed count
		processedOrgMembers += (end - i)
	}

	// Send final progress (100%)
	progressChan <- &BulkCensusMembershipStatus{
		Progress: 100,
		Total:    totalOrgMembers,
		Added:    addedOrgMembers,
	}
}

// SetBulkCensusMembership creates or updates an org member and a census membership in the database.
// If the membership already exists (same memberID and censusID), it updates it.
// If it doesn't exist, it creates a new one.
// Processes members in batches of 200 entries.
// Returns a channel that sends the percentage of members processed every 10 seconds.
// This function must be called in a goroutine.
func (ms *MongoStorage) SetBulkCensusMembership(
	salt, censusID string, orgMembers []OrgMember,
) (chan *BulkCensusMembershipStatus, error) {
	progressChan := make(chan *BulkCensusMembershipStatus, 10)

	// Validate input parameters
	census, err := ms.validateBulkCensusMembership(censusID, orgMembers)
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
	go ms.processBatches(orgMembers, census, censusID, salt, progressChan)

	return progressChan, nil
}

// CensusMemberships retrieves all the census memberships for a given census.
func (ms *MongoStorage) CensusMemberships(censusID string) ([]CensusMembership, error) {
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
