package db

import (
	"context"
	"fmt"
	"math"
	"net/mail"
	"time"

	"github.com/ethereum/go-ethereum/common"
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

	if orgMember.OrgAddress.Cmp(common.Address{}) == 0 {
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
func (ms *MongoStorage) OrgMember(orgAddress common.Address, id string) (*OrgMember, error) {
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
func (ms *MongoStorage) OrgMemberByMemberNumber(orgAddress common.Address, memberNumber string) (*OrgMember, error) {
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

// BulkOrgMembersJob is returned by SetBulkOrgMembers to provide the output.
type BulkOrgMembersJob struct {
	Progress int
	Total    int
	Added    int
	Errors   []error
}

func (j *BulkOrgMembersJob) ErrorsAsStrings() []string {
	s := []string{}
	for _, err := range j.Errors {
		s = append(s, err.Error())
	}
	return s
}

// validateBulkOrgMembers validates the input parameters for bulk org members
func (ms *MongoStorage) validateBulkOrgMembers(
	orgAddress common.Address,
	orgMembers []OrgMember,
) (*Organization, error) {
	// Early returns for invalid input
	if len(orgMembers) == 0 {
		return nil, nil // Not an error, just no work to do
	}
	if orgAddress.Cmp(common.Address{}) == 0 {
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
func prepareOrgMember(member *OrgMember, orgAddress common.Address, salt string, currentTime time.Time) []error {
	var errors []error

	// Assign a new internal ID if not provided
	if member.ID == primitive.NilObjectID {
		member.ID = primitive.NewObjectID()
	}
	member.OrgAddress = orgAddress
	member.CreatedAt = currentTime

	// check if mail is valid
	if member.Email != "" {
		if _, err := mail.ParseAddress(member.Email); err != nil {
			errors = append(errors, fmt.Errorf("could not parse from email: %s %v", member.Email, err))
			// If email is invalid, set it to empty and store the error
			member.Email = ""
		}
	}

	// Hash phone if valid
	if member.Phone != "" {
		normalizedPhone, err := internal.SanitizeAndVerifyPhoneNumber(member.Phone)
		if err == nil {
			member.HashedPhone = internal.HashOrgData(orgAddress, normalizedPhone)
		} else {
			errors = append(errors, fmt.Errorf("could not sanitize phone number: %s %v", member.Phone, err))
		}
		member.Phone = ""
	}

	// Hash password if present
	if member.Password != "" {
		member.HashedPass = internal.HashPassword(salt, member.Password)
		member.Password = ""
	}

	// Check that the birthdate is valid
	if len(member.BirthDate) > 0 {
		if _, err := time.Parse("2006-01-02", member.BirthDate); err != nil {
			errors = append(errors, fmt.Errorf("invalid birthdate format: %s %v", member.BirthDate, err))
			member.BirthDate = "" // Reset invalid birthdate
		}
	}
	return errors
}

// createOrgMemberBulkOperations creates a batch of members using bulk write operations,
// and returns the number of members added (or updated) and any errors encountered.
func (ms *MongoStorage) createOrgMemberBulkOperations(
	members []OrgMember,
	orgAddress common.Address,
	salt string,
	currentTime time.Time,
) (int, []error) {
	var bulkOps []mongo.WriteModel
	var errors []error

	for _, member := range members {
		// Prepare the member
		validationErrors := prepareOrgMember(&member, orgAddress, salt, currentTime)
		errors = append(errors, validationErrors...)

		// Create filter for existing members and update document
		filter := bson.M{
			"_id":        member.ID,
			"orgAddress": orgAddress,
		}

		updateDoc, err := dynamicUpdateDocument(member, nil)
		if err != nil {
			log.Warnw("failed to create update document for member",
				"error", err, "ID", member.ID)
			errors = append(errors, fmt.Errorf("member %s: %w", member.ID.Hex(), err))
			continue // Skip this member but continue with others
		}

		// Create upsert model
		upsertModel := mongo.NewUpdateOneModel().
			SetFilter(filter).
			SetUpdate(updateDoc).
			SetUpsert(true)
		bulkOps = append(bulkOps, upsertModel)
	}

	if len(bulkOps) == 0 {
		return 0, errors
	}

	// Only lock the mutex during the actual database operations
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	// Create a new context for the batch
	batchCtx, batchCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer batchCancel()

	// Execute the bulk write operations
	result, err := ms.orgMembers.BulkWrite(batchCtx, bulkOps)
	if err != nil {
		log.Warnw("error during bulk operation on members batch", "error", err)
		firstID := members[0].ID
		lastID := members[len(members)-1].ID
		errors = append(errors, fmt.Errorf("batch %s - %s: %w", firstID.Hex(), lastID.Hex(), err))
	}

	return int(result.ModifiedCount + result.UpsertedCount), errors
}

// startOrgMemberProgressReporter starts a goroutine that reports progress periodically
func startOrgMemberProgressReporter(
	ctx context.Context,
	progressChan chan<- *BulkOrgMembersJob,
	status *BulkOrgMembersJob,
) {
	defer close(progressChan)

	if status.Total == 0 {
		return
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	// Send initial progress
	progressChan <- status

	for {
		select {
		case <-ticker.C:
			progressChan <- status
		case <-ctx.Done():
			// Send final progress (100%)
			progressChan <- status
			return
		}
	}
}

// processOrgMemberBatches processes members in batches and sends progress updates
func (ms *MongoStorage) processOrgMemberBatches(
	orgMembers []OrgMember,
	orgAddress common.Address,
	salt string,
	progressChan chan<- *BulkOrgMembersJob,
) {
	if len(orgMembers) == 0 {
		close(progressChan)
		return
	}

	// Process members in batches of 200
	batchSize := 200
	currentTime := time.Now()

	job := BulkOrgMembersJob{
		Progress: 0,
		Total:    len(orgMembers),
		Added:    0,
		Errors:   []error{},
	}

	// Create a context for the progress reporter
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start progress reporter in a separate goroutine
	go startOrgMemberProgressReporter(
		ctx,
		progressChan,
		&job,
	)

	// Process members in batches
	for start := 0; start < job.Total; start += batchSize {
		// Calculate end index for current batch
		end := min(start+batchSize, job.Total)

		// Process the batch and get number of added members
		added, errs := ms.createOrgMemberBulkOperations(
			orgMembers[start:end],
			orgAddress,
			salt,
			currentTime,
		)

		// Update job stats
		job = BulkOrgMembersJob{
			Progress: ((job.Added + added) / job.Total) * 100,
			Total:    job.Total,
			Added:    job.Added + added,
			Errors:   append(job.Errors, errs...),
		}
	}
}

// SetBulkOrgMembers adds multiple organization members to the database in batches of 200 entries
// and updates already existing members (decided by combination of internal id and orgAddress)
// Requires an existing organization
// Returns a channel that sends the percentage of members processed every 10 seconds.
// This function must be called in a goroutine.
func (ms *MongoStorage) SetBulkOrgMembers(
	orgAddress common.Address, salt string,
	orgMembers []OrgMember,
) (chan *BulkOrgMembersJob, error) {
	progressChan := make(chan *BulkOrgMembersJob, 10)

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
func (ms *MongoStorage) OrgMembers(orgAddress common.Address, page, pageSize int, search string) (int, []OrgMember, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
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
		filter["$or"] = []bson.M{
			{"email": bson.M{"$regex": search, "$options": "i"}},
			{"memberNumber": bson.M{"$regex": search, "$options": "i"}},
			{"nationalID": bson.M{"$regex": search, "$options": "i"}},
			{"name": bson.M{"$regex": search, "$options": "i"}},
			{"surname": bson.M{"$regex": search, "$options": "i"}},
			{"birthDate": bson.M{"$regex": search, "$options": "i"}},
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

func (ms *MongoStorage) DeleteOrgMembers(orgAddress common.Address, ids []string) (int, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
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
func (ms *MongoStorage) validateOrgMembers(ctx context.Context, orgAddress common.Address, members []string) error {
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
	orgAddress common.Address,
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
