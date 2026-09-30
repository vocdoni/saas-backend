// Package db provides database operations for the Vocdoni SaaS backend,
// handling storage and retrieval of censuses, organizations, users, and
// other data structures required for the voting platform.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// SetCensus creates a new census for an organization
// Returns the hex representation of the census
func (ms *MongoStorage) SetCensus(census *Census) (string, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	if census.OrgAddress.Cmp(common.Address{}) == 0 {
		return "", ErrInvalidData
	}
	// check that the org exists
	_, err := ms.Organization(census.OrgAddress)
	if err != nil {
		if err == ErrNotFound {
			return "", ErrInvalidData
		}
		return "", fmt.Errorf("organization not found: %w", err)
	}

	if census.ID != bson.NilObjectID {
		// if the census exists, update it with the new data
		census.UpdatedAt = time.Now()
	} else {
		// if the census doesn't exist, create its id
		census.ID = bson.NewObjectID()
		census.CreatedAt = time.Now()
	}
	census.Type = census.TwoFaFields.GetCensusType()

	updateDoc, err := dynamicUpdateDocument(census, nil)
	if err != nil {
		return "", err
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	filter := bson.M{"_id": census.ID}
	opts := options.UpdateOne().SetUpsert(true)
	_, err = ms.censuses.UpdateOne(ctx, filter, updateDoc, opts)
	if err != nil {
		return "", err
	}

	return census.ID.Hex(), nil
}

// PopulateGroupCensus stores the census and adds every member of the group as a participant,
// returning the number added and the members left out for missing required auth data (they could
// never log in). Members that could not be told apart from another member or participant of the
// census (same login hash) are refused with a *CensusMembersError before anything is written.
func (ms *MongoStorage) PopulateGroupCensus(census *Census, groupID string) (int64, []bson.ObjectID, error) {
	if census.OrgAddress.Cmp(common.Address{}) == 0 {
		return 0, nil, ErrInvalidData
	}
	group, err := ms.OrganizationMemberGroup(groupID, census.OrgAddress)
	if err != nil {
		if err == ErrNotFound {
			return 0, nil, ErrInvalidData
		}
		return 0, nil, fmt.Errorf("error retrieving organization group: %w", err)
	}
	filter, err := groupMembersFilter(group)
	if err != nil {
		return 0, nil, err
	}
	return ms.populateCensus(census, filter, group)
}

// PopulateMembersCensus is PopulateGroupCensus for an explicit set of members of the census
// organization. IDs matching no member of the organization are ignored.
func (ms *MongoStorage) PopulateMembersCensus(census *Census, memberIDs []string) (int64, []bson.ObjectID, error) {
	if census.OrgAddress.Cmp(common.Address{}) == 0 {
		return 0, nil, ErrInvalidData
	}
	filter, err := membersFilter(census.OrgAddress, memberIDs)
	if err != nil {
		return 0, nil, err
	}
	return ms.populateCensus(census, filter, nil)
}

// populateCensus stores the census with the org members matching the filter as participants,
// linking it to the group when one is given. See PopulateGroupCensus.
func (ms *MongoStorage) populateCensus(
	census *Census,
	membersFilter bson.D,
	group *OrganizationMemberGroup,
) (int64, []bson.ObjectID, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// check that the org exists
	if _, err := ms.Organization(census.OrgAddress); err != nil {
		if err == ErrNotFound {
			return 0, nil, ErrInvalidData
		}
		return 0, nil, fmt.Errorf("error retrieving organization: %w", err)
	}

	if census.ID != bson.NilObjectID {
		// if the census exists, update it with the new data
		census.UpdatedAt = time.Now()
	} else {
		// if the census doesn't exist, create its id
		census.ID = bson.NewObjectID()
		census.CreatedAt = time.Now()
	}
	census.Type = census.TwoFaFields.GetCensusType()

	// participants already in the census hold login hashes too
	existing, err := ms.CensusParticipants(census.ID.Hex())
	if err != nil {
		return 0, nil, fmt.Errorf("error retrieving census participants: %w", err)
	}
	cur, err := ms.findMemberFieldsCursor(ctx, membersFilter, census.AuthFields, census.TwoFaFields)
	if err != nil {
		return 0, nil, fmt.Errorf("error retrieving census members: %w", err)
	}
	defer func() {
		if err := cur.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()
	members, err := classifyCensusMembers(ctx, cur, *census, existing)
	if err != nil {
		return 0, nil, fmt.Errorf("error retrieving census members: %w", err)
	}
	// members sharing a login hash would fail the unique login-hash index halfway through the build:
	// refuse them up front, naming them, before the group is linked
	if dups := members.duplicates(); len(dups) > 0 {
		return 0, nil, &CensusMembersError{Duplicates: dups}
	}

	if group != nil {
		census.GroupID = group.ID
		if err := ms.addOrganizationMemberGroupCensus(ctx, group.ID.Hex(), census.OrgAddress, census.ID.Hex()); err != nil {
			return 0, nil, fmt.Errorf("error updating group with census ID: %w", err)
		}
	}

	insertedCount, err := ms.setBulkCensusParticipant(ctx, census, members.complete)
	if err != nil {
		return 0, nil, fmt.Errorf("error setting census participants: %w", err)
	}
	census.Size = insertedCount

	updateDoc, err := dynamicUpdateDocument(census, nil)
	if err != nil {
		return 0, nil, err
	}
	filter := bson.M{"_id": census.ID}
	opts := options.UpdateOne().SetUpsert(true)
	if _, err := ms.censuses.UpdateOne(ctx, filter, updateDoc, opts); err != nil {
		return 0, nil, err
	}
	return census.Size, members.missing, nil
}

// DeleteCensus removes a census and all its members
func (ms *MongoStorage) DelCensus(censusID string) error {
	objID, err := bson.ObjectIDFromHex(censusID)
	if err != nil {
		return ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// delete the census participants first so removing a census does not orphan them (this
	// is done inline rather than via DeleteCensusParticipantsByCensus, which takes keysLock).
	if _, err := ms.censusParticipants.DeleteMany(ctx, bson.M{"censusId": censusID}); err != nil { //nolint:goconst
		return fmt.Errorf("failed to delete census participants: %w", err)
	}
	// delete the census from the database using the ID
	if _, err := ms.censuses.DeleteOne(ctx, bson.M{"_id": objID}); err != nil {
		return fmt.Errorf("failed to delete census: %w", err)
	}
	return nil
}

// Census retrieves a census from the DB based on its ID
func (ms *MongoStorage) Census(censusID string) (*Census, error) {
	objID, err := bson.ObjectIDFromHex(censusID)
	if err != nil {
		return nil, ErrInvalidData
	}

	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	census := &Census{}
	err = ms.censuses.FindOne(ctx, bson.M{"_id": objID}).Decode(census)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}

		return nil, fmt.Errorf("failed to get census: %w", err)
	}

	return census, nil
}

// CensusesByOrg retrieves all the censuses for an organization based on its
// address. It checks that the organization exists and returns an error if it
// doesn't. If the organization exists, it returns the censuses.
func (ms *MongoStorage) CensusesByOrg(orgAddress common.Address) ([]*Census, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	if _, err := ms.fetchOrganizationFromDB(ctx, orgAddress); err != nil {
		if err == ErrNotFound {
			return nil, ErrInvalidData
		}
		return nil, fmt.Errorf("organization not found: %w", err)
	}
	// find the censuses in the database
	censuses := []*Census{}
	cursor, err := ms.censuses.Find(ctx, bson.M{"orgAddress": orgAddress})
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()
	if err := cursor.All(ctx, &censuses); err != nil {
		return nil, err
	}
	return censuses, nil
}
