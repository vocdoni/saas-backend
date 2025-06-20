package db

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// SetOrgMember creates a new orgMembers for an organization
// requires an existing organization
func (ms *MongoStorage) SetOrgMember(salt string, orgMember *OrgMember) (string, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	if len(orgMember.OrgAddress) == 0 {
		return "", ErrInvalidData
	}

	// check that the org exists
	_, err := ms.Organization(orgMember.OrgAddress)
	if err != nil {
		if err == ErrNotFound {
			return "", ErrInvalidData
		}
		return "", fmt.Errorf("organization not found: %w", err)
	}

	if orgMember.Phone != "" {
		// normalize and store only the hashed phone
		normalizedPhone, err := internal.SanitizeAndVerifyPhoneNumber(orgMember.Phone)
		if err == nil {
			orgMember.HashedPhone = internal.HashOrgData(orgMember.OrgAddress, normalizedPhone)
		}
		orgMember.Phone = ""
	}
	if orgMember.Password != "" {
		// store only the hashed password
		orgMember.HashedPass = internal.HashPassword(salt, orgMember.Password)
		orgMember.Password = ""
	}

	if orgMember.ID != primitive.NilObjectID {
		// if the orgMember exists, update it with the new data
		orgMember.UpdatedAt = time.Now()
	} else {
		// if the orgMember doesn't exist, create the corresponding id
		orgMember.ID = primitive.NewObjectID()
		orgMember.CreatedAt = time.Now()
	}
	updateDoc, err := dynamicUpdateDocument(orgMember, nil)
	if err != nil {
		return "", err
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	filter := bson.M{"_id": orgMember.ID}
	opts := options.Update().SetUpsert(true)
	_, err = ms.orgMembers.UpdateOne(ctx, filter, updateDoc, opts)
	if err != nil {
		return "", err
	}

	return orgMember.ID.Hex(), nil
}

// DeleteOrgMember removes a orgMember
func (ms *MongoStorage) DelOrgMember(id string) error {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// delete the orgMember from the database using the ID
	filter := bson.M{"_id": objID}
	_, err = ms.orgMembers.DeleteOne(ctx, filter)
	return err
}

// OrgMember retrieves a orgMember from the DB based on it ID
func (ms *MongoStorage) OrgMember(orgAddress, id string) (*OrgMember, error) {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, ErrInvalidData
	}

	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	orgMember := &OrgMember{}
	if err = ms.orgMembers.FindOne(ctx, bson.M{"_id": objID, "orgAddress": orgAddress}).Decode(orgMember); err != nil {
		return nil, fmt.Errorf("failed to get orgMember: %w", err)
	}

	return orgMember, nil
}

// OrgMemberByMemberNumber retrieves a orgMember from the DB based on organization address and member number
func (ms *MongoStorage) OrgMemberByMemberNumber(orgAddress, memberNumber string) (*OrgMember, error) {
	if len(memberNumber) == 0 {
		return nil, ErrInvalidData
	}
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	orgMember := &OrgMember{}
	if err := ms.orgMembers.FindOne(
		ctx, bson.M{"orgAddress": orgAddress, "memberNumber": memberNumber},
	).Decode(orgMember); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get orgMember: %w", err)
	}

	return orgMember, nil
}

// BulkOrgMembersStatus is returned by SetBulkOrgMembers to provide the output.
type BulkOrgMembersStatus struct {
	Progress int `json:"progress"`
	Total    int `json:"total"`
	Added    int `json:"added"`
}

// validateBulkOrgMembers validates the input parameters for bulk org members
func (ms *MongoStorage) validateBulkOrgMembers(
	orgAddress string,
	orgMembers []OrgMember,
) (*Organization, error) {
	// Early returns for invalid input
	if len(orgMembers) == 0 {
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

// prepareOrgMember processes a member for storage
func prepareOrgMember(member *OrgMember, orgAddress, salt string, currentTime time.Time) {
	// Assign a new internal ID if not provided
	if member.ID == primitive.NilObjectID {
		member.ID = primitive.NewObjectID()
	}
	member.OrgAddress = orgAddress
	member.CreatedAt = currentTime

	// Hash phone if valid
	if member.Phone != "" {
		normalizedPhone, err := internal.SanitizeAndVerifyPhoneNumber(member.Phone)
		if err == nil {
			member.HashedPhone = internal.HashOrgData(orgAddress, normalizedPhone)
		}
		member.Phone = ""
	}

	// Hash password if present
	if member.Password != "" {
		member.HashedPass = internal.HashPassword(salt, member.Password)
		member.Password = ""
	}
}

// createOrgMemberBulkOperations creates the bulk write operations for members
func createOrgMemberBulkOperations(
	members []OrgMember,
	orgAddress string,
	salt string,
	currentTime time.Time,
) []mongo.WriteModel {
	var bulkOps []mongo.WriteModel

	for _, member := range members {
		// Prepare the member
		prepareOrgMember(&member, orgAddress, salt, currentTime)

		// Create filter for existing members and update document
		filter := bson.M{
			"_id":        member.ID,
			"orgAddress": orgAddress,
		}

		updateDoc, err := dynamicUpdateDocument(member, nil)
		if err != nil {
			log.Warnw("failed to create update document for member",
				"error", err, "ID", member.ID)
			continue // Skip this member but continue with others
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

// processOrgMemberBatch processes a batch of members and returns the number added
func (ms *MongoStorage) processOrgMemberBatch(
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
	_, err := ms.orgMembers.BulkWrite(batchCtx, bulkOps)
	if err != nil {
		log.Warnw("failed to perform bulk operation on members", "error", err)
		return 0
	}

	return len(bulkOps)
}

// startOrgMemberProgressReporter starts a goroutine that reports progress periodically
func startOrgMemberProgressReporter(
	ctx context.Context,
	progressChan chan<- *BulkOrgMembersStatus,
	totalMembers int,
	processedMembers *int,
	addedMembers *int,
) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Calculate and send progress percentage
			if totalMembers > 0 {
				progress := (*processedMembers * 100) / totalMembers
				progressChan <- &BulkOrgMembersStatus{
					Progress: progress,
					Total:    totalMembers,
					Added:    *addedMembers,
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// processOrgMemberBatches processes members in batches and sends progress updates
func (ms *MongoStorage) processOrgMemberBatches(
	orgMembers []OrgMember,
	orgAddress string,
	salt string,
	progressChan chan<- *BulkOrgMembersStatus,
) {
	defer close(progressChan)

	// Process members in batches of 200
	batchSize := 200
	totalMembers := len(orgMembers)
	processedMembers := 0
	addedMembers := 0
	currentTime := time.Now()

	// Send initial progress
	progressChan <- &BulkOrgMembersStatus{
		Progress: 0,
		Total:    totalMembers,
		Added:    addedMembers,
	}

	// Create a context for the entire operation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start progress reporter in a separate goroutine
	go startOrgMemberProgressReporter(
		ctx,
		progressChan,
		totalMembers,
		&processedMembers,
		&addedMembers,
	)

	// Process members in batches
	for i := 0; i < totalMembers; i += batchSize {
		// Calculate end index for current batch
		end := i + batchSize
		if end > totalMembers {
			end = totalMembers
		}

		// Create bulk operations for this batch
		bulkOps := createOrgMemberBulkOperations(
			orgMembers[i:end],
			orgAddress,
			salt,
			currentTime,
		)

		// Process the batch and get number of added members
		added := ms.processOrgMemberBatch(bulkOps)
		addedMembers += added

		// Update processed count
		processedMembers += (end - i)
	}

	// Send final progress (100%)
	progressChan <- &BulkOrgMembersStatus{
		Progress: 100,
		Total:    totalMembers,
		Added:    addedMembers,
	}
}

// SetBulkOrgMembers adds multiple organization members to the database in batches of 200 entries
// and updates already existing members (decided by combination of internal id and orgAddress)
// Requires an existing organization
// Returns a channel that sends the percentage of members processed every 10 seconds.
// This function must be called in a goroutine.
func (ms *MongoStorage) SetBulkOrgMembers(
	orgAddress, salt string,
	orgMembers []OrgMember,
) (chan *BulkOrgMembersStatus, error) {
	progressChan := make(chan *BulkOrgMembersStatus, 10)

	// Validate input parameters
	org, err := ms.validateBulkOrgMembers(orgAddress, orgMembers)
	if err != nil {
		close(progressChan)
		return progressChan, err
	}

	// If no members, return empty channel
	if org == nil {
		close(progressChan)
		return progressChan, nil
	}

	// Start processing in a goroutine
	go ms.processOrgMemberBatches(orgMembers, orgAddress, salt, progressChan)

	return progressChan, nil
}

// OrgMembers retrieves paginated orgMembers for an organization from the DB
func (ms *MongoStorage) OrgMembers(orgAddress string, page, pageSize int, search string) (int, []OrgMember, error) {
	if len(orgAddress) == 0 {
		return 0, nil, ErrInvalidData
	}
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Create filter
	filter := bson.M{
		"orgAddress": orgAddress,
	}
	if len(search) > 0 {
		filter["$text"] = bson.M{
			"$search": search,
		}
	}

	// Calculate skip value based on page and pageSize
	skip := (page - 1) * pageSize

	// Count total documents
	totalCount, err := ms.orgMembers.CountDocuments(ctx, filter)
	if err != nil {
		return 0, nil, err
	}
	totalPages := int(math.Ceil(float64(totalCount) / float64(pageSize)))

	sort := bson.D{
		bson.E{Key: "name", Value: 1},
		bson.E{Key: "surname", Value: 1},
	}
	// Set up options for pagination
	findOptions := options.Find().
		SetSort(sort). // Sort by createdAt in descending order
		SetSkip(int64(skip)).
		SetLimit(int64(pageSize))

	// Execute the find operation with pagination
	cursor, err := ms.orgMembers.Find(ctx, filter, findOptions)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to get orgMembers: %w", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	// Decode results
	var orgMembers []OrgMember
	if err = cursor.All(ctx, &orgMembers); err != nil {
		return 0, nil, fmt.Errorf("failed to decode orgMembers: %w", err)
	}

	return totalPages, orgMembers, nil
}

func (ms *MongoStorage) DeleteOrgMembers(orgAddress string, ids []string) (int, error) {
	if len(orgAddress) == 0 {
		return 0, ErrInvalidData
	}
	if len(ids) == 0 {
		return 0, nil
	}
	// Convert string IDs to ObjectIDs
	var oids []primitive.ObjectID
	for _, id := range ids {
		objID, err := primitive.ObjectIDFromHex(id)
		if err != nil {
			return 0, fmt.Errorf("invalid member ID %s: %w", id, ErrInvalidData)
		}
		oids = append(oids, objID)
	}

	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// create the filter for the delete operation
	filter := bson.M{
		"orgAddress": orgAddress,
		"_id": bson.M{
			"$in": oids,
		},
	}

	result, err := ms.orgMembers.DeleteMany(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("failed to delete orgMembers: %w", err)
	}

	return int(result.DeletedCount), nil
}

// validateOrgMembers checks if the provided member IDs are valid
func (ms *MongoStorage) validateOrgMembers(ctx context.Context, orgAddress string, members []string) error {
	if len(members) == 0 {
		return fmt.Errorf("no members provided")
	}

	// Convert string IDs to ObjectIDs
	var objectIDs []primitive.ObjectID
	for _, id := range members {
		objID, err := primitive.ObjectIDFromHex(id)
		if err != nil {
			return fmt.Errorf("invalid ObjectID format: %s", id)
		}
		objectIDs = append(objectIDs, objID)
	}

	cursor, err := ms.orgMembers.Find(ctx, bson.M{
		"_id":        bson.M{"$in": objectIDs},
		"orgAddress": orgAddress,
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	var found []OrgMember
	if err := cursor.All(ctx, &found); err != nil {
		return err
	}

	// Create a map of found IDs for quick lookup
	foundMap := make(map[string]bool)
	for _, member := range found {
		foundMap[member.ID.Hex()] = true
	}

	// Check if all requested IDs were found
	for _, id := range members {
		if !foundMap[id] {
			return fmt.Errorf("invalid member ID in add list: %s", id)
		}
	}
	return nil
}

// getOrgMembersByIDs retrieves organization members by their IDs
func (ms *MongoStorage) orgMembersByIDs(
	orgAddress string,
	memberIDs []string,
	page, pageSize int64,
) (int, []*OrgMember, error) {
	if len(memberIDs) == 0 {
		return 0, nil, nil // No members to retrieve
	}

	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Convert string IDs to ObjectIDs
	var objectIDs []primitive.ObjectID
	for _, id := range memberIDs {
		objID, err := primitive.ObjectIDFromHex(id)
		if err != nil {
			return 0, nil, fmt.Errorf("invalid ObjectID format: %s", id)
		}
		objectIDs = append(objectIDs, objID)
	}

	filter := bson.M{
		"_id":        bson.M{"$in": objectIDs},
		"orgAddress": orgAddress,
	}

	// Count total documents
	totalCount, err := ms.orgMembers.CountDocuments(ctx, filter)
	if err != nil {
		return 0, nil, err
	}
	totalPages := int(math.Ceil(float64(totalCount) / float64(pageSize)))

	// Calculate skip value based on page and pageSize
	skip := (page - 1) * pageSize

	// Set up options for pagination
	findOptions := options.Find().
		SetSkip(skip).
		SetLimit(pageSize)

	cursor, err := ms.orgMembers.Find(ctx, filter, findOptions)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to find org members: %w", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	var members []*OrgMember
	if err := cursor.All(ctx, &members); err != nil {
		return 0, nil, fmt.Errorf("failed to decode org members: %w", err)
	}

	return totalPages, members, nil
}
