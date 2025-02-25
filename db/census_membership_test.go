package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const testMembershipParticipantNo = "participant123"

func setupTestPrerequisites(c *qt.C) (*Census, string) {
	// Create test organization
	org := &Organization{
		Address:   testOrgAddress,
		Active:    true,
		CreatedAt: time.Now(),
	}
	err := db.SetOrganization(org)
	c.Assert(err, qt.IsNil)

	// Create test participant
	participant := &OrgParticipant{
		OrgAddress:    testOrgAddress,
		ParticipantNo: testMembershipParticipantNo,
		Email:         "test@example.com",
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	_, err = db.SetOrgParticipant("test_salt", participant)
	c.Assert(err, qt.IsNil)

	// Create test census
	census := &Census{
		OrgAddress: testOrgAddress,
		Type:       CensusTypeMail,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	censusID, err := db.SetCensus(census)
	c.Assert(err, qt.IsNil)

	return census, censusID
}

func TestSetCensusMembership(t *testing.T) {
	c := qt.New(t)
	defer resetDB(c)

	// Setup prerequisites
	_, censusID := setupTestPrerequisites(c)

	// Test creating a new membership
	membership := &CensusMembership{
		ParticipantNo: testMembershipParticipantNo,
		CensusID:      censusID,
	}

	// Test with invalid data
	invalidMembership := &CensusMembership{
		ParticipantNo: "",
		CensusID:      censusID,
	}
	err := db.SetCensusMembership(invalidMembership)
	c.Assert(err, qt.Equals, ErrInvalidData)

	invalidMembership = &CensusMembership{
		ParticipantNo: testMembershipParticipantNo,
		CensusID:      "",
	}
	err = db.SetCensusMembership(invalidMembership)
	c.Assert(err, qt.Equals, ErrInvalidData)

	// Test with non-existent census
	nonExistentMembership := &CensusMembership{
		ParticipantNo: testMembershipParticipantNo,
		CensusID:      primitive.NewObjectID().Hex(),
	}
	err = db.SetCensusMembership(nonExistentMembership)
	c.Assert(err, qt.Not(qt.IsNil))

	// Test with non-existent participant
	nonExistentParticipantMembership := &CensusMembership{
		ParticipantNo: "non-existent",
		CensusID:      censusID,
	}
	err = db.SetCensusMembership(nonExistentParticipantMembership)
	c.Assert(err, qt.Not(qt.IsNil))

	// Create new membership
	err = db.SetCensusMembership(membership)
	c.Assert(err, qt.IsNil)

	// Verify the membership was created correctly
	createdMembership, err := db.CensusMembership(censusID, testMembershipParticipantNo)
	c.Assert(err, qt.IsNil)
	c.Assert(createdMembership.ParticipantNo, qt.Equals, testMembershipParticipantNo)
	c.Assert(createdMembership.CensusID, qt.Equals, censusID)
	c.Assert(createdMembership.CreatedAt.IsZero(), qt.IsFalse)
	c.Assert(createdMembership.UpdatedAt.IsZero(), qt.IsFalse)

	// Test updating an existing membership
	time.Sleep(time.Millisecond) // Ensure different UpdatedAt timestamp
	err = db.SetCensusMembership(membership)
	c.Assert(err, qt.IsNil)

	// Verify the membership was updated correctly
	updatedMembership, err := db.CensusMembership(censusID, testMembershipParticipantNo)
	c.Assert(err, qt.IsNil)
	c.Assert(updatedMembership.ParticipantNo, qt.Equals, testMembershipParticipantNo)
	c.Assert(updatedMembership.CensusID, qt.Equals, censusID)
	c.Assert(updatedMembership.CreatedAt, qt.Equals, createdMembership.CreatedAt)
	c.Assert(updatedMembership.UpdatedAt.After(createdMembership.UpdatedAt), qt.IsTrue)
}

func TestCensusMembership(t *testing.T) {
	c := qt.New(t)
	defer resetDB(c)

	// Setup prerequisites
	_, censusID := setupTestPrerequisites(c)

	// Test getting membership with invalid data
	_, err := db.CensusMembership("", testMembershipParticipantNo)
	c.Assert(err, qt.Equals, ErrInvalidData)

	_, err = db.CensusMembership(censusID, "")
	c.Assert(err, qt.Equals, ErrInvalidData)

	// Test getting non-existent membership
	_, err = db.CensusMembership(censusID, testMembershipParticipantNo)
	c.Assert(err, qt.Equals, ErrNotFound)

	// Create a membership to retrieve
	membership := &CensusMembership{
		ParticipantNo: testMembershipParticipantNo,
		CensusID:      censusID,
	}
	err = db.SetCensusMembership(membership)
	c.Assert(err, qt.IsNil)

	// Test getting existing membership
	retrievedMembership, err := db.CensusMembership(censusID, testMembershipParticipantNo)
	c.Assert(err, qt.IsNil)
	c.Assert(retrievedMembership.ParticipantNo, qt.Equals, testMembershipParticipantNo)
	c.Assert(retrievedMembership.CensusID, qt.Equals, censusID)
	c.Assert(retrievedMembership.CreatedAt.IsZero(), qt.IsFalse)
	c.Assert(retrievedMembership.UpdatedAt.IsZero(), qt.IsFalse)
}

func TestDelCensusMembership(t *testing.T) {
	c := qt.New(t)
	defer resetDB(c)

	// Setup prerequisites
	_, censusID := setupTestPrerequisites(c)

	// Test deleting with invalid data
	err := db.DelCensusMembership("", testMembershipParticipantNo)
	c.Assert(err, qt.Equals, ErrInvalidData)

	err = db.DelCensusMembership(censusID, "")
	c.Assert(err, qt.Equals, ErrInvalidData)

	// Create a membership to delete
	membership := &CensusMembership{
		ParticipantNo: testMembershipParticipantNo,
		CensusID:      censusID,
	}
	err = db.SetCensusMembership(membership)
	c.Assert(err, qt.IsNil)

	// Test deleting existing membership
	err = db.DelCensusMembership(censusID, testMembershipParticipantNo)
	c.Assert(err, qt.IsNil)

	// Verify the membership was deleted
	_, err = db.CensusMembership(censusID, testMembershipParticipantNo)
	c.Assert(err, qt.Equals, ErrNotFound)

	// Test deleting non-existent membership (should not error)
	err = db.DelCensusMembership(censusID, testMembershipParticipantNo)
	c.Assert(err, qt.IsNil)
}

func TestSetBulkCensusMembership(t *testing.T) {
	c := qt.New(t)
	defer resetDB(c)

	// Setup prerequisites
	_, censusID := setupTestPrerequisites(c)

	// Test with empty participants
	result, err := db.SetBulkCensusMembership("test_salt", censusID, nil)
	c.Assert(err, qt.IsNil)
	c.Assert(result, qt.IsNil)

	// Test with empty census ID
	participants := []OrgParticipant{
		{
			ParticipantNo: "test1",
			Email:         "test1@example.com",
			Phone:         "1234567890",
			Password:      "password1",
		},
	}
	result, err = db.SetBulkCensusMembership("test_salt", "", participants)
	c.Assert(err, qt.Equals, ErrInvalidData)
	c.Assert(result, qt.IsNil)

	// Test with non-existent census
	result, err = db.SetBulkCensusMembership("test_salt", primitive.NewObjectID().Hex(), participants)
	c.Assert(err, qt.Not(qt.IsNil))
	c.Assert(result, qt.IsNil)

	// Test successful bulk creation
	participants = []OrgParticipant{
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

	result, err = db.SetBulkCensusMembership("test_salt", censusID, participants)
	c.Assert(err, qt.IsNil)
	c.Assert(result, qt.Not(qt.IsNil))
	c.Assert(result.UpsertedCount, qt.Equals, int64(2))

	// Verify participants were created with hashed data
	for _, p := range participants {
		participant, err := db.OrgParticipantByNo(testOrgAddress, p.ParticipantNo)
		c.Assert(err, qt.IsNil)
		c.Assert(participant.Email, qt.Equals, "")
		c.Assert(participant.HashedEmail, qt.Not(qt.Equals), "")
		c.Assert(participant.Phone, qt.Equals, "")
		c.Assert(participant.HashedPhone, qt.Not(qt.Equals), "")
		c.Assert(participant.Password, qt.Equals, "")
		c.Assert(participant.HashedPass, qt.Not(qt.Equals), "")
		c.Assert(participant.CreatedAt.IsZero(), qt.IsFalse)

		// Verify memberships were created
		membership, err := db.CensusMembership(censusID, p.ParticipantNo)
		c.Assert(err, qt.IsNil)
		c.Assert(membership.ParticipantNo, qt.Equals, p.ParticipantNo)
		c.Assert(membership.CensusID, qt.Equals, censusID)
		c.Assert(membership.CreatedAt.IsZero(), qt.IsFalse)
	}

	// Test updating existing participants and memberships
	participants[0].Email = "updated1@example.com"
	participants[1].Phone = "1111111111"

	result, err = db.SetBulkCensusMembership("test_salt", censusID, participants)
	c.Assert(err, qt.IsNil)
	c.Assert(result, qt.Not(qt.IsNil))
	c.Assert(result.ModifiedCount, qt.Equals, int64(2))

	// Verify updates
	for _, p := range participants {
		participant, err := db.OrgParticipantByNo(testOrgAddress, p.ParticipantNo)
		c.Assert(err, qt.IsNil)
		c.Assert(participant.Email, qt.Equals, "")
		c.Assert(participant.HashedEmail, qt.Not(qt.Equals), "")
		c.Assert(participant.Phone, qt.Equals, "")
		c.Assert(participant.HashedPhone, qt.Not(qt.Equals), "")
	}
}
