package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const testMembershipParticipantID = "member123"

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
	memberNumber := testMembershipParticipantID + memberSuffix
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

		// Test creating a new membership
		membership := &CensusParticipant{
			ParticipantID: member.ID.Hex(),
			CensusID:      censusID,
		}

		// Test with invalid data
		t.Run("InvalidData", func(_ *testing.T) {
			invalidMembership := &CensusParticipant{
				ParticipantID: "",
				CensusID:      censusID,
			}
			err := testDB.SetCensusParticipant(invalidMembership)
			c.Assert(err, qt.Equals, ErrInvalidData)

			invalidMembership = &CensusParticipant{
				ParticipantID: testMembershipParticipantID,
				CensusID:      "",
			}
			err = testDB.SetCensusParticipant(invalidMembership)
			c.Assert(err, qt.Equals, ErrInvalidData)
		})

		t.Run("NonExistentCensus", func(_ *testing.T) {
			nonExistentMembership := &CensusParticipant{
				ParticipantID: testMembershipParticipantID,
				CensusID:      primitive.NewObjectID().Hex(),
			}
			err := testDB.SetCensusParticipant(nonExistentMembership)
			c.Assert(err, qt.Not(qt.IsNil))
		})

		t.Run("NonExistentMember", func(_ *testing.T) {
			nonExistentMemberMembership := &CensusParticipant{
				ParticipantID: "non-existent",
				CensusID:      censusID,
			}
			err := testDB.SetCensusParticipant(nonExistentMemberMembership)
			c.Assert(err, qt.Not(qt.IsNil))
		})

		t.Run("CreateAndUpdate", func(_ *testing.T) {
			// Create new membership
			err := testDB.SetCensusParticipant(membership)
			c.Assert(err, qt.IsNil)

			// Verify the membership was created correctly
			createdMembership, err := testDB.CensusParticipant(censusID, member.ID.Hex())
			c.Assert(err, qt.IsNil)
			c.Assert(createdMembership.CensusID, qt.Equals, censusID)
			c.Assert(createdMembership.CreatedAt.IsZero(), qt.IsFalse)
			c.Assert(createdMembership.UpdatedAt.IsZero(), qt.IsFalse)

			// Test updating an existing membership
			time.Sleep(time.Millisecond) // Ensure different UpdatedAt timestamp
			err = testDB.SetCensusParticipant(membership)
			c.Assert(err, qt.IsNil)

			// Verify the membership was updated correctly
			updatedMembership, err := testDB.CensusParticipant(censusID, member.ID.Hex())
			c.Assert(err, qt.IsNil)
			c.Assert(updatedMembership.CensusID, qt.Equals, censusID)
			c.Assert(updatedMembership.CreatedAt, qt.Equals, createdMembership.CreatedAt)
			c.Assert(updatedMembership.UpdatedAt.After(createdMembership.UpdatedAt), qt.IsTrue)
		})
	})

	t.Run("GetCensusParticipant", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		member, _, censusID := setupTestCensusParticipantPrerequisites(t, "_get")
		participantID := testMembershipParticipantID + "_get"

		t.Run("InvalidData", func(_ *testing.T) {
			// Test getting membership with invalid data
			_, err := testDB.CensusParticipant("", member.ID.Hex())
			c.Assert(err, qt.Equals, ErrInvalidData)

			_, err = testDB.CensusParticipant(censusID, "")
			c.Assert(err, qt.Equals, ErrInvalidData)
		})

		t.Run("NonExistentMembership", func(_ *testing.T) {
			// Test getting non-existent membership
			_, err := testDB.CensusParticipant(censusID, participantID)
			c.Assert(err, qt.Equals, ErrNotFound)
		})

		t.Run("ExistingMembership", func(_ *testing.T) {
			// Create a membership to retrieve
			membership := &CensusParticipant{
				ParticipantID: member.ID.Hex(),
				CensusID:      censusID,
			}
			err := testDB.SetCensusParticipant(membership)
			c.Assert(err, qt.IsNil)

			// Test getting existing membership
			retrievedMembership, err := testDB.CensusParticipant(censusID, member.ID.Hex())
			c.Assert(err, qt.IsNil)
			c.Assert(retrievedMembership.CensusID, qt.Equals, censusID)
			c.Assert(retrievedMembership.CreatedAt.IsZero(), qt.IsFalse)
			c.Assert(retrievedMembership.UpdatedAt.IsZero(), qt.IsFalse)
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

		t.Run("ExistingMembership", func(_ *testing.T) {
			// Create a membership to delete
			membership := &CensusParticipant{
				ParticipantID: member.ID.Hex(),
				CensusID:      censusID,
			}
			err := testDB.SetCensusParticipant(membership)
			c.Assert(err, qt.IsNil)

			// Test deleting existing membership
			err = testDB.DelCensusParticipant(censusID, member.ID.Hex())
			c.Assert(err, qt.IsNil)

			// Verify the membership was deleted
			_, err = testDB.CensusParticipant(censusID, member.ID.Hex())
			c.Assert(err, qt.Equals, ErrNotFound)
		})

		t.Run("NonExistentMembership", func(_ *testing.T) {
			// Test deleting non-existent membership (should not error)
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
			progressChan, err := testDB.SetBulkCensusParticipant("test_salt", censusID, nil)
			c.Assert(err, qt.IsNil)

			// Channel should be closed immediately for empty members
			_, open := <-progressChan
			c.Assert(open, qt.IsFalse)
		})

		t.Run("InvalidData", func(_ *testing.T) {
			// Test with empty census ID
			members := []OrgMember{
				{
					MemberNumber: "test1",
					Email:        "test1@example.com",
					Phone:        "1234567890",
					Password:     "password1",
				},
			}
			progressChan, err := testDB.SetBulkCensusParticipant("test_salt", "", members)
			c.Assert(err, qt.Equals, ErrInvalidData)

			// Channel should be closed immediately for invalid data
			_, open := <-progressChan
			c.Assert(open, qt.IsFalse)
		})

		t.Run("NonExistentCensus", func(_ *testing.T) {
			members := []OrgMember{
				{
					MemberNumber: "test1",
					Email:        "test1@example.com",
					Phone:        "1234567890",
					Password:     "password1",
				},
			}
			// Test with non-existent census
			progressChan, err := testDB.SetBulkCensusParticipant("test_salt", primitive.NewObjectID().Hex(), members)
			c.Assert(err, qt.Not(qt.IsNil))

			// Channel should be closed immediately for non-existent census
			_, open := <-progressChan
			c.Assert(open, qt.IsFalse)
		})

		t.Run("SuccessfulBulkCreation", func(_ *testing.T) {
			// Test successful bulk creation
			members := []OrgMember{
				{
					MemberNumber: "test1",
					Email:        "test1@example.com",
					Phone:        "1234567890",
					Password:     "password1",
				},
				{
					MemberNumber: "test2",
					Email:        "test2@example.com",
					Phone:        "0987654321",
					Password:     "password2",
				},
			}

			progressChan, err := testDB.SetBulkCensusParticipant("test_salt", censusID, members)
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
				member, err := testDB.OrgMemberByMemberNumber(testOrgAddress, p.MemberNumber)
				c.Assert(err, qt.IsNil)
				c.Assert(member.Email, qt.Not(qt.Equals), "")
				c.Assert(member.Phone, qt.Equals, "")
				c.Assert(member.HashedPhone, qt.Not(qt.Equals), "")
				c.Assert(member.Password, qt.Equals, "")
				c.Assert(member.HashedPass, qt.Not(qt.Equals), "")
				c.Assert(member.CreatedAt.IsZero(), qt.IsFalse)

				// Verify memberships were created
				membership, err := testDB.CensusParticipant(censusID, member.ID.Hex())
				c.Assert(err, qt.IsNil)
				c.Assert(membership.CensusID, qt.Equals, censusID)
				c.Assert(membership.CreatedAt.IsZero(), qt.IsFalse)
			}
		})

		t.Run("UpdateExistingMembers", func(_ *testing.T) {
			// Create members first
			members := []OrgMember{
				{
					MemberNumber: "update1",
					Email:        "update1@example.com",
					Phone:        "1234567890",
					Password:     "password1",
				},
				{
					MemberNumber: "update2",
					Email:        "update2@example.com",
					Phone:        "0987654321",
					Password:     "password2",
				},
			}

			// Create initial members
			progressChan, err := testDB.SetBulkCensusParticipant("test_salt", censusID, members)
			c.Assert(err, qt.IsNil)
			c.Assert(progressChan, qt.Not(qt.IsNil))

			// Wait for the operation to complete
			//revive:disable:empty-block
			for range progressChan {
				// Just drain the channel
			}
			//revive:enable:empty-block

			// Test updating existing members and memberships
			// set first their internal ID correctly
			member0, err := testDB.OrgMemberByMemberNumber(testOrgAddress, members[0].MemberNumber)
			c.Assert(err, qt.IsNil)
			members[0].ID = member0.ID
			members[0].Email = "updated1@example.com"
			member1, err := testDB.OrgMemberByMemberNumber(testOrgAddress, members[1].MemberNumber)
			c.Assert(err, qt.IsNil)
			members[1].ID = member1.ID
			members[1].Phone = "1111111111"

			progressChan, err = testDB.SetBulkCensusParticipant("test_salt", censusID, members)
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
			for i, p := range members {
				member, err := testDB.OrgMemberByMemberNumber(testOrgAddress, p.MemberNumber)
				c.Assert(err, qt.IsNil)
				c.Assert(member.Email, qt.Equals, members[i].Email)
				c.Assert(member.Phone, qt.Equals, "")
				c.Assert(member.HashedPhone, qt.Not(qt.Equals), "")
			}
		})
	})
}
