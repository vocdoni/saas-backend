package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
)

var testParticipantID = internal.NewObjectID()

func setupTestCensusParticipantPrerequisites(t *testing.T, memberSuffix string) (*OrgMember, *Census, internal.ObjectID) {
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
	memberNumber := testParticipantID.String() + memberSuffix
	member := &OrgMember{
		ID:           internal.NewObjectID(),
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
			ParticipantID: member.ID,
			CensusID:      censusID,
		}

		// Test with invalid data
		t.Run("InvalidData", func(_ *testing.T) {
			invalidParticipant := &CensusParticipant{
				ParticipantID: internal.NilObjectID,
				CensusID:      censusID,
			}
			err := testDB.SetCensusParticipant(invalidParticipant)
			c.Assert(err, qt.Equals, ErrInvalidData)

			invalidParticipant = &CensusParticipant{
				ParticipantID: testParticipantID,
				CensusID:      internal.NilObjectID,
			}
			err = testDB.SetCensusParticipant(invalidParticipant)
			c.Assert(err, qt.Equals, ErrInvalidData)
		})

		t.Run("NonExistentCensus", func(_ *testing.T) {
			nonExistentParticipant := &CensusParticipant{
				ParticipantID: testParticipantID,
				CensusID:      internal.NewObjectID(),
			}
			err := testDB.SetCensusParticipant(nonExistentParticipant)
			c.Assert(err, qt.Not(qt.IsNil))
		})

		t.Run("NonExistentMember", func(_ *testing.T) {
			nonExistentParticipantID := &CensusParticipant{
				ParticipantID: internal.NewObjectID(),
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
			createdParticipant, err := testDB.CensusParticipant(censusID, member.ID)
			c.Assert(err, qt.IsNil)
			c.Assert(createdParticipant.CensusID, qt.Equals, censusID)
			c.Assert(createdParticipant.CreatedAt.IsZero(), qt.IsFalse)
			c.Assert(createdParticipant.UpdatedAt.IsZero(), qt.IsFalse)

			// Test updating an existing participant
			time.Sleep(time.Millisecond) // Ensure different UpdatedAt timestamp
			err = testDB.SetCensusParticipant(participant)
			c.Assert(err, qt.IsNil)

			// Verify the participant was updated correctly
			updatedParticipant, err := testDB.CensusParticipant(censusID, member.ID)
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

		t.Run("InvalidData", func(_ *testing.T) {
			// Test getting participant with invalid data
			_, err := testDB.CensusParticipant(internal.NilObjectID, member.ID)
			c.Assert(err, qt.Equals, ErrInvalidData)

			_, err = testDB.CensusParticipant(censusID, internal.NilObjectID)
			c.Assert(err, qt.Equals, ErrInvalidData)
		})

		t.Run("NonExistentParticipant", func(_ *testing.T) {
			// Test getting non-existent participant
			_, err := testDB.CensusParticipant(censusID, testParticipantID)
			c.Assert(err, qt.Equals, ErrNotFound)
		})

		t.Run("ExistingParticipant", func(_ *testing.T) {
			// Create a participant to retrieve
			participant := &CensusParticipant{
				ParticipantID: member.ID,
				CensusID:      censusID,
			}
			err := testDB.SetCensusParticipant(participant)
			c.Assert(err, qt.IsNil)

			// Test getting existing participant
			retrievedParticipant, err := testDB.CensusParticipant(censusID, member.ID)
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
			err := testDB.DelCensusParticipant(internal.NilObjectID, member.ID)
			c.Assert(err, qt.Equals, ErrInvalidData)

			err = testDB.DelCensusParticipant(censusID, internal.NilObjectID)
			c.Assert(err, qt.Equals, ErrInvalidData)
		})

		t.Run("ExistingParticipant", func(_ *testing.T) {
			// Create a participant to delete
			participant := &CensusParticipant{
				ParticipantID: member.ID,
				CensusID:      censusID,
			}
			err := testDB.SetCensusParticipant(participant)
			c.Assert(err, qt.IsNil)

			// Test deleting existing participant
			err = testDB.DelCensusParticipant(censusID, member.ID)
			c.Assert(err, qt.IsNil)

			// Verify the participant was deleted
			_, err = testDB.CensusParticipant(censusID, member.ID)
			c.Assert(err, qt.Equals, ErrNotFound)
		})

		t.Run("NonExistentParticipant", func(_ *testing.T) {
			// Test deleting non-existent participant (should not error)
			err := testDB.DelCensusParticipant(censusID, member.ID)
			c.Assert(err, qt.IsNil)
		})
	})

	t.Run("BulkCensusParticipant", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		_, _, censusID := setupTestCensusParticipantPrerequisites(t, "_bulk")

		t.Run("EmptyMembers", func(_ *testing.T) {
			// Test with empty members
			progressChan, err := testDB.SetBulkCensusOrgMemberParticipant("test_salt", censusID, nil)
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
			progressChan, err := testDB.SetBulkCensusOrgMemberParticipant("test_salt", internal.NilObjectID, members)
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
			progressChan, err := testDB.SetBulkCensusOrgMemberParticipant("test_salt", internal.NewObjectID(), members)
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

			progressChan, err := testDB.SetBulkCensusOrgMemberParticipant("test_salt", censusID, members)
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

				// Verify participants were created
				participant, err := testDB.CensusParticipant(censusID, member.ID)
				c.Assert(err, qt.IsNil)
				c.Assert(participant.CensusID, qt.Equals, censusID)
				c.Assert(participant.CreatedAt.IsZero(), qt.IsFalse)
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
			progressChan, err := testDB.SetBulkCensusOrgMemberParticipant("test_salt", censusID, members)
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
			members[1].Phone = "1111111111"

			progressChan, err = testDB.SetBulkCensusOrgMemberParticipant("test_salt", censusID, members)
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
