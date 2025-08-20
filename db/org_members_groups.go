package db

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// OrgMembersGroup returns an organization members group
func (ms *MongoStorage) OrganizationMemberGroup(
	groupID internal.ObjectID,
	orgAddress common.Address,
) (*OrganizationMemberGroup, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return nil, ErrInvalidData
	}
	if groupID.IsZero() {
		return nil, ErrInvalidData
	}

	filter := bson.M{"_id": groupID, "orgAddress": orgAddress}
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// find the organization in the database
	result := ms.orgMemberGroups.FindOne(ctx, filter)
	var group *OrganizationMemberGroup
	if err := result.Decode(&group); err != nil {
		// if the organization doesn't exist return a specific error
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return group, nil
}

// OrganizationMemberGroups returns the list of an organization's members groups
func (ms *MongoStorage) OrganizationMemberGroups(
	orgAddress common.Address,
	page, pageSize int,
) (int, []*OrganizationMemberGroup, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return 0, nil, ErrInvalidData
	}
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	filter := bson.M{"orgAddress": orgAddress}

	// Count total documents
	totalCount, err := ms.orgMemberGroups.CountDocuments(ctx, filter)
	if err != nil {
		return 0, nil, err
	}
	totalPages := int(math.Ceil(float64(totalCount) / float64(pageSize)))

	// Calculate skip value based on page and pageSize
	skip := (page - 1) * pageSize

	// Set up options for pagination
	findOptions := options.Find().
		SetSkip(int64(skip)).
		SetLimit(int64(pageSize))

	// find the organization in the database
	cursor, err := ms.orgMemberGroups.Find(ctx, filter, findOptions)
	if err != nil {
		return 0, nil, err
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	var groups []*OrganizationMemberGroup
	for cursor.Next(ctx) {
		var group OrganizationMemberGroup
		if err := cursor.Decode(&group); err != nil {
			return 0, nil, err
		}
		groups = append(groups, &group)
	}

	if err := cursor.Err(); err != nil {
		return 0, nil, err
	}

	return totalPages, groups, nil
}

// CreateOrganizationMemberGroup Creates an organization member group
func (ms *MongoStorage) CreateOrganizationMemberGroup(group *OrganizationMemberGroup) (internal.ObjectID, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	if group == nil || group.OrgAddress.Cmp(common.Address{}) == 0 || len(group.MemberIDs) == 0 {
		return internal.NilObjectID, ErrInvalidData
	}

	// check that the organization exists
	if _, err := ms.fetchOrganizationFromDB(ctx, group.OrgAddress); err != nil {
		if err == ErrNotFound {
			return internal.NilObjectID, ErrInvalidData
		}
		return internal.NilObjectID, fmt.Errorf("organization not found: %w", err)
	}
	// check that the members are valid
	err := ms.validateOrgMembers(ctx, group.OrgAddress, group.MemberIDs)
	if err != nil {
		return internal.NilObjectID, err
	}
	// create the group id
	group.ID = internal.NewObjectID()
	group.CreatedAt = time.Now()
	group.UpdatedAt = time.Now()
	group.CensusIDs = make([]internal.ObjectID, 0)

	// Only lock the mutex during the actual database operations
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	// insert the group into the database
	if _, err := ms.orgMemberGroups.InsertOne(ctx, *group); err != nil {
		return internal.NilObjectID, fmt.Errorf("could not create organization members group: %w", err)
	}
	return group.ID, nil
}

// UpdateOrganizationMemberGroup updates an organization members group by adding
// and/or removing members. If a member exists in both lists, it will be removed
// TODO allow to update the rest of the fields as well. Maybe a different function?
func (ms *MongoStorage) UpdateOrganizationMemberGroup(
	groupID internal.ObjectID, orgAddress common.Address,
	title, description string, addedMembers, removedMembers []internal.ObjectID,
) error {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return ErrInvalidData
	}
	if groupID.IsZero() {
		return ErrInvalidData
	}
	group, err := ms.OrganizationMemberGroup(groupID, orgAddress)
	if err != nil {
		if err == ErrNotFound {
			return ErrInvalidData
		}
		return fmt.Errorf("could not retrieve organization members group: %w", err)
	}
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// check that the addedMembers contains valid IDs from the orgMembers collection
	if len(addedMembers) > 0 {
		err = ms.validateOrgMembers(ctx, group.OrgAddress, addedMembers)
		if err != nil {
			return err
		}
	}

	filter := bson.M{"_id": groupID, "orgAddress": orgAddress}

	// Only lock the mutex during the actual database operations
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	// First, update metadata if needed
	if title != "" || description != "" {
		updateFields := bson.M{}
		if title != "" {
			updateFields["title"] = title
		}
		if description != "" {
			updateFields["description"] = description
		}
		updateFields["updatedAt"] = time.Now()

		metadataUpdate := bson.D{{Key: "$set", Value: updateFields}}
		_, err = ms.orgMemberGroups.UpdateOne(ctx, filter, metadataUpdate)
		if err != nil {
			return err
		}
	}

	// Get the updated group to ensure we have the latest state
	updatedGroup, err := ms.OrganizationMemberGroup(groupID, orgAddress)
	if err != nil {
		return err
	}

	// Now handle member updates if needed
	if len(addedMembers) > 0 || len(removedMembers) > 0 {
		// Calculate the final list of members
		finalMembers := make([]internal.ObjectID, 0, len(updatedGroup.MemberIDs)+len(addedMembers))

		// Add existing members that aren't in the removedMembers list
		for _, id := range updatedGroup.MemberIDs {
			if !contains(removedMembers, id) {
				finalMembers = append(finalMembers, id)
			}
		}

		// Add new members that aren't already in the list
		for _, id := range addedMembers {
			if !contains(finalMembers, id) {
				finalMembers = append(finalMembers, id)
			}
		}

		// Update the member list
		memberUpdate := bson.D{{Key: "$set", Value: bson.M{
			"memberIds": finalMembers,
			"updatedAt": time.Now(),
		}}}

		_, err = ms.orgMemberGroups.UpdateOne(ctx, filter, memberUpdate)
		if err != nil {
			return err
		}
	}

	return nil
}

// AddOrganizationMemberGroupCensus adds a census to an organization member group
func (ms *MongoStorage) addOrganizationMemberGroupCensus(
	ctx context.Context, groupID internal.ObjectID, orgAddress common.Address, censusID internal.ObjectID,
) error {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return ErrInvalidData
	}

	// update the group with the census ID
	filter := bson.M{"_id": groupID, "orgAddress": orgAddress}
	update := bson.D{{Key: "$addToSet", Value: bson.M{"censusIds": censusID}}}
	_, err := ms.orgMemberGroups.UpdateOne(ctx, filter, update)
	return err
}

// DeleteOrganizationMemberGroup deletes an organization member group by its ID
func (ms *MongoStorage) DeleteOrganizationMemberGroup(groupID internal.ObjectID, orgAddress common.Address) error {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return ErrInvalidData
	}
	if groupID.IsZero() {
		return ErrInvalidData
	}
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Only lock the mutex during the actual database operations
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	// delete the group from the database
	filter := bson.M{"_id": groupID, "orgAddress": orgAddress}
	if _, err := ms.orgMemberGroups.DeleteOne(ctx, filter); err != nil {
		return fmt.Errorf("could not delete organization members group: %w", err)
	}
	return nil
}

// ListOrganizationMemberGroup lists all the members of an organization member group and the total number of members
func (ms *MongoStorage) ListOrganizationMemberGroup(
	groupID internal.ObjectID, orgAddress common.Address,
	page, pageSize int64,
) (int, []*OrgMember, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return 0, nil, ErrInvalidData
	}
	if groupID.IsZero() {
		return 0, nil, ErrInvalidData
	}
	// get the group
	group, err := ms.OrganizationMemberGroup(groupID, orgAddress)
	if err != nil {
		return 0, nil, fmt.Errorf("could not retrieve organization members group: %w", err)
	}

	return ms.orgMembersByIDs(
		orgAddress,
		group.MemberIDs,
		page,
		pageSize,
	)
}

// CheckOrgMemberAuthFields checks if the provided orgFields are valid for authentication
// Checks the entire member base of an organization creating a projection that contains only
// the provided auth fields and verifies that the resulting data do not have duplicates or
// missing fields. Returns the corrsponding informative errors concerning duplicates or columns with empty values
// The authFields are checked for missing data and duplicates while the twoFaFields are only checked for missing data
func (ms *MongoStorage) CheckGroupMembersFields(
	orgAddress common.Address,
	groupID internal.ObjectID,
	authFields OrgMemberAuthFields,
	twoFaFields OrgMemberTwoFaFields,
) (*OrgMemberAggregationResults, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return nil, ErrInvalidData
	}
	if len(authFields) == 0 && len(twoFaFields) == 0 {
		return nil, fmt.Errorf("no auth or twoFa fields provided")
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// 2) Fetch all matching docs
	cur, err := ms.getGroupMembersFields(ctx, orgAddress, groupID, authFields, twoFaFields)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := cur.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	results := OrgMemberAggregationResults{
		Members:     make([]internal.ObjectID, 0),
		Duplicates:  make([]internal.ObjectID, 0),
		MissingData: make([]internal.ObjectID, 0),
	}

	seenKeys := make(map[string]internal.ObjectID, cur.RemainingBatchLength())
	duplicates := make(map[internal.ObjectID]struct{}, 0)

	// 4) Iterate and detect
	for cur.Next(ctx) {
		// decode into a map so we can handle dynamic fields
		var m OrgMember
		var bm bson.M
		if err := cur.Decode(&m); err != nil {
			return nil, err
		}
		if err := cur.Decode(&bm); err != nil {
			return nil, err
		}

		// if any of the fields are empty, add to missing data
		// and continue to the next member
		// we do not check for duplicates in empty rows
		if hasEmptyFields(bm, authFields) || hasEmptyFields(bm, twoFaFields) {
			results.MissingData = append(results.MissingData, m.ID)
			continue
		}

		// if the key is already seen, add to duplicates
		// and continue to the next member
		if len(authFields) > 0 {
			key := buildKey(bm, authFields)
			if val, seen := seenKeys[key]; seen {
				duplicates[m.ID] = struct{}{}
				duplicates[val] = struct{}{}
				continue
			}
			// neither empty nor duplicate, so we add it to the seen keys
			seenKeys[key] = m.ID
		}

		// if thedata pass all checkss  append the member ID to the results
		results.Members = append(results.Members, m.ID)

	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	results.Duplicates = mapKeysToSlice(duplicates)

	return &results, nil
}

// getGroupMembersAuthFields creates a projection of a set of members that
// contains only the chosen AuthFields
func (ms *MongoStorage) getGroupMembersFields(
	ctx context.Context,
	orgAddress common.Address,
	groupID internal.ObjectID,
	authFields OrgMemberAuthFields,
	twoFaFields OrgMemberTwoFaFields,
) (*mongo.Cursor, error) {
	// 1) Build find filter and projection
	filter := bson.D{
		{Key: "orgAddress", Value: orgAddress},
	}
	// in case a groupID is provided, fetch the group and its members and
	// extend the filter to include only those members
	if !groupID.IsZero() {
		group, err := ms.OrganizationMemberGroup(groupID, orgAddress)
		if err != nil {
			if err == ErrNotFound {
				return nil, fmt.Errorf("group %s not found for organization %s: %w", groupID, orgAddress, ErrInvalidData)
			}
			return nil, fmt.Errorf("failed to fetch group %s for organization %s: %w", groupID, orgAddress, err)
		}
		// Check if the group has members
		if len(group.MemberIDs) == 0 {
			return nil, fmt.Errorf("no members in group %s for organization %s", groupID, orgAddress)
		}
		objectIDs := group.MemberIDs
		if len(objectIDs) > 0 {
			filter = append(filter, bson.E{Key: "_id", Value: bson.M{"$in": objectIDs}})
		}
	}

	proj := bson.D{
		{Key: "_id", Value: 1},
		{Key: "orgAddress", Value: 1},
	}
	// Add the authFields and twoFaFields to the projection
	for _, f := range authFields {
		proj = append(proj, bson.E{Key: string(f), Value: 1})
	}
	for _, f := range twoFaFields {
		proj = append(proj, bson.E{Key: string(f), Value: 1})
	}
	findOpts := options.Find().SetProjection(proj)

	// 2) Fetch all matching docs
	return ms.orgMembers.Find(ctx, filter, findOpts)
}

// Helper function to check if an item is in a slice (generic version)
func contains[T comparable](slice []T, item T) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// mapKeysToSlice extracts all keys from a map as a slice.
func mapKeysToSlice[T comparable, V any](m map[T]V) []T {
	keys := make([]T, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// hasEmptyFields returns true if any of the specified fields in the BSON document are empty or nil.
func hasEmptyFields[T ~string](bm bson.M, fields []T) bool {
	for _, f := range fields {
		val := fmt.Sprint(bm[string(f)])
		if val == "" || bm[string(f)] == nil {
			return true
		}
	}
	return false
}

// buildKey constructs a composite key from the values of specified fields in the BSON document.
// The values are concatenated with "|" as a delimiter.
func buildKey[T ~string](bm bson.M, fields []T) string {
	keyParts := make([]string, len(fields))
	for i, f := range fields {
		keyParts[i] = fmt.Sprint(bm[string(f)])
	}
	return strings.Join(keyParts, "|")
}
