package db

import (
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestOrgParticipants(t *testing.T) {
	c := qt.New(t)
	db := startTestDB(t)

	t.Run("SetOrgParticipant", func(t *testing.T) {
		c.Assert(db.Reset(), qt.IsNil)
		// Create org
		organization := &Organization{
			Address: testOrgAddress,
		}
		err := db.SetOrganization(organization)
		c.Assert(err, qt.IsNil)

		// Test creating a new participant
		participant := &OrgParticipant{
			OrgAddress:    testOrgAddress,
			Email:         testParticipantEmail,
			Phone:         testPhone,
			ParticipantNo: testParticipantNo,
			Name:          testName,
			Password:      testPassword,
		}

		// Create new participant
		participantOID, err := db.SetOrgParticipant(testSalt, participant)
		c.Assert(err, qt.IsNil)
		c.Assert(participantOID, qt.Not(qt.Equals), "")

		// Verify the participant was created correctly
		createdParticipant, err := db.OrgParticipant(participantOID)
		c.Assert(err, qt.IsNil)
		c.Assert(createdParticipant.HashedEmail, qt.DeepEquals, internal.HashOrgData(testOrgAddress, testParticipantEmail))
		c.Assert(createdParticipant.HashedPhone, qt.DeepEquals, internal.HashOrgData(testOrgAddress, testPhone))
		c.Assert(createdParticipant.ParticipantNo, qt.Equals, participant.ParticipantNo)
		c.Assert(createdParticipant.Name, qt.Equals, testName)
		c.Assert(createdParticipant.HashedPass, qt.DeepEquals, internal.HashPassword(testSalt, testPassword))
		c.Assert(createdParticipant.CreatedAt, qt.Not(qt.IsNil))

		// Test updating an existing participant
		newName := "Updated Name"
		newPhone := "+34655432100"
		createdParticipant.Name = newName
		createdParticipant.Phone = newPhone

		// Update participant
		updatedID, err := db.SetOrgParticipant(testSalt, createdParticipant)
		c.Assert(err, qt.IsNil)
		c.Assert(updatedID, qt.Equals, participantOID)

		// Verify the participant was updated correctly
		updatedParticipant, err := db.OrgParticipant(updatedID)
		c.Assert(err, qt.IsNil)
		c.Assert(updatedParticipant.Name, qt.Equals, newName)
		c.Assert(updatedParticipant.HashedPhone, qt.DeepEquals, internal.HashOrgData(testOrgAddress, newPhone))
		c.Assert(updatedParticipant.CreatedAt, qt.Equals, createdParticipant.CreatedAt)

		// Test duplicate entries
		duplicateParticipant := &OrgParticipant{
			OrgAddress:    testOrgAddress,
			Email:         testParticipantEmail,
			Phone:         testPhone,
			ParticipantNo: testParticipantNo,
			Name:          testName,
			Password:      testPassword,
		}

		// Attempt to create duplicate participant
		_, err = db.SetOrgParticipant(testSalt, duplicateParticipant)
		c.Assert(err, qt.Not(qt.IsNil))

		// Attempt to update participant
		duplicateParticipant.ID = updatedParticipant.ID
		duplicateID, err := db.SetOrgParticipant(testSalt, duplicateParticipant)
		c.Assert(err, qt.IsNil)
		c.Assert(duplicateID, qt.Equals, participantOID)

		// Verify the duplicate participant was not created but updated
		duplicateCreatedParticipant, err := db.OrgParticipant(duplicateID)
		c.Assert(err, qt.IsNil)
		c.Assert(duplicateCreatedParticipant.ParticipantNo, qt.Equals, testParticipantNo)
		c.Assert(duplicateCreatedParticipant.Name, qt.Equals, testName)
	})

	t.Run("DelOrgParticipant", func(t *testing.T) {
		c.Assert(db.Reset(), qt.IsNil)
		// Create org
		organization := &Organization{
			Address: testOrgAddress,
		}
		err := db.SetOrganization(organization)
		c.Assert(err, qt.IsNil)

		// Create a participant to delete
		participant := &OrgParticipant{
			OrgAddress:    testOrgAddress,
			Email:         testParticipantEmail,
			ParticipantNo: testParticipantNo,
			Name:          testName,
		}

		// Create new participant
		participantOID, err := db.SetOrgParticipant(testSalt, participant)
		c.Assert(err, qt.IsNil)

		// Test deleting with invalid ID
		err = db.DelOrgParticipant("invalid-id")
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test deleting with valid ID
		err = db.DelOrgParticipant(participantOID)
		c.Assert(err, qt.IsNil)

		// Verify the participant was deleted
		_, err = db.OrgParticipant(participantOID)
		c.Assert(err, qt.Not(qt.IsNil))
	})

	t.Run("GetOrgParticipant", func(t *testing.T) {
		c.Assert(db.Reset(), qt.IsNil)
		// Create org
		organization := &Organization{
			Address: testOrgAddress,
		}
		err := db.SetOrganization(organization)
		c.Assert(err, qt.IsNil)

		// Test getting participant with invalid ID
		_, err = db.OrgParticipant("invalid-id")
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Create a participant to retrieve
		participant := &OrgParticipant{
			OrgAddress:    testOrgAddress,
			Email:         testParticipantEmail,
			ParticipantNo: testParticipantNo,
			Name:          testName,
		}

		// Create new participant
		participantOID, err := db.SetOrgParticipant(testSalt, participant)
		c.Assert(err, qt.IsNil)

		// Test getting participant with valid ID
		retrievedParticipant, err := db.OrgParticipant(participantOID)
		c.Assert(err, qt.IsNil)
		c.Assert(retrievedParticipant.HashedEmail, qt.DeepEquals, internal.HashOrgData(testOrgAddress, testParticipantEmail))
		c.Assert(retrievedParticipant.ParticipantNo, qt.Equals, testParticipantNo)
		c.Assert(retrievedParticipant.Name, qt.Equals, testName)
		c.Assert(retrievedParticipant.CreatedAt, qt.Not(qt.IsNil))

		// Test getting non-existent participant
		nonExistentID := primitive.NewObjectID().Hex()
		_, err = db.OrgParticipant(nonExistentID)
		c.Assert(err, qt.Not(qt.IsNil))
	})

	t.Run("BulkUpsertOrgParticipants", func(t *testing.T) {
		c.Assert(db.Reset(), qt.IsNil)
		// Create org
		organization := &Organization{
			Address: testOrgAddress,
		}
		err := db.SetOrganization(organization)
		c.Assert(err, qt.IsNil)

		// Test bulk insert of new participants
		participants := []OrgParticipant{
			{
				Email:         testParticipantEmail,
				Phone:         testPhone,
				ParticipantNo: testParticipantNo,
				Name:          testName,
				Password:      testPassword,
			},
			{
				Email:         "participant2@test.com",
				Phone:         "+34678678978",
				ParticipantNo: "participant456",
				Name:          "Test Participant 2",
				Password:      "testpass456",
			},
		}

		// Perform bulk upsert
		result, err := db.BulkUpsertOrgParticipants(testOrgAddress, testSalt, participants)
		c.Assert(err, qt.IsNil)
		c.Assert(result.UpsertedCount, qt.Equals, int64(2))

		// Verify both participants were created with hashed fields
		participant1, err := db.OrgParticipantByNo(testOrgAddress, testParticipantNo)
		c.Assert(err, qt.IsNil)
		c.Assert(participant1.HashedEmail, qt.DeepEquals, internal.HashOrgData(testOrgAddress, testParticipantEmail))
		c.Assert(participant1.HashedPhone, qt.DeepEquals, internal.HashOrgData(testOrgAddress, testPhone))
		c.Assert(participant1.HashedPass, qt.DeepEquals, internal.HashPassword(testSalt, testPassword))

		participant2, err := db.OrgParticipantByNo(testOrgAddress, participants[1].ParticipantNo)
		c.Assert(err, qt.IsNil)
		c.Assert(participant2.HashedEmail, qt.DeepEquals, internal.HashOrgData(testOrgAddress, participants[1].Email))
		c.Assert(participant2.HashedPhone, qt.DeepEquals, internal.HashOrgData(testOrgAddress, participants[1].Phone))
		c.Assert(participant2.HashedPass, qt.DeepEquals, internal.HashPassword(testSalt, participants[1].Password))

		// Test updating existing participants
		participants[0].Name = "Updated Name"
		participants[1].Phone = "+34678678971"

		// Perform bulk upsert again
		result, err = db.BulkUpsertOrgParticipants(testOrgAddress, testSalt, participants)
		c.Assert(err, qt.IsNil)
		c.Assert(result.ModifiedCount, qt.Equals, int64(2)) // Both documents should be modified
		c.Assert(result.UpsertedCount, qt.Equals, int64(0)) // No new documents should be inserted

		// Verify updates for both participants
		updatedParticipant1, err := db.OrgParticipantByNo(testOrgAddress, testParticipantNo)
		c.Assert(err, qt.IsNil)
		c.Assert(updatedParticipant1.Name, qt.Equals, "Updated Name")
		c.Assert(updatedParticipant1.HashedEmail, qt.DeepEquals, internal.HashOrgData(testOrgAddress, testParticipantEmail))

		updatedParticipant2, err := db.OrgParticipantByNo(testOrgAddress, "participant456")
		c.Assert(err, qt.IsNil)
		c.Assert(updatedParticipant2.HashedPhone, qt.DeepEquals, internal.HashOrgData(testOrgAddress, participants[1].Phone))
		c.Assert(updatedParticipant2.Name, qt.Equals, "Test Participant 2")

		// Test with empty organization address
		_, err = db.BulkUpsertOrgParticipants("", testSalt, participants)
		c.Assert(err, qt.Not(qt.IsNil))
	})
}
