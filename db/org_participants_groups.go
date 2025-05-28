package db

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.vocdoni.io/dvote/log"
)

// OrgParticipantsGroup returns an organization participants group
func (ms *MongoStorage) OrgParticipantsGroup(groupID string) (*OrgParticipantsGroup, error) {
	filter := bson.M{"_id": groupID}
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// find the organization in the database
	result := ms.orgPariticipantsGroups.FindOne(ctx, filter)
	var group *OrgParticipantsGroup
	if err := result.Decode(&group); err != nil {
		// if the organization doesn't exist return a specific error
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return group, nil
}

// OrgParticipantsGroupsByOrg returns an organization's participants groups
func (ms *MongoStorage) OrgAddressGroupsByOrg(orgAddress string) ([]*OrgParticipantsGroup, error) {
	filter := bson.M{"org_address": orgAddress}
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// find the organization in the database
	cursor, err := ms.orgPariticipantsGroups.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	var groups []*OrgParticipantsGroup
	for cursor.Next(ctx) {
		var group OrgParticipantsGroup
		if err := cursor.Decode(&group); err != nil {
			return nil, err
		}
		groups = append(groups, &group)
	}

	if err := cursor.Err(); err != nil {
		return nil, err
	}

	return groups, nil
}

// Creates an organization participants group
func (ms *MongoStorage) CreateOrgParticipantsGroup(orgAddress string, participants []string) error {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// check that the organization exists
	if _, err := ms.fetchOrganizationFromDB(ctx, orgAddress); err != nil {
		if err == ErrNotFound {
			return ErrInvalidData
		}
		return fmt.Errorf("organization not found: %w", err)
	}
	// check that the participants are valid
	err := ms.validateOrgParticipants(ctx, orgAddress, participants)
	if err != nil {
		return err
	}
	// create the group id and group object
	group := OrgParticipantsGroup{
		ID:             primitive.NewObjectID(),
		OrgAddress:     orgAddress,
		ParticipantIDs: participants,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	// Only lock the mutex during the actual database operations
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	// insert the group into the database
	if _, err := ms.orgPariticipantsGroups.InsertOne(ctx, group); err != nil {
		return fmt.Errorf("could not create organization participants group: %w", err)
	}
	return nil
}

// UpdateOrgParticipantsGroup updates an organization participants group by adding
// and/or removing members. If a member exists in both lists, it will be removed
// TODO allow to update the rest of the fields as well. Maybe a different function?
func (ms *MongoStorage) UpdateOrgParticipantsGroup(
	groupID string,
	addedParticipants, removedParticipants []string,
) error {
	group, err := ms.OrgParticipantsGroup(groupID)
	if err != nil {
		if err == ErrNotFound {
			return ErrInvalidData
		}
		return fmt.Errorf("could not retrieve organization participants group: %w", err)
	}
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	objID, err := primitive.ObjectIDFromHex(groupID)
	if err != nil {
		return err
	}

	// check that the addedParticipants contains valid IDs from the orgParticipants collection
	if len(addedParticipants) > 0 {
		err = ms.validateOrgParticipants(ctx, group.OrgAddress, addedParticipants)
		if err != nil {
			return err
		}
	}

	// add the participants to the group
	update := bson.D{}
	if len(addedParticipants) > 0 {
		update = append(update, bson.E{Key: "$addToSet", Value: bson.M{"participanIds": bson.M{"$each": addedParticipants}}})
	}
	// remove the participants from the group
	// if the participant is in both lists, it will be removed
	if len(removedParticipants) > 0 {
		update = append(update, bson.E{Key: "$pull", Value: bson.M{"participanIds": bson.M{"$in": removedParticipants}}})
	}

	if len(update) == 0 {
		return nil // Nothing to do
	}

	// Only lock the mutex during the actual database operations
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	_, err = ms.orgPariticipantsGroups.UpdateOne(ctx, bson.M{"_id": objID}, update)
	return err
}
