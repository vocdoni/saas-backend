package db

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestPopulateGroupCensus(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.Reset(), qt.IsNil) })

	t.Run("InputValidation", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Create test organization first
		org := &Organization{
			Address:   testOrgAddress,
			Active:    true,
			CreatedAt: time.Now(),
		}
		err := testDB.SetOrganization(org)
		c.Assert(err, qt.IsNil)

		// Test with empty orgAddress
		invalidCensus := &Census{
			OrgAddress: common.Address{},
			TwoFaFields: OrgMemberTwoFaFields{
				OrgMemberTwoFaFieldEmail,
			},
		}
		_, err = testDB.PopulateGroupCensus(invalidCensus, "some-group-id")
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test with non-existent organization
		nonExistentCensus := &Census{
			OrgAddress: testNonExistentOrg,
			TwoFaFields: OrgMemberTwoFaFields{
				OrgMemberTwoFaFieldEmail,
			},
		}
		_, err = testDB.PopulateGroupCensus(nonExistentCensus, "some-group-id")
		c.Assert(err, qt.Not(qt.IsNil))
		c.Assert(err.Error(), qt.Contains, "invalid data provided")

		// Test with non-existent group
		nonExistentGroupCensus := &Census{
			OrgAddress: testOrgAddress,
			TwoFaFields: OrgMemberTwoFaFields{
				OrgMemberTwoFaFieldEmail,
			},
		}
		nonExistentGroupID := primitive.NewObjectID().Hex()
		_, err = testDB.PopulateGroupCensus(nonExistentGroupCensus, nonExistentGroupID)
		c.Assert(err, qt.Not(qt.IsNil))
		c.Assert(err.Error(), qt.Contains, "invalid data provided")

		// Test with invalid groupID format
		invalidGroupCensus := &Census{
			OrgAddress: testOrgAddress,
			TwoFaFields: OrgMemberTwoFaFields{
				OrgMemberTwoFaFieldEmail,
			},
		}
		_, err = testDB.PopulateGroupCensus(invalidGroupCensus, "invalid-group-id-format")
		c.Assert(err, qt.Not(qt.IsNil))
	})

	t.Run("GroupValidation", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Create test organizations
		org1 := &Organization{
			Address:   testOrgAddress,
			Active:    true,
			CreatedAt: time.Now(),
		}
		err := testDB.SetOrganization(org1)
		c.Assert(err, qt.IsNil)

		org2 := &Organization{
			Address:   testAnotherOrgAddress,
			Active:    true,
			CreatedAt: time.Now(),
		}
		err = testDB.SetOrganization(org2)
		c.Assert(err, qt.IsNil)

		// Create members for org1
		member1 := &OrgMember{
			OrgAddress: testOrgAddress,
			Email:      "member1@example.com",
			Name:       "Member 1",
		}
		member1ID, err := testDB.SetOrgMember(testSalt, member1)
		c.Assert(err, qt.IsNil)

		// Create members for org2
		member2 := &OrgMember{
			OrgAddress: testAnotherOrgAddress,
			Email:      "member2@example.com",
			Name:       "Member 2",
		}
		member2ID, err := testDB.SetOrgMember(testSalt, member2)
		c.Assert(err, qt.IsNil)

		// Create a group for org1
		group1 := &OrganizationMemberGroup{
			OrgAddress:  testOrgAddress,
			Title:       "Test Group 1",
			Description: "Test Group 1 Description",
			MemberIDs:   []string{member1ID},
		}
		group1ID, err := testDB.CreateOrganizationMemberGroup(group1)
		c.Assert(err, qt.IsNil)
		group1OID, err := primitive.ObjectIDFromHex(group1ID)
		c.Assert(err, qt.IsNil)

		// Create a group for org2
		group2 := &OrganizationMemberGroup{
			OrgAddress:  testAnotherOrgAddress,
			Title:       "Test Group 2",
			Description: "Test Group 2 Description",
			MemberIDs:   []string{member2ID},
		}
		group2ID, err := testDB.CreateOrganizationMemberGroup(group2)
		c.Assert(err, qt.IsNil)

		// Test with valid organization but group belonging to different organization
		census1 := &Census{
			OrgAddress: testOrgAddress,
			TwoFaFields: OrgMemberTwoFaFields{
				OrgMemberTwoFaFieldEmail,
			},
		}
		_, err = testDB.PopulateGroupCensus(census1, group2ID)
		c.Assert(err, qt.Not(qt.IsNil))
		c.Assert(err.Error(), qt.Contains, "invalid data provided")

		// Test with valid group and organization combination
		census2ID := primitive.NewObjectID()
		census2 := &Census{
			ID:         census2ID,
			GroupID:    group1OID,
			OrgAddress: testOrgAddress,
			TwoFaFields: OrgMemberTwoFaFields{
				OrgMemberTwoFaFieldEmail,
			},
		}
		_, err = testDB.PopulateGroupCensus(census2, group1ID)
		c.Assert(err, qt.IsNil)

		// Verify the census was created correctly with the group ID
		createdCensus, err := testDB.Census(census2ID.Hex())
		c.Assert(err, qt.IsNil)
		c.Assert(createdCensus.OrgAddress, qt.Equals, testOrgAddress)
		c.Assert(createdCensus.Type, qt.Equals, CensusTypeMail)
		c.Assert(createdCensus.GroupID.Hex(), qt.Equals, group1ID)

		// Verify that the group was updated with the census ID
		group, err := testDB.OrganizationMemberGroup(group1ID, testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(group.CensusIDs, qt.HasLen, 1)
		c.Assert(group.CensusIDs[0], qt.Equals, census2ID.Hex())
	})

	t.Run("CensusCreation", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Create test organization
		org := &Organization{
			Address:   testOrgAddress,
			Active:    true,
			CreatedAt: time.Now(),
		}
		err := testDB.SetOrganization(org)
		c.Assert(err, qt.IsNil)

		// Create a member for the group
		member := &OrgMember{
			OrgAddress: testOrgAddress,
			Email:      "member@example.com",
			Name:       "Test Member",
		}
		memberID, err := testDB.SetOrgMember(testSalt, member)
		c.Assert(err, qt.IsNil)
		c.Assert(err, qt.IsNil)

		// Create a group
		group := &OrganizationMemberGroup{
			OrgAddress:  testOrgAddress,
			Title:       "Test Group",
			Description: "Test Group Description",
			MemberIDs:   []string{memberID},
		}
		groupID, err := testDB.CreateOrganizationMemberGroup(group)
		c.Assert(err, qt.IsNil)
		groupOID, err := primitive.ObjectIDFromHex(groupID)
		c.Assert(err, qt.IsNil)

		// Test creating new census with group
		census := &Census{
			GroupID:    groupOID,
			OrgAddress: testOrgAddress,
			TwoFaFields: OrgMemberTwoFaFields{
				OrgMemberTwoFaFieldEmail,
			},
		}
		censusID, err := testDB.SetCensus(census)
		c.Assert(err, qt.IsNil)
		c.Assert(censusID, qt.Not(qt.Equals), "")

		// Verify the census was created correctly
		createdCensus, err := testDB.Census(censusID)
		c.Assert(err, qt.IsNil)
		c.Assert(createdCensus.OrgAddress, qt.Equals, testOrgAddress)
		c.Assert(createdCensus.Type, qt.Equals, CensusTypeMail)
		c.Assert(createdCensus.GroupID.Hex(), qt.Equals, groupID)
		c.Assert(createdCensus.CreatedAt.IsZero(), qt.IsFalse)

		// Test updating existing census with group
		createdCensus.Type = CensusTypeSMS
		createdCensus.TwoFaFields = OrgMemberTwoFaFields{
			OrgMemberTwoFaFieldPhone,
		}

		// Ensure different UpdatedAt timestamp
		time.Sleep(time.Millisecond)

		// Update census
		inserted, err := testDB.PopulateGroupCensus(createdCensus, groupID)
		c.Assert(err, qt.IsNil)
		c.Assert(inserted, qt.Equals, int64(1))

		// Verify the census was updated correctly
		updatedCensus, err := testDB.Census(createdCensus.ID.Hex())
		c.Assert(err, qt.IsNil)
		c.Assert(updatedCensus.Type, qt.Equals, CensusTypeSMS)
		c.Assert(updatedCensus.GroupID.Hex(), qt.Equals, groupID)
		c.Assert(updatedCensus.CreatedAt, qt.Equals, createdCensus.CreatedAt)
		c.Assert(updatedCensus.UpdatedAt.After(createdCensus.CreatedAt), qt.IsTrue)
	})

	t.Run("ParticipantHandling", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Create test organization
		org := &Organization{
			Address:   testOrgAddress,
			Active:    true,
			CreatedAt: time.Now(),
		}
		err := testDB.SetOrganization(org)
		c.Assert(err, qt.IsNil)

		// Create test members
		member1 := &OrgMember{
			OrgAddress: testOrgAddress,
			Email:      "member1@example.com",
			Name:       "Member 1",
		}
		member1ID, err := testDB.SetOrgMember(testSalt, member1)
		c.Assert(err, qt.IsNil)
		c.Assert(member1ID, qt.Not(qt.Equals), "")

		member2 := &OrgMember{
			OrgAddress: testOrgAddress,
			Email:      "member2@example.com",
			Name:       "Member 2",
		}
		member2ID, err := testDB.SetOrgMember(testSalt, member2)
		c.Assert(err, qt.IsNil)
		c.Assert(member2ID, qt.Not(qt.Equals), "")

		// Test with group that has one member
		singleMemberGroup := &OrganizationMemberGroup{
			OrgAddress:  testOrgAddress,
			Title:       "Single Member Group",
			Description: "Single Member Group Description",
			MemberIDs:   []string{member1ID},
		}
		singleGroupID, err := testDB.CreateOrganizationMemberGroup(singleMemberGroup)
		c.Assert(err, qt.IsNil)

		censusOID2 := primitive.NewObjectID()
		census2 := &Census{
			ID:         censusOID2,
			OrgAddress: testOrgAddress,
			TwoFaFields: OrgMemberTwoFaFields{
				OrgMemberTwoFaFieldEmail,
			},
		}
		inserted2, err := testDB.PopulateGroupCensus(census2, singleGroupID)
		c.Assert(err, qt.IsNil)
		c.Assert(inserted2, qt.Equals, int64(1))

		// Verify one participant was added
		participants2, err := testDB.CensusParticipants(censusOID2.Hex())
		c.Assert(err, qt.IsNil)
		c.Assert(participants2, qt.HasLen, 1)
		c.Assert(participants2[0].ParticipantID, qt.Equals, member1ID)

		// Test with group that has multiple members
		multiMemberGroup := &OrganizationMemberGroup{
			OrgAddress:  testOrgAddress,
			Title:       "Multi Member Group",
			Description: "Multi Member Group Description",
			MemberIDs:   []string{member1ID, member2ID},
		}
		multiGroupID, err := testDB.CreateOrganizationMemberGroup(multiMemberGroup)
		c.Assert(err, qt.IsNil)

		censusOID3 := primitive.NewObjectID()
		census3 := &Census{
			ID:         censusOID3,
			OrgAddress: testOrgAddress,
			TwoFaFields: OrgMemberTwoFaFields{
				OrgMemberTwoFaFieldEmail,
			},
		}
		inserted3, err := testDB.PopulateGroupCensus(census3, multiGroupID)
		c.Assert(err, qt.IsNil)
		c.Assert(inserted3, qt.Equals, int64(2))

		// Verify both participants were added
		participants3, err := testDB.CensusParticipants(censusOID3.Hex())
		c.Assert(err, qt.IsNil)
		c.Assert(participants3, qt.HasLen, 2)

		// Verify the correct participants were added
		participantMap := make(map[string]bool)
		for _, p := range participants3 {
			participantMap[p.ParticipantID] = true
		}
		c.Assert(participantMap[member1ID], qt.IsTrue)
		c.Assert(participantMap[member2ID], qt.IsTrue)
	})
}

func TestCensus(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.Reset(), qt.IsNil) })

	t.Run("SetCensus", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Test with non-existent organization
		nonExistentCensus := &Census{
			OrgAddress: testNonExistentOrg,
			TwoFaFields: OrgMemberTwoFaFields{
				OrgMemberTwoFaFieldEmail,
			},
		}
		_, err := testDB.SetCensus(nonExistentCensus)
		c.Assert(err, qt.Not(qt.IsNil))
		c.Assert(err.Error(), qt.Contains, "invalid data provided")

		// Create test organization first
		org := &Organization{
			Address:   testOrgAddress,
			Active:    true,
			CreatedAt: time.Now(),
		}
		err = testDB.SetOrganization(org)
		c.Assert(err, qt.IsNil)

		// Test with invalid data
		invalidCensus := &Census{
			OrgAddress: testNonExistentOrg,
			TwoFaFields: OrgMemberTwoFaFields{
				OrgMemberTwoFaFieldEmail,
			},
		}
		_, err = testDB.SetCensus(invalidCensus)
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test creating a new census
		census := &Census{
			OrgAddress: testOrgAddress,
			TwoFaFields: OrgMemberTwoFaFields{
				OrgMemberTwoFaFieldEmail,
			},
		}

		// Create new census
		censusID, err := testDB.SetCensus(census)
		c.Assert(err, qt.IsNil)
		c.Assert(censusID, qt.Not(qt.Equals), "")

		// Verify the census was created correctly
		createdCensus, err := testDB.Census(censusID)
		c.Assert(err, qt.IsNil)
		c.Assert(createdCensus.OrgAddress, qt.DeepEquals, testOrgAddress)
		c.Assert(createdCensus.Type, qt.Equals, CensusTypeMail)
		c.Assert(createdCensus.CreatedAt.IsZero(), qt.IsFalse)

		// Test updating an existing census
		createdCensus.Type = CensusTypeSMS
		createdCensus.TwoFaFields = OrgMemberTwoFaFields{
			OrgMemberTwoFaFieldPhone,
		}

		// Ensure different UpdatedAt timestamp
		time.Sleep(time.Millisecond)

		// Update census
		updatedID, err := testDB.SetCensus(createdCensus)
		c.Assert(err, qt.IsNil)
		c.Assert(updatedID, qt.Equals, censusID)

		// Verify the census was updated correctly
		updatedCensus, err := testDB.Census(updatedID)
		c.Assert(err, qt.IsNil)
		c.Assert(updatedCensus.Type, qt.Equals, CensusTypeSMS)
		c.Assert(updatedCensus.CreatedAt, qt.Equals, createdCensus.CreatedAt)
		c.Assert(updatedCensus.UpdatedAt.After(createdCensus.CreatedAt), qt.IsTrue)
	})

	t.Run("DelCensus", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Create test organization first
		org := &Organization{
			Address:   testOrgAddress,
			Active:    true,
			CreatedAt: time.Now(),
		}
		err := testDB.SetOrganization(org)
		c.Assert(err, qt.IsNil)

		// Create a census to delete
		census := &Census{
			OrgAddress: testOrgAddress,
			TwoFaFields: OrgMemberTwoFaFields{
				OrgMemberTwoFaFieldEmail,
			},
		}

		// Create new census
		censusID, err := testDB.SetCensus(census)
		c.Assert(err, qt.IsNil)

		// Test deleting with invalid ID
		err = testDB.DelCensus("")
		c.Assert(err, qt.Equals, ErrInvalidData)

		err = testDB.DelCensus("invalid-id")
		c.Assert(err, qt.Not(qt.IsNil))

		// Test deleting with valid ID
		err = testDB.DelCensus(censusID)
		c.Assert(err, qt.IsNil)

		// Verify the census was deleted
		_, err = testDB.Census(censusID)
		c.Assert(err, qt.Not(qt.IsNil))
	})

	t.Run("GetCensus", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Create test organization first
		org := &Organization{
			Address:   testOrgAddress,
			Active:    true,
			CreatedAt: time.Now(),
		}
		err := testDB.SetOrganization(org)
		c.Assert(err, qt.IsNil)

		// Test getting census with invalid ID
		_, err = testDB.Census("")
		c.Assert(err, qt.Equals, ErrInvalidData)

		_, err = testDB.Census("invalid-id")
		c.Assert(err, qt.Not(qt.IsNil))

		// Create a census to retrieve
		census := &Census{
			OrgAddress: testOrgAddress,
			TwoFaFields: OrgMemberTwoFaFields{
				OrgMemberTwoFaFieldEmail,
			},
		}

		// Create new census
		censusID, err := testDB.SetCensus(census)
		c.Assert(err, qt.IsNil)

		// Test getting census with valid ID
		retrievedCensus, err := testDB.Census(censusID)
		c.Assert(err, qt.IsNil)
		c.Assert(retrievedCensus.OrgAddress, qt.DeepEquals, testOrgAddress)
		c.Assert(retrievedCensus.Type, qt.Equals, CensusTypeMail)
		c.Assert(retrievedCensus.CreatedAt.IsZero(), qt.IsFalse)

		// Test getting non-existent census
		nonExistentID := primitive.NewObjectID().Hex()
		_, err = testDB.Census(nonExistentID)
		c.Assert(err, qt.Not(qt.IsNil))
	})

	t.Run("CensusesByOrg", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Create test organization first
		org := &Organization{
			Address:   testOrgAddress,
			Active:    true,
			CreatedAt: time.Now(),
		}
		err := testDB.SetOrganization(org)
		c.Assert(err, qt.IsNil)

		// Try to get censuses for non-existent organization
		_, err = testDB.CensusesByOrg(testNonExistentOrg)
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Get censuses for the organization (should be empty)
		emptyCensuses, err := testDB.CensusesByOrg(testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(emptyCensuses, qt.HasLen, 0)

		// Create a census for the organization
		firstCensus := &Census{
			OrgAddress: testOrgAddress,
			TwoFaFields: OrgMemberTwoFaFields{
				OrgMemberTwoFaFieldEmail,
			},
		}
		firstCensusID, err := testDB.SetCensus(firstCensus)
		c.Assert(err, qt.IsNil)

		// Get censuses for the organization (should have one)
		censuses, err := testDB.CensusesByOrg(testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(censuses, qt.HasLen, 1)
		c.Assert(censuses[0].ID.Hex(), qt.Equals, firstCensusID)
		c.Assert(censuses[0].OrgAddress, qt.DeepEquals, testOrgAddress)
		c.Assert(censuses[0].Type, qt.Equals, CensusTypeMail)

		// Create another census for the organization
		secondCensus := &Census{
			OrgAddress: testOrgAddress,
			TwoFaFields: OrgMemberTwoFaFields{
				OrgMemberTwoFaFieldPhone,
			},
		}
		secondCensusID, err := testDB.SetCensus(secondCensus)
		c.Assert(err, qt.IsNil)

		// Get censuses for the organization (should have two)
		censuses, err = testDB.CensusesByOrg(testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(censuses, qt.HasLen, 2)
		c.Assert(censuses[0].ID.Hex(), qt.Equals, firstCensusID)
		c.Assert(censuses[0].OrgAddress, qt.DeepEquals, testOrgAddress)
		c.Assert(censuses[0].Type, qt.Equals, CensusTypeMail)
		c.Assert(censuses[1].ID.Hex(), qt.Equals, secondCensusID)
		c.Assert(censuses[1].OrgAddress, qt.DeepEquals, testOrgAddress)
		c.Assert(censuses[1].Type, qt.Equals, CensusTypeSMS)

		// Remove the first census
		err = testDB.DelCensus(firstCensusID)
		c.Assert(err, qt.IsNil)

		// Get censuses for the organization (should have one)
		censuses, err = testDB.CensusesByOrg(testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(censuses, qt.HasLen, 1)
		c.Assert(censuses[0].ID.Hex(), qt.Equals, secondCensusID)
		c.Assert(censuses[0].OrgAddress, qt.DeepEquals, testOrgAddress)
		c.Assert(censuses[0].Type, qt.Equals, CensusTypeSMS)
	})

	t.Run("ZeroAddressValidation", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)

		// Test SetCensus with zero address - should fail
		zeroAddrCensus := &Census{
			OrgAddress: common.Address{}, // Zero address
			TwoFaFields: OrgMemberTwoFaFields{
				OrgMemberTwoFaFieldEmail,
			},
		}
		_, err := testDB.SetCensus(zeroAddrCensus)
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test CensusesByOrg with zero address - should fail
		_, err = testDB.CensusesByOrg(common.Address{})
		c.Assert(err, qt.Equals, ErrInvalidData)
	})
}
