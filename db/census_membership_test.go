package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const testMembershipParticipantNo = "participant123"

func setupTestCensusMembershipPrerequisites(t *testing.T, participantSuffix string) (*Census, string) {
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

	// Create test participant with unique ID
	participantNo := testMembershipParticipantNo + participantSuffix
	participant := &OrgParticipant{
		OrgAddress:    testOrgAddress,
		ParticipantNo: participantNo,
		Email:         "test" + participantSuffix + "@example.com",
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	_, err = testDB.SetOrgParticipant("test_salt", participant)
	if err != nil {
		t.Fatalf("failed to set organization participant: %v", err)
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

	return census, censusID
}

func TestCensusMembership(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.Reset(), qt.IsNil) })

	t.Run("SetCensusMembership", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		_, censusID := setupTestCensusMembershipPrerequisites(t, "_set")

		// Test creating a new membership
		participantNo := testMembershipParticipantNo + "_set"
		membership := &CensusMembership{
			ParticipantNo: participantNo,
			CensusID:      censusID,
		}

		// Test with invalid data
		t.Run("InvalidData", func(_ *testing.T) {
			invalidMembership := &CensusMembership{
				ParticipantNo: "",
				CensusID:      censusID,
			}
			err := testDB.SetCensusMembership(invalidMembership)
			c.Assert(err, qt.Equals, ErrInvalidData)

			invalidMembership = &CensusMembership{
				ParticipantNo: testMembershipParticipantNo,
				CensusID:      "",
			}
			err = testDB.SetCensusMembership(invalidMembership)
			c.Assert(err, qt.Equals, ErrInvalidData)
		})

		t.Run("NonExistentCensus", func(_ *testing.T) {
			nonExistentMembership := &CensusMembership{
				ParticipantNo: testMembershipParticipantNo,
				CensusID:      primitive.NewObjectID().Hex(),
			}
			err := testDB.SetCensusMembership(nonExistentMembership)
			c.Assert(err, qt.Not(qt.IsNil))
		})

		t.Run("NonExistentParticipant", func(_ *testing.T) {
			nonExistentParticipantMembership := &CensusMembership{
				ParticipantNo: "non-existent",
				CensusID:      censusID,
			}
			err := testDB.SetCensusMembership(nonExistentParticipantMembership)
			c.Assert(err, qt.Not(qt.IsNil))
		})

		t.Run("CreateAndUpdate", func(_ *testing.T) {
			// Create new membership
			err := testDB.SetCensusMembership(membership)
			c.Assert(err, qt.IsNil)

			// Verify the membership was created correctly
			createdMembership, err := testDB.CensusMembership(censusID, participantNo)
			c.Assert(err, qt.IsNil)
			c.Assert(createdMembership.ParticipantNo, qt.Equals, participantNo)
			c.Assert(createdMembership.CensusID, qt.Equals, censusID)
			c.Assert(createdMembership.CreatedAt.IsZero(), qt.IsFalse)
			c.Assert(createdMembership.UpdatedAt.IsZero(), qt.IsFalse)

			// Test updating an existing membership
			time.Sleep(time.Millisecond) // Ensure different UpdatedAt timestamp
			err = testDB.SetCensusMembership(membership)
			c.Assert(err, qt.IsNil)

			// Verify the membership was updated correctly
			updatedMembership, err := testDB.CensusMembership(censusID, participantNo)
			c.Assert(err, qt.IsNil)
			c.Assert(updatedMembership.ParticipantNo, qt.Equals, participantNo)
			c.Assert(updatedMembership.CensusID, qt.Equals, censusID)
			c.Assert(updatedMembership.CreatedAt, qt.Equals, createdMembership.CreatedAt)
			c.Assert(updatedMembership.UpdatedAt.After(createdMembership.UpdatedAt), qt.IsTrue)
		})
	})

	t.Run("GetCensusMembership", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		_, censusID := setupTestCensusMembershipPrerequisites(t, "_get")
		participantNo := testMembershipParticipantNo + "_get"

		t.Run("InvalidData", func(_ *testing.T) {
			// Test getting membership with invalid data
			_, err := testDB.CensusMembership("", participantNo)
			c.Assert(err, qt.Equals, ErrInvalidData)

			_, err = testDB.CensusMembership(censusID, "")
			c.Assert(err, qt.Equals, ErrInvalidData)
		})

		t.Run("NonExistentMembership", func(_ *testing.T) {
			// Test getting non-existent membership
			_, err := testDB.CensusMembership(censusID, participantNo)
			c.Assert(err, qt.Equals, ErrNotFound)
		})

		t.Run("ExistingMembership", func(_ *testing.T) {
			// Create a membership to retrieve
			membership := &CensusMembership{
				ParticipantNo: participantNo,
				CensusID:      censusID,
			}
			err := testDB.SetCensusMembership(membership)
			c.Assert(err, qt.IsNil)

			// Test getting existing membership
			retrievedMembership, err := testDB.CensusMembership(censusID, participantNo)
			c.Assert(err, qt.IsNil)
			c.Assert(retrievedMembership.ParticipantNo, qt.Equals, participantNo)
			c.Assert(retrievedMembership.CensusID, qt.Equals, censusID)
			c.Assert(retrievedMembership.CreatedAt.IsZero(), qt.IsFalse)
			c.Assert(retrievedMembership.UpdatedAt.IsZero(), qt.IsFalse)
		})
	})

	t.Run("DeleteCensusMembership", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		_, censusID := setupTestCensusMembershipPrerequisites(t, "_delete")
		participantNo := testMembershipParticipantNo + "_delete"

		t.Run("InvalidData", func(_ *testing.T) {
			// Test deleting with invalid data
			err := testDB.DelCensusMembership("", participantNo)
			c.Assert(err, qt.Equals, ErrInvalidData)

			err = testDB.DelCensusMembership(censusID, "")
			c.Assert(err, qt.Equals, ErrInvalidData)
		})

		t.Run("ExistingMembership", func(_ *testing.T) {
			// Create a membership to delete
			membership := &CensusMembership{
				ParticipantNo: participantNo,
				CensusID:      censusID,
			}
			err := testDB.SetCensusMembership(membership)
			c.Assert(err, qt.IsNil)

			// Test deleting existing membership
			err = testDB.DelCensusMembership(censusID, participantNo)
			c.Assert(err, qt.IsNil)

			// Verify the membership was deleted
			_, err = testDB.CensusMembership(censusID, participantNo)
			c.Assert(err, qt.Equals, ErrNotFound)
		})

		t.Run("NonExistentMembership", func(_ *testing.T) {
			// Test deleting non-existent membership (should not error)
			err := testDB.DelCensusMembership(censusID, participantNo)
			c.Assert(err, qt.IsNil)
		})
	})

	t.Run("BulkCensusMembership", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		_, censusID := setupTestCensusMembershipPrerequisites(t, "_bulk")

		t.Run("EmptyParticipants", func(_ *testing.T) {
			// Test with empty participants
			progressChan, err := testDB.SetBulkCensusMembership("test_salt", censusID, nil)
			c.Assert(err, qt.IsNil)

			// Channel should be closed immediately for empty participants
			_, open := <-progressChan
			c.Assert(open, qt.IsFalse)
		})

		t.Run("InvalidData", func(_ *testing.T) {
			// Test with empty census ID
			participants := []OrgParticipant{
				{
					ParticipantNo: "test1",
					Email:         "test1@example.com",
					Phone:         "1234567890",
					Password:      "password1",
				},
			}
			progressChan, err := testDB.SetBulkCensusMembership("test_salt", "", participants)
			c.Assert(err, qt.Equals, ErrInvalidData)

			// Channel should be closed immediately for invalid data
			_, open := <-progressChan
			c.Assert(open, qt.IsFalse)
		})

		t.Run("NonExistentCensus", func(_ *testing.T) {
			participants := []OrgParticipant{
				{
					ParticipantNo: "test1",
					Email:         "test1@example.com",
					Phone:         "1234567890",
					Password:      "password1",
				},
			}
			// Test with non-existent census
			progressChan, err := testDB.SetBulkCensusMembership("test_salt", primitive.NewObjectID().Hex(), participants)
			c.Assert(err, qt.Not(qt.IsNil))

			// Channel should be closed immediately for non-existent census
			_, open := <-progressChan
			c.Assert(open, qt.IsFalse)
		})

		t.Run("SuccessfulBulkCreation", func(_ *testing.T) {
			// Test successful bulk creation
			participants := []OrgParticipant{
				{
					ParticipantNo: "test1",
					Email:         "test1@example.com",
					Phone:         "1234567890",
					Password:      "password1",
				},
				{
					ParticipantNo: "test2",
					Email:         "test2@example.com",
					Phone:         "0987654321",
					Password:      "password2",
				},
			}

			progressChan, err := testDB.SetBulkCensusMembership("test_salt", censusID, participants)
			c.Assert(err, qt.IsNil)
			c.Assert(progressChan, qt.Not(qt.IsNil))

			// Wait for the operation to complete by draining the channel
			var lastProgress *BulkCensusMembershipStatus
			for progress := range progressChan {
				lastProgress = progress
			}
			// Final progress should be 100%
			c.Assert(lastProgress.Progress, qt.Equals, 100)

			// Verify participants were created with hashed data
			for _, p := range participants {
				participant, err := testDB.OrgParticipantByNo(testOrgAddress, p.ParticipantNo)
				c.Assert(err, qt.IsNil)
				c.Assert(participant.Email, qt.Equals, "")
				c.Assert(participant.HashedEmail, qt.Not(qt.Equals), "")
				c.Assert(participant.Phone, qt.Equals, "")
				c.Assert(participant.HashedPhone, qt.Not(qt.Equals), "")
				c.Assert(participant.Password, qt.Equals, "")
				c.Assert(participant.HashedPass, qt.Not(qt.Equals), "")
				c.Assert(participant.CreatedAt.IsZero(), qt.IsFalse)

				// Verify memberships were created
				membership, err := testDB.CensusMembership(censusID, p.ParticipantNo)
				c.Assert(err, qt.IsNil)
				c.Assert(membership.ParticipantNo, qt.Equals, p.ParticipantNo)
				c.Assert(membership.CensusID, qt.Equals, censusID)
				c.Assert(membership.CreatedAt.IsZero(), qt.IsFalse)
			}
		})

		t.Run("UpdateExistingParticipants", func(_ *testing.T) {
			// Create participants first
			participants := []OrgParticipant{
				{
					ParticipantNo: "update1",
					Email:         "update1@example.com",
					Phone:         "1234567890",
					Password:      "password1",
				},
				{
					ParticipantNo: "update2",
					Email:         "update2@example.com",
					Phone:         "0987654321",
					Password:      "password2",
				},
			}

			// Create initial participants
			progressChan, err := testDB.SetBulkCensusMembership("test_salt", censusID, participants)
			c.Assert(err, qt.IsNil)
			c.Assert(progressChan, qt.Not(qt.IsNil))

			// Wait for the operation to complete
			//revive:disable:empty-block
			for range progressChan {
				// Just drain the channel
			}
			//revive:enable:empty-block

			// Test updating existing participants and memberships
			participants[0].Email = "updated1@example.com"
			participants[1].Phone = "1111111111"

			progressChan, err = testDB.SetBulkCensusMembership("test_salt", censusID, participants)
			c.Assert(err, qt.IsNil)
			c.Assert(progressChan, qt.Not(qt.IsNil))

			// Wait for the operation to complete
			var lastProgress *BulkCensusMembershipStatus
			for progress := range progressChan {
				lastProgress = progress
			}
			// Final progress should be 100%
			c.Assert(lastProgress.Progress, qt.Equals, 100)

			// Verify updates
			for _, p := range participants {
				participant, err := testDB.OrgParticipantByNo(testOrgAddress, p.ParticipantNo)
				c.Assert(err, qt.IsNil)
				c.Assert(participant.Email, qt.Equals, "")
				c.Assert(participant.HashedEmail, qt.Not(qt.Equals), "")
				c.Assert(participant.Phone, qt.Equals, "")
				c.Assert(participant.HashedPhone, qt.Not(qt.Equals), "")
			}
		})
	})
}
