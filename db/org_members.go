package db

import (
	"context"
	"fmt"
	"net/mail"
	"strings"
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

	// check that the org exists
	org, err := ms.Organization(orgMember.OrgAddress)
	if err != nil {
		if err == ErrNotFound {
			return "", ErrInvalidData
		}
		return "", fmt.Errorf("organization not found: %w", err)
	}

	member, errs := prepareOrgMember(org, orgMember, salt, time.Now())
	if len(errs) != 0 {
		return "", fmt.Errorf("%s", strings.Join(errorsAsStrings(errs), ", "))
	}

	updateDoc, err := dynamicUpdateDocument(member, nil)
	if err != nil {
		return "", err
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	filter := bson.M{"_id": member.ID}
	opts := options.Update().SetUpsert(true)
	_, err = ms.orgMembers.UpdateOne(ctx, filter, updateDoc, opts)
	if err != nil {
		return "", err
	}

	return member.ID.Hex(), nil
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

// ErrorsAsStrings returns the errors as a slice of strings
func (j *BulkOrgMembersJob) ErrorsAsStrings() []string {
	return errorsAsStrings(j.Errors)
}

// errorsAsStrings converts a slice of errors to a slice of strings
func errorsAsStrings(errs []error) []string {
	s := []string{}
	for _, err := range errs {
		s = append(s, err.Error())
	}
	return s
}

// prepareOrgMember processes a member for storage by:
//   - Setting the organization address
//   - Setting the creation timestamp
//   - Hashing sensitive data (email, phone, password)
//   - Not including original sensitive data
func prepareOrgMember(org *Organization, m *OrgMember, salt string, currentTime time.Time) (
	*OrgMember, []error,
) {
	member := *m
	var errors []error

	// Assign a new internal ID if not provided
	if member.ID == primitive.NilObjectID {
		member.ID = primitive.NewObjectID()
		member.CreatedAt = currentTime
	} else {
		member.UpdatedAt = currentTime
	}
	member.OrgAddress = org.Address

	// check if mail is valid
	if member.Email != "" {
		if _, err := mail.ParseAddress(member.Email); err != nil {
			errors = append(errors, fmt.Errorf("could not parse email: %s %v", member.Email, err))
			// If email is invalid, set it to empty and store the error
			member.Email = ""
		}
	}

	// Hash phone if present
	if member.PlaintextPhone != "" {
		phone, err := NewHashedPhone(member.PlaintextPhone, org)
		if err != nil {
			errors = append(errors, fmt.Errorf("invalid phone %q: %w", member.PlaintextPhone, err))
		} else {
			member.Phone = phone
		}
		member.PlaintextPhone = ""
	}

	// Hash password if present
	if member.Password != "" {
		member.HashedPass = internal.HashPassword(salt, member.Password)
		member.Password = ""
	}

	// Check that the birthdate is valid
	if len(member.BirthDate) > 0 {
		var err error
		member.ParsedBirthDate, member.BirthDate, err = internal.ParseBirthDate(member.BirthDate)
		if err != nil {
			errors = append(errors, err)
			member.BirthDate = "" // Reset invalid birthdate
			member.ParsedBirthDate = time.Time{}
		}
	}
	return &member, errors
}

// createOrgMemberBulkOperations creates a batch of members using bulk write operations,
// and returns the number of members added (or updated) and any errors encountered.
func (ms *MongoStorage) createOrgMemberBulkOperations(
	org *Organization,
	members []*OrgMember,
	salt string,
	currentTime time.Time,
) (int, []error) {
	var bulkOps []mongo.WriteModel
	var errors []error

	for _, m := range members {
		// Prepare the member
		member, validationErrors := prepareOrgMember(org, m, salt, currentTime)
		errors = append(errors, validationErrors...)

		// Create filter for existing members and update document
		filter := bson.M{
			"_id":        member.ID,
			"orgAddress": member.OrgAddress,
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
	org *Organization,
	orgMembers []*OrgMember,
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
			org,
			orgMembers[start:end],
			salt,
			currentTime,
		)

		// Update job stats
		job = BulkOrgMembersJob{
			Progress: int(float64(job.Added+added) / float64(job.Total) * 100),
			Total:    job.Total,
			Added:    job.Added + added,
			Errors:   append(job.Errors, errs...),
		}
	}
}

// SetBulkOrgMembers adds multiple organization members to the database in batches of 200 entries
// and updates already existing members (decided by combination of internal id and orgAddress).
// Requires an existing organization.
// Returns a channel that sends the percentage of members processed every 10 seconds.
// This function must be called in a goroutine.
func (ms *MongoStorage) SetBulkOrgMembers(org *Organization, members []*OrgMember, salt string,
) (chan *BulkOrgMembersJob, error) {
	// Early returns for invalid input
	if len(members) == 0 {
		return nil, nil // Not an error, just no work to do
	}
	if org.Address.Cmp(common.Address{}) == 0 {
		return nil, ErrInvalidData
	}

	// Start processing in a goroutine
	progressChan := make(chan *BulkOrgMembersJob, 10)
	go ms.processOrgMemberBatches(org, members, salt, progressChan)
	return progressChan, nil
}

// OrgMembers retrieves paginated orgMembers for an organization from the DB
func (ms *MongoStorage) OrgMembers(orgAddress common.Address, page, limit int64, search string) (int64, []*OrgMember, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return 0, nil, ErrInvalidData
	}

	// Create filter
	filter := bson.M{
		"orgAddress": orgAddress,
	}
	if len(search) > 0 {
		filter["$or"] = []bson.M{
			{"email": bson.M{"$regex": search, "$options": "i"}},
			{"memberNumber": bson.M{"$regex": search, "$options": "i"}},
			{"nationalId": bson.M{"$regex": search, "$options": "i"}},
			{"name": bson.M{"$regex": search, "$options": "i"}},
			{"surname": bson.M{"$regex": search, "$options": "i"}},
			{"birthDate": bson.M{"$regex": search, "$options": "i"}},
		}
	}

	findOptions := options.Find().
		SetSort(bson.D{
			{Key: "name", Value: 1},
			{Key: "surname", Value: 1},
		})

	return paginatedDocuments[*OrgMember](ms.orgMembers, page, limit, filter, findOptions)
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

	// Convert ObjectIDs to string IDs for group updates (groups store member IDs as strings)
	var stringIDs []string
	for _, oid := range oids {
		stringIDs = append(stringIDs, oid.Hex())
	}

	// Update all groups to remove the deleted member IDs from their MemberIDs arrays
	groupFilter := bson.M{
		"orgAddress": orgAddress,
		"memberIds": bson.M{
			"$in": stringIDs,
		},
	}

	// Use $pull to remove the deleted member IDs from all groups that contain them
	groupUpdate := bson.M{
		"$pull": bson.M{
			"memberIds": bson.M{
				"$in": stringIDs,
			},
		},
		"$set": bson.M{
			"updatedAt": time.Now(),
		},
	}

	_, err = ms.orgMemberGroups.UpdateMany(ctx, groupFilter, groupUpdate)
	if err != nil {
		return 0, fmt.Errorf("failed to update groups after deleting orgMembers: %w", err)
	}

	return int(result.DeletedCount), nil
}

// DeleteAllOrgMembers removes all members from an organization
func (ms *MongoStorage) DeleteAllOrgMembers(orgAddress common.Address) (int, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return 0, ErrInvalidData
	}

	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// create the filter for the delete operation - only organization address
	filter := bson.M{
		"orgAddress": orgAddress,
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	result, err := ms.orgMembers.DeleteMany(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("failed to delete all orgMembers: %w", err)
	}

	// Update all groups to remove the deleted member IDs
	groupFilter := bson.M{
		"orgAddress": orgAddress,
	}
	groupUpdate := bson.M{
		"$set": bson.M{
			"memberIds": []string{},
			"updatedAt": time.Now(),
		},
	}

	_, err = ms.orgMemberGroups.UpdateMany(ctx, groupFilter, groupUpdate)
	if err != nil {
		return 0, fmt.Errorf("failed to update groups after deleting all orgMembers: %w", err)
	}

	return int(result.DeletedCount), nil
}

// GetAllOrgMemberIDs retrieves all member IDs for an organization
func (ms *MongoStorage) GetAllOrgMemberIDs(orgAddress common.Address) ([]string, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return nil, ErrInvalidData
	}

	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Create filter for the organization
	filter := bson.M{
		"orgAddress": orgAddress,
	}

	// Only select the _id field for efficiency
	projection := bson.M{
		"_id": 1,
	}

	cursor, err := ms.orgMembers.Find(ctx, filter, options.Find().SetProjection(projection))
	if err != nil {
		return nil, fmt.Errorf("failed to find org members: %w", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	var memberIDs []string
	for cursor.Next(ctx) {
		var member struct {
			ID primitive.ObjectID `bson:"_id"`
		}
		if err := cursor.Decode(&member); err != nil {
			return nil, fmt.Errorf("failed to decode member ID: %w", err)
		}
		memberIDs = append(memberIDs, member.ID.Hex())
	}

	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("cursor error: %w", err)
	}

	return memberIDs, nil
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
	page, limit int64,
) (int64, []*OrgMember, error) {
	if len(memberIDs) == 0 {
		return 0, nil, nil // No members to retrieve
	}

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

	return paginatedDocuments[*OrgMember](ms.orgMembers, page, limit, filter, options.Find())
}
