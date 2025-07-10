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

// OrgMembersGroup returns an organization members group
func (ms *MongoStorage) OrganizationMemberGroup(groupID string, orgAddress internal.HexBytes) (*OrganizationMemberGroup, error) {
	objID, err := primitive.ObjectIDFromHex(groupID)
	if err != nil {
		return nil, ErrInvalidData
	}

	filter := bson.M{"_id": objID, "orgAddress": orgAddress}
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
	orgAddress internal.HexBytes,
	page, pageSize int,
) (int, []*OrganizationMemberGroup, error) {
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
func (ms *MongoStorage) CreateOrganizationMemberGroup(group *OrganizationMemberGroup) (string, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	if group == nil || len(group.OrgAddress) == 0 || len(group.MemberIDs) == 0 {
		return "", ErrInvalidData
	}

	// check that the organization exists
	if _, err := ms.fetchOrganizationFromDB(ctx, group.OrgAddress); err != nil {
		if err == ErrNotFound {
			return "", ErrInvalidData
		}
		return "", fmt.Errorf("organization not found: %w", err)
	}
	// check that the members are valid
	err := ms.validateOrgMembers(ctx, group.OrgAddress, group.MemberIDs)
	if err != nil {
		return "", err
	}
	// create the group id
	group.ID = primitive.NewObjectID()
	group.CreatedAt = time.Now()
	group.UpdatedAt = time.Now()
	group.CensusIDs = make([]string, 0)

	// Only lock the mutex during the actual database operations
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	// insert the group into the database
	if _, err := ms.orgMemberGroups.InsertOne(ctx, *group); err != nil {
		return "", fmt.Errorf("could not create organization members group: %w", err)
	}
	return group.ID.Hex(), nil
}

// UpdateOrganizationMemberGroup updates an organization members group by adding
// and/or removing members. If a member exists in both lists, it will be removed
// TODO allow to update the rest of the fields as well. Maybe a different function?
func (ms *MongoStorage) UpdateOrganizationMemberGroup(
	groupID string, orgAddress internal.HexBytes,
	title, description string, addedMembers, removedMembers []string,
) error {
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
	objID, err := primitive.ObjectIDFromHex(groupID)
	if err != nil {
		return err
	}

	// check that the addedMembers contains valid IDs from the orgMembers collection
	if len(addedMembers) > 0 {
		err = ms.validateOrgMembers(ctx, group.OrgAddress, addedMembers)
		if err != nil {
			return err
		}
	}

	filter := bson.M{"_id": objID, "orgAddress": orgAddress}

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
		finalMembers := make([]string, 0, len(updatedGroup.MemberIDs)+len(addedMembers))

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

// DeleteOrganizationMemberGroup deletes an organization member group by its ID
func (ms *MongoStorage) DeleteOrganizationMemberGroup(groupID string, orgAddress internal.HexBytes) error {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	objID, err := primitive.ObjectIDFromHex(groupID)
	if err != nil {
		return fmt.Errorf("invalid group ID: %w", err)
	}

	// Only lock the mutex during the actual database operations
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	// delete the group from the database
	filter := bson.M{"_id": objID, "orgAddress": orgAddress}
	if _, err := ms.orgMemberGroups.DeleteOne(ctx, filter); err != nil {
		return fmt.Errorf("could not delete organization members group: %w", err)
	}
	return nil
}

// ListOrganizationMemberGroup lists all the members of an organization member group and the total number of members
func (ms *MongoStorage) ListOrganizationMemberGroup(
	groupID string, orgAddress internal.HexBytes,
	page, pageSize int64,
) (int, []*OrgMember, error) {
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

// Helper function to check if a string is in a slice
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
