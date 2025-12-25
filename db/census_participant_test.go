package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const testParticipantID = "member123"

func setupTestCensusParticipantPrerequisites(t *testing.T, memberSuffix string) (*OrgMember, *Census) {
	t.Helper()
	// Create test organization
	org := &Organization{
		Address:   testOrgAddress,
		Active:    true,
		CreatedAt: time.Now(),
	}

	err := testDB.SetOrganization(org)
	if err != nil {
		t.Fatalf("failed to set organization: %v", err)
	}

	// Create test member with unique ID
	memberNumber := testParticipantID + memberSuffix
	member := &OrgMember{
		ID:           primitive.NewObjectID(),
		OrgAddress:   testOrgAddress,
		MemberNumber: memberNumber,
		Email:        "test" + memberSuffix + "@example.com",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	_, err = testDB.SetOrgMember("test_salt", member)
	if err != nil {
		t.Fatalf("failed to set organization member: %v", err)
	}

	// Create test census
	census := &Census{
		OrgAddress:  testOrgAddress,
		AuthFields:  OrgMemberAuthFields{OrgMemberAuthFieldsMemberNumber, OrgMemberAuthFieldsName},
		TwoFaFields: OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail},
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	censusID, err := testDB.SetCensus(census)
	if err != nil {
		t.Fatalf("failed to set census: %v", err)
	}

	oid, err := primitive.ObjectIDFromHex(censusID)
	if err != nil {
		t.Fatalf("failed to ObjectIDFromHex: %v", err)
	}
	census.ID = oid

	return member, census
}

func TestCensusParticipant(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	t.Run("SetCensusParticipant", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		// Setup prerequisites
		member, census := setupTestCensusParticipantPrerequisites(t, "_set")

		// Test creating a new participant
		participant := &CensusParticipant{
			ParticipantID: member.ID.Hex(),
			CensusID:      census.ID.Hex(),
		}

		// Test with invalid data
		t.Run("InvalidData", func(_ *testing.T) {
			invalidParticipant := &CensusParticipant{
				ParticipantID: "",
				CensusID:      census.ID.Hex(),
			}
			err := testDB.SetCensusParticipant(invalidParticipant)
			c.Assert(err, qt.Equals, ErrInvalidData)

			invalidParticipant = &CensusParticipant{
				ParticipantID: testParticipantID,
				CensusID:      "",
			}
			err = testDB.SetCensusParticipant(invalidParticipant)
			c.Assert(err, qt.Equals, ErrInvalidData)
		})

		t.Run("NonExistentCensus", func(_ *testing.T) {
			nonExistentParticipant := &CensusParticipant{
				ParticipantID: testParticipantID,
				CensusID:      primitive.NewObjectID().Hex(),
			}
			err := testDB.SetCensusParticipant(nonExistentParticipant)
			c.Assert(err, qt.Not(qt.IsNil))
		})

		t.Run("NonExistentMember", func(_ *testing.T) {
			nonExistentParticipantID := &CensusParticipant{
				ParticipantID: "non-existent",
				CensusID:      census.ID.Hex(),
			}
			err := testDB.SetCensusParticipant(nonExistentParticipantID)
			c.Assert(err, qt.Not(qt.IsNil))
		})

		t.Run("CreateAndUpdate", func(_ *testing.T) {
			// Create new participant
			err := testDB.SetCensusParticipant(participant)
			c.Assert(err, qt.IsNil)

			// Verify the participant was created correctly
			createdParticipant, err := testDB.CensusParticipant(census.ID.Hex(), member.ID.Hex())
			c.Assert(err, qt.IsNil)
			c.Assert(createdParticipant.CensusID, qt.Equals, census.ID.Hex())
			c.Assert(createdParticipant.CreatedAt.IsZero(), qt.IsFalse)
			c.Assert(createdParticipant.UpdatedAt.IsZero(), qt.IsFalse)

			// Test updating an existing participant
			time.Sleep(time.Millisecond) // Ensure different UpdatedAt timestamp
			err = testDB.SetCensusParticipant(participant)
			c.Assert(err, qt.IsNil)

			// Verify the participant was updated correctly
			updatedParticipant, err := testDB.CensusParticipant(census.ID.Hex(), member.ID.Hex())
			c.Assert(err, qt.IsNil)
			c.Assert(updatedParticipant.CensusID, qt.Equals, census.ID.Hex())
			c.Assert(updatedParticipant.CreatedAt, qt.Equals, createdParticipant.CreatedAt)
			c.Assert(updatedParticipant.UpdatedAt.After(createdParticipant.UpdatedAt), qt.IsTrue)
		})
	})

	t.Run("GetCensusParticipant", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		// Setup prerequisites
		member, census := setupTestCensusParticipantPrerequisites(t, "_get")
		participantID := testParticipantID + "_get"

		t.Run("InvalidData", func(_ *testing.T) {
			// Test getting participant with invalid data
			_, err := testDB.CensusParticipant("", member.ID.Hex())
			c.Assert(err, qt.Equals, ErrInvalidData)

			_, err = testDB.CensusParticipant(census.ID.Hex(), "")
			c.Assert(err, qt.Equals, ErrInvalidData)
		})

		t.Run("NonExistentParticipant", func(_ *testing.T) {
			// Test getting non-existent participant
			_, err := testDB.CensusParticipant(census.ID.Hex(), participantID)
			c.Assert(err, qt.Equals, ErrNotFound)
		})

		t.Run("ExistingParticipant", func(_ *testing.T) {
			// Create a participant to retrieve
			participant := &CensusParticipant{
				ParticipantID: member.ID.Hex(),
				CensusID:      census.ID.Hex(),
			}
			err := testDB.SetCensusParticipant(participant)
			c.Assert(err, qt.IsNil)

			// Test getting existing participant
			retrievedParticipant, err := testDB.CensusParticipant(census.ID.Hex(), member.ID.Hex())
			c.Assert(err, qt.IsNil)
			c.Assert(retrievedParticipant.CensusID, qt.Equals, census.ID.Hex())
			c.Assert(retrievedParticipant.CreatedAt.IsZero(), qt.IsFalse)
			c.Assert(retrievedParticipant.UpdatedAt.IsZero(), qt.IsFalse)
		})
	})

	t.Run("DeleteCensusParticipant", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		// Setup prerequisites
		member, census := setupTestCensusParticipantPrerequisites(t, "_delete")

		t.Run("InvalidData", func(_ *testing.T) {
			// Test deleting with invalid data
			err := testDB.DelCensusParticipant("", member.ID.Hex())
			c.Assert(err, qt.Equals, ErrInvalidData)

			err = testDB.DelCensusParticipant(census.ID.Hex(), "")
			c.Assert(err, qt.Equals, ErrInvalidData)
		})

		t.Run("ExistingParticipant", func(_ *testing.T) {
			// Create a participant to delete
			participant := &CensusParticipant{
				ParticipantID: member.ID.Hex(),
				CensusID:      census.ID.Hex(),
			}
			err := testDB.SetCensusParticipant(participant)
			c.Assert(err, qt.IsNil)

			// Test deleting existing participant
			err = testDB.DelCensusParticipant(census.ID.Hex(), member.ID.Hex())
			c.Assert(err, qt.IsNil)

			// Verify the participant was deleted
			_, err = testDB.CensusParticipant(census.ID.Hex(), member.ID.Hex())
			c.Assert(err, qt.Equals, ErrNotFound)
		})

		t.Run("NonExistentParticipant", func(_ *testing.T) {
			// Test deleting non-existent participant (should not error)
			err := testDB.DelCensusParticipant(census.ID.Hex(), member.ID.Hex())
			c.Assert(err, qt.IsNil)
		})
	})

	t.Run("BulkCensusParticipant", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		// Setup prerequisites
		_, census := setupTestCensusParticipantPrerequisites(t, "_bulk")

		t.Run("EmptyMembers", func(_ *testing.T) {
			// Test with empty members
			progressChan, err := testDB.SetBulkCensusOrgMemberParticipant(testOrg, "test_salt", census.ID.Hex(), nil)
			c.Assert(err, qt.IsNil)

			// Channel should be closed immediately for empty members
			_, open := <-progressChan
			c.Assert(open, qt.IsFalse)
		})

		t.Run("InvalidData", func(_ *testing.T) {
			// Test with empty census ID
			members := []*OrgMember{
				{
					MemberNumber:   "test1",
					Email:          "test1@example.com",
					PlaintextPhone: "1234567890",
					Password:       "password1",
				},
			}
			progressChan, err := testDB.SetBulkCensusOrgMemberParticipant(testOrg, "test_salt", "", members)
			c.Assert(err, qt.Equals, ErrInvalidData)

			// Channel should be closed immediately for invalid data
			_, open := <-progressChan
			c.Assert(open, qt.IsFalse)
		})

		t.Run("NonExistentCensus", func(_ *testing.T) {
			members := []*OrgMember{
				{
					MemberNumber:   "test1",
					Email:          "test1@example.com",
					PlaintextPhone: "1234567890",
					Password:       "password1",
				},
			}
			// Test with non-existent census
			progressChan, err := testDB.SetBulkCensusOrgMemberParticipant(testOrg, "test_salt", primitive.NewObjectID().Hex(), members)
			c.Assert(err, qt.Not(qt.IsNil))

			// Channel should be closed immediately for non-existent census
			_, open := <-progressChan
			c.Assert(open, qt.IsFalse)
		})

		t.Run("SuccessfulBulkCreation", func(_ *testing.T) {
			// Test successful bulk creation
			members := []*OrgMember{
				{
					MemberNumber:   "test1",
					Email:          "test1@example.com",
					PlaintextPhone: "+34698111111",
					Password:       "password1",
				},
				{
					MemberNumber:   "test2",
					Email:          "test2@example.com",
					PlaintextPhone: "+34698222222",
					Password:       "password2",
				},
			}

			progressChan, err := testDB.SetBulkCensusOrgMemberParticipant(testOrg, "test_salt", census.ID.Hex(), members)
			c.Assert(err, qt.IsNil)
			c.Assert(progressChan, qt.Not(qt.IsNil))

			// Wait for the operation to complete by draining the channel
			var lastProgress *BulkCensusParticipantStatus
			for progress := range progressChan {
				lastProgress = progress
			}
			// Final progress should be 100%
			c.Assert(lastProgress.Progress, qt.Equals, 100)

			// Verify members were created with hashed data
			for _, p := range members {
				member, err := testDB.OrgMemberByMemberNumber(testOrg.Address, p.MemberNumber)
				c.Assert(err, qt.IsNil)
				c.Assert(member.Email, qt.Not(qt.Equals), "")
				c.Assert(member.Phone.Bytes(), qt.DeepEquals, internal.HashOrgData(testOrg.Address, p.PlaintextPhone))
				c.Assert(member.Password, qt.Equals, "")
				c.Assert(member.HashedPass, qt.Not(qt.Equals), "")
				c.Assert(member.CreatedAt.IsZero(), qt.IsFalse)

				// Verify participants were created
				participant, err := testDB.CensusParticipant(census.ID.Hex(), member.ID.Hex())
				c.Assert(err, qt.IsNil)
				c.Assert(participant.CensusID, qt.Equals, census.ID.Hex())
				c.Assert(participant.CreatedAt.IsZero(), qt.IsFalse)
			}
		})

		t.Run("UpdateExistingMembers", func(_ *testing.T) {
			// Create members first
			members := []*OrgMember{
				{
					MemberNumber:   "update1",
					Email:          "update1@example.com",
					PlaintextPhone: "+34698123456",
					Password:       "password1",
				},
				{
					MemberNumber:   "update2",
					Email:          "update2@example.com",
					PlaintextPhone: "+34698654321",
					Password:       "password2",
				},
			}

			// Create initial members
			progressChan, err := testDB.SetBulkCensusOrgMemberParticipant(testOrg, "test_salt", census.ID.Hex(), members)
			c.Assert(err, qt.IsNil)
			c.Assert(progressChan, qt.Not(qt.IsNil))

			// Wait for the operation to complete
			//revive:disable:empty-block
			for range progressChan {
				// Just drain the channel
			}
			//revive:enable:empty-block

			// Test updating existing members and participants
			// set first their internal ID correctly
			member0, err := testDB.OrgMemberByMemberNumber(testOrgAddress, members[0].MemberNumber)
			c.Assert(err, qt.IsNil)
			members[0].ID = member0.ID
			members[0].Email = "updated1@example.com"
			member1, err := testDB.OrgMemberByMemberNumber(testOrgAddress, members[1].MemberNumber)
			c.Assert(err, qt.IsNil)
			members[1].ID = member1.ID
			members[1].PlaintextPhone = "+34698111111"

			progressChan, err = testDB.SetBulkCensusOrgMemberParticipant(testOrg, "test_salt", census.ID.Hex(), members)
			c.Assert(err, qt.IsNil)
			c.Assert(progressChan, qt.Not(qt.IsNil))

			// Wait for the operation to complete
			var lastProgress *BulkCensusParticipantStatus
			for progress := range progressChan {
				lastProgress = progress
			}
			// Final progress should be 100%
			c.Assert(lastProgress.Progress, qt.Equals, 100)

			// Verify updates
			for _, p := range members {
				member, err := testDB.OrgMemberByMemberNumber(testOrgAddress, p.MemberNumber)
				c.Assert(err, qt.IsNil)
				c.Assert(member.Email, qt.Equals, p.Email)
				c.Assert(member.Phone.Bytes(), qt.DeepEquals, internal.HashOrgData(testOrgAddress, p.PlaintextPhone))
			}
		})
	})

	t.Run("CensusParticipantByLoginHash", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		// Setup prerequisites
		member, census := setupTestCensusParticipantPrerequisites(t, "_loginHash")

		// Update the member with additional information for testing login hash
		member.Name = "Test User"
		_, err := testDB.SetOrgMember("test_salt", member)
		c.Assert(err, qt.IsNil)

		// Generate login hash
		loginHash := HashAuthTwoFaFields(*member, census.AuthFields, census.TwoFaFields)
		c.Assert(loginHash, qt.Not(qt.IsNil))

		// Create participant with login hash
		participant := &CensusParticipant{
			ParticipantID: member.ID.Hex(),
			CensusID:      census.ID.Hex(),
			LoginHash:     loginHash,
		}
		err = testDB.SetCensusParticipant(participant)
		c.Assert(err, qt.IsNil)

		t.Run("InvalidData", func(_ *testing.T) {
			// Test with empty login hash
			censusWithNilID := *census
			censusWithNilID.ID = primitive.NilObjectID
			_, err := testDB.CensusParticipantByLoginHash(censusWithNilID, *member)
			c.Assert(err, qt.Equals, ErrInvalidData)
		})

		t.Run("CensusWithNoAuthNorTwoFa", func(_ *testing.T) {
			// Test with empty login hash
			censusWithNoFields := *census
			censusWithNoFields.AuthFields = nil
			censusWithNoFields.TwoFaFields = nil
			_, err := testDB.CensusParticipantByLoginHash(censusWithNoFields, *member)
			c.Assert(err, qt.ErrorMatches, ErrInvalidData.Error()+".*")
		})

		t.Run("NonExistentParticipant", func(_ *testing.T) {
			// Test with non-existent login hash
			nonExistentParticipant := *member
			nonExistentParticipant.Name = "nonExistent"
			_, err := testDB.CensusParticipantByLoginHash(*census, nonExistentParticipant)
			c.Assert(err, qt.Equals, ErrNotFound)
		})

		t.Run("ExistingParticipant", func(_ *testing.T) {
			// Test successful retrieval
			retrievedParticipant, err := testDB.CensusParticipantByLoginHash(*census, *member)
			c.Assert(err, qt.IsNil)
			c.Assert(retrievedParticipant.CensusID, qt.Equals, census.ID.Hex())
			c.Assert(retrievedParticipant.ParticipantID, qt.Equals, member.ID.Hex())
		})
	})

	t.Run("SetBulkCensusParticipant", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		// Create organization and census
		org := &Organization{
			Address:   testOrgAddress,
			Active:    true,
			CreatedAt: time.Now(),
		}
		err := testDB.SetOrganization(org)
		c.Assert(err, qt.IsNil)

		// Create census
		census := &Census{
			OrgAddress:  testOrgAddress,
			Type:        CensusTypeMail,
			AuthFields:  OrgMemberAuthFields{OrgMemberAuthFieldsMemberNumber, OrgMemberAuthFieldsName},
			TwoFaFields: OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail, OrgMemberTwoFaFieldPhone},
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}
		censusID, err := testDB.SetCensus(census)
		c.Assert(err, qt.IsNil)

		// Create members first
		members := make([]*OrgMember, 0, 3)
		for i := range 3 {
			member := &OrgMember{
				ID:             primitive.NewObjectID(),
				OrgAddress:     testOrgAddress,
				MemberNumber:   fmt.Sprintf("bulk-login-%d", i),
				Name:           fmt.Sprintf("Bulk User %d", i),
				Email:          fmt.Sprintf("bulk%d@example.com", i),
				PlaintextPhone: fmt.Sprintf("+3469811111%d", i),
				CreatedAt:      time.Now(),
				UpdatedAt:      time.Now(),
			}
			_, err := testDB.SetOrgMember("test_salt", member)
			c.Assert(err, qt.IsNil)

			members = append(members, member)
		}

		// Create members group with the members
		group := &OrganizationMemberGroup{
			OrgAddress: testOrgAddress,
			Title:      "Test Group",
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			MemberIDs: []string{
				members[0].ID.Hex(),
				members[1].ID.Hex(),
				members[2].ID.Hex(),
			},
		}
		groupID, err := testDB.CreateOrganizationMemberGroup(group)
		c.Assert(err, qt.IsNil)

		// Update census with group ID
		objID, err := primitive.ObjectIDFromHex(groupID)
		c.Assert(err, qt.IsNil)
		census.GroupID = objID
		_, err = testDB.SetCensus(census)
		c.Assert(err, qt.IsNil)

		// Test setBulkCensusParticipant
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		upsertCount, err := testDB.setBulkCensusParticipant(ctx, census, groupID)
		c.Assert(err, qt.IsNil)
		c.Assert(upsertCount, qt.Equals, int64(3))

		// Get all participants
		participants, err := testDB.CensusParticipants(censusID)
		c.Assert(err, qt.IsNil)
		c.Assert(participants, qt.HasLen, 3)

		// Verify login hash exists for each participant
		for i, participant := range participants {
			c.Assert(participant.LoginHash, qt.Not(qt.IsNil))

			// Verify we can retrieve participant by login hash
			found, err := testDB.CensusParticipantByLoginHash(*census, *members[i])
			c.Assert(err, qt.IsNil)
			c.Assert(found.ParticipantID, qt.Equals, participant.ParticipantID)
		}

		// Now try to update the member in a way that would produce a duplicate, this must fail
		member0, err := testDB.OrgMemberByMemberNumber(testOrgAddress, members[0].MemberNumber)
		c.Assert(err, qt.IsNil)
		// set member0 email and phone same as member1
		member0.Name = members[1].Name
		member0.MemberNumber = members[1].MemberNumber
		member0.Email = members[1].Email
		member0.PlaintextPhone = members[1].PlaintextPhone

		{
			_, err := testDB.UpsertOrgMemberAndCensusParticipants(testOrg, member0, "test_salt")
			c.Assert(err, qt.ErrorMatches, ".*update would create duplicates.*",
				qt.Commentf("trying to UpdateOrgMember(%+v) should create a conflict with %+v", member0, members[1]))

			member, err := testDB.OrgMemberByMemberNumber(testOrgAddress, members[0].MemberNumber)
			c.Assert(err, qt.IsNil)
			c.Assert(member.Email, qt.Equals, members[0].Email)
		}

		// second try to update in a way that would NOT produce a duplicate, should succeed
		member1, err := testDB.OrgMemberByMemberNumber(testOrgAddress, members[1].MemberNumber)
		c.Assert(err, qt.IsNil)
		oldHashedPhone := member1.Phone
		member1.PlaintextPhone = "+34698123321"
		{
			_, err := testDB.UpsertOrgMemberAndCensusParticipants(testOrg, member1, "test_salt")
			c.Assert(err, qt.IsNil)

			member, err := testDB.OrgMemberByMemberNumber(testOrgAddress, members[1].MemberNumber)
			c.Assert(err, qt.IsNil)
			c.Assert(member.Phone, qt.Not(qt.DeepEquals), oldHashedPhone)
		}

		// Trying to add a NEW member with the same details that caused a conflict should also succeed,
		// since duplicates in memberbase are allowed, and a new member is not part of any census
		{
			member0.ID = primitive.NilObjectID
			newMemberID, err := testDB.UpsertOrgMemberAndCensusParticipants(testOrg, member0, "test_salt")
			c.Assert(err, qt.IsNil)
			member, err := testDB.OrgMember(testOrgAddress, newMemberID.Hex())
			c.Assert(err, qt.IsNil)
			c.Assert(member.Email, qt.Equals, member0.Email)
		}

		// Passing an arbitrary (new) memberID should also work OK and create a new member
		{
			member0.ID = primitive.NewObjectID()
			newMemberID, err := testDB.UpsertOrgMemberAndCensusParticipants(testOrg, member0, "test_salt")
			c.Assert(err, qt.IsNil)
			member, err := testDB.OrgMember(testOrgAddress, newMemberID.Hex())
			c.Assert(err, qt.IsNil)
			c.Assert(member.Email, qt.Equals, member0.Email)
		}
	})
}

// TestCreateCensusParticipantBulkOperationsFiltering specifically tests the filtering functionality
// in the createCensusParticipantBulkOperations function, focusing on ensuring
// that "participantID": orgMember.ID.Hex() works correctly for upserts
func TestCreateCensusParticipantBulkOperationsFiltering(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

	// Create organization
	org := &Organization{
		Address:   testOrgAddress,
		Active:    true,
		CreatedAt: time.Now(),
	}
	err := testDB.SetOrganization(org)
	c.Assert(err, qt.IsNil)

	// Create a census
	census := &Census{
		ID:         primitive.NewObjectID(),
		OrgAddress: testOrgAddress,
		Type:       CensusTypeMail,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	censusID, err := testDB.SetCensus(census)
	c.Assert(err, qt.IsNil)

	// Create test members
	members := []*OrgMember{
		{
			ID:           primitive.NewObjectID(),
			OrgAddress:   testOrgAddress,
			MemberNumber: "filter-test-1",
			Email:        "filter1@example.com",
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		},
		{
			ID:           primitive.NewObjectID(),
			OrgAddress:   testOrgAddress,
			MemberNumber: "filter-test-2",
			Email:        "filter2@example.com",
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		},
	}

	// Save members to DB
	for _, member := range members {
		_, err = testDB.SetOrgMember("test_salt", member)
		c.Assert(err, qt.IsNil)
	}

	// Get the census ObjectID
	censusObjID, err := primitive.ObjectIDFromHex(censusID)
	c.Assert(err, qt.IsNil)

	t.Run("InitialCreation", func(_ *testing.T) {
		// Create bulk operations
		bulkOrgMembersOps, bulkCensusParticipantOps := createCensusParticipantBulkOperations(
			members,
			org,
			censusObjID,
			"test_salt",
			time.Now(),
		)

		// Verify operations were created correctly
		c.Assert(bulkOrgMembersOps, qt.HasLen, 2)
		c.Assert(bulkCensusParticipantOps, qt.HasLen, 2)

		// Process the batch
		added := testDB.processBatch(bulkOrgMembersOps, bulkCensusParticipantOps)
		c.Assert(added, qt.Equals, 2)

		// Verify participants were created with correct IDs
		for _, member := range members {
			participant, err := testDB.CensusParticipant(censusID, member.ID.Hex())
			c.Assert(err, qt.IsNil)
			c.Assert(participant.ParticipantID, qt.Equals, member.ID.Hex())
			c.Assert(participant.CensusID, qt.Equals, censusID)
		}

		// Count total participants - should be exactly 2
		participants, err := testDB.CensusParticipants(censusID)
		c.Assert(err, qt.IsNil)
		c.Assert(participants, qt.HasLen, 2)
	})

	t.Run("UpsertFunctionality", func(_ *testing.T) {
		// Store creation times to verify updates
		originalParticipants := make(map[string]CensusParticipant)
		participants, err := testDB.CensusParticipants(censusID)
		c.Assert(err, qt.IsNil)
		for _, p := range participants {
			originalParticipants[p.ParticipantID] = p
		}

		// Wait a moment to ensure timestamps will differ
		time.Sleep(10 * time.Millisecond)

		// Update the same members - this should trigger upsert
		currentTime := time.Now()
		bulkOrgMembersOps, bulkCensusParticipantOps := createCensusParticipantBulkOperations(
			members,
			org,
			censusObjID,
			"test_salt",
			currentTime,
		)

		// Process the batch again - should update existing participants
		added := testDB.processBatch(bulkOrgMembersOps, bulkCensusParticipantOps)
		c.Assert(added, qt.Equals, 2)

		// Verify participants were updated, not duplicated
		participants, err = testDB.CensusParticipants(censusID)
		c.Assert(err, qt.IsNil)
		c.Assert(participants, qt.HasLen, 2) // Still only 2 participants

		// Check that each participant's ParticipantID matches a member's ID.Hex()
		// and that timestamps are properly updated
		for _, participant := range participants {
			original, exists := originalParticipants[participant.ParticipantID]
			c.Assert(exists, qt.IsTrue)

			c.Assert(original.CreatedAt, qt.Equals, participant.CreatedAt)         // CreatedAt should be unchanged
			c.Assert(original.UpdatedAt, qt.Not(qt.Equals), participant.UpdatedAt) // UpdatedAt should be changed

			foundMatch := false
			for _, member := range members {
				if participant.ParticipantID == member.ID.Hex() {
					foundMatch = true
					break
				}
			}
			c.Assert(foundMatch, qt.IsTrue, qt.Commentf("ParticipantID should match a member's ID.Hex()"))
		}
	})
}
