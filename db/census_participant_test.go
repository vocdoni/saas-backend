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

func setupTestCensusParticipantPrerequisites(t *testing.T, memberSuffix string) (*OrgMember, *Census, string) {
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
		OrgAddress: testOrgAddress,
		Type:       CensusTypeMail,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	censusID, err := testDB.SetCensus(census)
	if err != nil {
		t.Fatalf("failed to set census: %v", err)
	}

	return member, census, censusID
}

func TestCensusParticipant(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.Reset(), qt.IsNil) })

	t.Run("SetCensusParticipant", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		member, _, censusID := setupTestCensusParticipantPrerequisites(t, "_set")

		// Test creating a new participant
		participant := &CensusParticipant{
			ParticipantID: member.ID.Hex(),
			CensusID:      censusID,
		}

		// Test with invalid data
		t.Run("InvalidData", func(_ *testing.T) {
			invalidParticipant := &CensusParticipant{
				ParticipantID: "",
				CensusID:      censusID,
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
				CensusID:      censusID,
			}
			err := testDB.SetCensusParticipant(nonExistentParticipantID)
			c.Assert(err, qt.Not(qt.IsNil))
		})

		t.Run("CreateAndUpdate", func(_ *testing.T) {
			// Create new participant
			err := testDB.SetCensusParticipant(participant)
			c.Assert(err, qt.IsNil)

			// Verify the participant was created correctly
			createdParticipant, err := testDB.CensusParticipant(censusID, member.ID.Hex())
			c.Assert(err, qt.IsNil)
			c.Assert(createdParticipant.CensusID, qt.Equals, censusID)
			c.Assert(createdParticipant.CreatedAt.IsZero(), qt.IsFalse)
			c.Assert(createdParticipant.UpdatedAt.IsZero(), qt.IsFalse)

			// Test updating an existing participant
			time.Sleep(time.Millisecond) // Ensure different UpdatedAt timestamp
			err = testDB.SetCensusParticipant(participant)
			c.Assert(err, qt.IsNil)

			// Verify the participant was updated correctly
			updatedParticipant, err := testDB.CensusParticipant(censusID, member.ID.Hex())
			c.Assert(err, qt.IsNil)
			c.Assert(updatedParticipant.CensusID, qt.Equals, censusID)
			c.Assert(updatedParticipant.CreatedAt, qt.Equals, createdParticipant.CreatedAt)
			c.Assert(updatedParticipant.UpdatedAt.After(createdParticipant.UpdatedAt), qt.IsTrue)
		})
	})

	t.Run("GetCensusParticipant", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		member, _, censusID := setupTestCensusParticipantPrerequisites(t, "_get")
		participantID := testParticipantID + "_get"

		t.Run("InvalidData", func(_ *testing.T) {
			// Test getting participant with invalid data
			_, err := testDB.CensusParticipant("", member.ID.Hex())
			c.Assert(err, qt.Equals, ErrInvalidData)

			_, err = testDB.CensusParticipant(censusID, "")
			c.Assert(err, qt.Equals, ErrInvalidData)
		})

		t.Run("NonExistentParticipant", func(_ *testing.T) {
			// Test getting non-existent participant
			_, err := testDB.CensusParticipant(censusID, participantID)
			c.Assert(err, qt.Equals, ErrNotFound)
		})

		t.Run("ExistingParticipant", func(_ *testing.T) {
			// Create a participant to retrieve
			participant := &CensusParticipant{
				ParticipantID: member.ID.Hex(),
				CensusID:      censusID,
			}
			err := testDB.SetCensusParticipant(participant)
			c.Assert(err, qt.IsNil)

			// Test getting existing participant
			retrievedParticipant, err := testDB.CensusParticipant(censusID, member.ID.Hex())
			c.Assert(err, qt.IsNil)
			c.Assert(retrievedParticipant.CensusID, qt.Equals, censusID)
			c.Assert(retrievedParticipant.CreatedAt.IsZero(), qt.IsFalse)
			c.Assert(retrievedParticipant.UpdatedAt.IsZero(), qt.IsFalse)
		})
	})

	t.Run("DeleteCensusParticipant", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		member, _, censusID := setupTestCensusParticipantPrerequisites(t, "_delete")

		t.Run("InvalidData", func(_ *testing.T) {
			// Test deleting with invalid data
			err := testDB.DelCensusParticipant("", member.ID.Hex())
			c.Assert(err, qt.Equals, ErrInvalidData)

			err = testDB.DelCensusParticipant(censusID, "")
			c.Assert(err, qt.Equals, ErrInvalidData)
		})

		t.Run("ExistingParticipant", func(_ *testing.T) {
			// Create a participant to delete
			participant := &CensusParticipant{
				ParticipantID: member.ID.Hex(),
				CensusID:      censusID,
			}
			err := testDB.SetCensusParticipant(participant)
			c.Assert(err, qt.IsNil)

			// Test deleting existing participant
			err = testDB.DelCensusParticipant(censusID, member.ID.Hex())
			c.Assert(err, qt.IsNil)

			// Verify the participant was deleted
			_, err = testDB.CensusParticipant(censusID, member.ID.Hex())
			c.Assert(err, qt.Equals, ErrNotFound)
		})

		t.Run("NonExistentParticipant", func(_ *testing.T) {
			// Test deleting non-existent participant (should not error)
			err := testDB.DelCensusParticipant(censusID, member.ID.Hex())
			c.Assert(err, qt.IsNil)
		})
	})

	t.Run("BulkCensusParticipant", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		_, _, censusID := setupTestCensusParticipantPrerequisites(t, "_bulk")

		t.Run("EmptyMembers", func(_ *testing.T) {
			// Test with empty members
			progressChan, err := testDB.SetBulkCensusOrgMemberParticipant(testOrg, "test_salt", censusID, nil)
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

			progressChan, err := testDB.SetBulkCensusOrgMemberParticipant(testOrg, "test_salt", censusID, members)
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
				participant, err := testDB.CensusParticipant(censusID, member.ID.Hex())
				c.Assert(err, qt.IsNil)
				c.Assert(participant.CensusID, qt.Equals, censusID)
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
			progressChan, err := testDB.SetBulkCensusOrgMemberParticipant(testOrg, "test_salt", censusID, members)
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

			progressChan, err = testDB.SetBulkCensusOrgMemberParticipant(testOrg, "test_salt", censusID, members)
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
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		member, _, censusID := setupTestCensusParticipantPrerequisites(t, "_loginHash")

		// Set auth fields and two-factor auth fields
		authFields := OrgMemberAuthFields{OrgMemberAuthFieldsMemberNumber, OrgMemberAuthFieldsName}
		twoFaFields := OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail}

		// Update the member with additional information for testing login hash
		member.Name = "Test User"
		_, err := testDB.SetOrgMember("test_salt", member)
		c.Assert(err, qt.IsNil)

		// Generate login hash
		loginHash := HashAuthTwoFaFields(*member, authFields, twoFaFields)
		c.Assert(loginHash, qt.Not(qt.IsNil))

		// Create participant with login hash
		participant := &CensusParticipant{
			ParticipantID: member.ID.Hex(),
			CensusID:      censusID,
			LoginHash:     loginHash,
		}
		err = testDB.SetCensusParticipant(participant)
		c.Assert(err, qt.IsNil)

		t.Run("InvalidData", func(_ *testing.T) {
			// Test with empty login hash
			_, err := testDB.CensusParticipantByLoginHash(censusID, []byte{})
			c.Assert(err, qt.Equals, ErrInvalidData)

			// Test with empty census ID
			_, err = testDB.CensusParticipantByLoginHash("", loginHash)
			c.Assert(err, qt.Equals, ErrInvalidData)
		})

		t.Run("NonExistentParticipant", func(_ *testing.T) {
			// Test with non-existent login hash
			nonExistentHash := []byte("nonexistenthash")
			_, err := testDB.CensusParticipantByLoginHash(censusID, nonExistentHash)
			c.Assert(err, qt.Equals, ErrNotFound)
		})

		t.Run("ExistingParticipant", func(_ *testing.T) {
			// Test successful retrieval
			retrievedParticipant, err := testDB.CensusParticipantByLoginHash(censusID, loginHash)
			c.Assert(err, qt.IsNil)
			c.Assert(retrievedParticipant.CensusID, qt.Equals, censusID)
			c.Assert(retrievedParticipant.ParticipantID, qt.Equals, member.ID.Hex())
		})
	})

	t.Run("SetBulkCensusParticipant", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)

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
			TwoFaFields: OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail},
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}
		censusID, err := testDB.SetCensus(census)
		c.Assert(err, qt.IsNil)

		// Create members first
		memberIDs := make([]string, 0, 3)
		for i := 1; i <= 3; i++ {
			member := &OrgMember{
				ID:           primitive.NewObjectID(),
				OrgAddress:   testOrgAddress,
				MemberNumber: fmt.Sprintf("bulk-login-%d", i),
				Name:         fmt.Sprintf("Bulk User %d", i),
				Email:        fmt.Sprintf("bulk%d@example.com", i),
				CreatedAt:    time.Now(),
				UpdatedAt:    time.Now(),
			}
			_, err := testDB.SetOrgMember("test_salt", member)
			c.Assert(err, qt.IsNil)

			memberIDs = append(memberIDs, member.ID.Hex())
		}

		// Create members group with the members
		group := &OrganizationMemberGroup{
			OrgAddress: testOrgAddress,
			Title:      "Test Group",
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			MemberIDs:  memberIDs,
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

		upsertCount, err := testDB.setBulkCensusParticipant(
			ctx,
			censusID,
			groupID,
			testOrgAddress,
			census.AuthFields,
			census.TwoFaFields,
		)
		c.Assert(err, qt.IsNil)
		c.Assert(upsertCount, qt.Equals, int64(3))

		// Get all participants
		participants, err := testDB.CensusParticipants(censusID)
		c.Assert(err, qt.IsNil)
		c.Assert(len(participants), qt.Equals, 3)

		// Verify login hash exists for each participant
		for _, participant := range participants {
			c.Assert(participant.LoginHash, qt.Not(qt.IsNil))

			// Verify we can retrieve participant by login hash
			found, err := testDB.CensusParticipantByLoginHash(censusID, participant.LoginHash)
			c.Assert(err, qt.IsNil)
			c.Assert(found.ParticipantID, qt.Equals, participant.ParticipantID)
		}
	})
}

// TestCreateCensusParticipantBulkOperationsFiltering specifically tests the filtering functionality
// in the createCensusParticipantBulkOperations function, focusing on ensuring
// that "participantID": orgMember.ID.Hex() works correctly for upserts
func TestCreateCensusParticipantBulkOperationsFiltering(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.Reset(), qt.IsNil) })

	c.Assert(testDB.Reset(), qt.IsNil)

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
		c.Assert(len(bulkOrgMembersOps), qt.Equals, 2)
		c.Assert(len(bulkCensusParticipantOps), qt.Equals, 2)

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
		c.Assert(len(participants), qt.Equals, 2)
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
		c.Assert(len(participants), qt.Equals, 2) // Still only 2 participants

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
