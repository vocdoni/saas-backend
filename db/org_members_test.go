package db

import (
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestOrgMembers(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.Reset(), qt.IsNil) })

	t.Run("SetOrgMember", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Create org
		organization := &Organization{
			Address: testOrgAddress,
		}
		err := testDB.SetOrganization(organization)
		c.Assert(err, qt.IsNil)

		// Test creating a new member
		member := &OrgMember{
			OrgAddress: testOrgAddress,
			Email:      testMemberEmail,
			Phone:      testPhone,
			MemberID:   testMemberID,
			Name:       testName,
			Password:   testPassword,
		}

		// Create new member
		memberOID, err := testDB.SetOrgMember(testSalt, member)
		c.Assert(err, qt.IsNil)
		c.Assert(memberOID, qt.Not(qt.Equals), "")

		// Verify the member was created correctly
		createdMember, err := testDB.OrgMember(testOrgAddress, memberOID)
		c.Assert(err, qt.IsNil)
		c.Assert(createdMember.Email, qt.Equals, testMemberEmail)
		c.Assert(createdMember.HashedPhone, qt.DeepEquals, internal.HashOrgData(testOrgAddress, testPhone))
		c.Assert(createdMember.MemberID, qt.Equals, member.MemberID)
		c.Assert(createdMember.Name, qt.Equals, testName)
		c.Assert(createdMember.HashedPass, qt.DeepEquals, internal.HashPassword(testSalt, testPassword))
		c.Assert(createdMember.CreatedAt, qt.Not(qt.IsNil))

		// Test updating an existing member
		newName := "Updated Name"
		newPhone := "+34655432100"
		createdMember.Name = newName
		createdMember.Phone = newPhone

		// Update member
		updatedID, err := testDB.SetOrgMember(testSalt, createdMember)
		c.Assert(err, qt.IsNil)
		c.Assert(updatedID, qt.Equals, memberOID)

		// Verify the member was updated correctly
		updatedMember, err := testDB.OrgMember(testOrgAddress, updatedID)
		c.Assert(err, qt.IsNil)
		c.Assert(updatedMember.Name, qt.Equals, newName)
		c.Assert(updatedMember.HashedPhone, qt.DeepEquals, internal.HashOrgData(testOrgAddress, newPhone))
		c.Assert(updatedMember.CreatedAt, qt.Equals, createdMember.CreatedAt)

		duplicateMember := &OrgMember{
			ID:         updatedMember.ID, // Use the same ID to simulate a duplicate
			OrgAddress: testOrgAddress,
			Email:      testMemberEmail,
			Phone:      testPhone,
			MemberID:   testMemberID,
			Name:       testName,
			Password:   testPassword,
		}

		// Attempt to update member
		duplicateMember.ID = updatedMember.ID
		duplicateID, err := testDB.SetOrgMember(testSalt, duplicateMember)
		c.Assert(err, qt.IsNil)
		c.Assert(duplicateID, qt.Equals, memberOID)

		// Verify the duplicate member was not created but updated
		duplicateCreatedMember, err := testDB.OrgMember(testOrgAddress, duplicateID)
		c.Assert(err, qt.IsNil)
		c.Assert(duplicateCreatedMember.MemberID, qt.Equals, testMemberID)
		c.Assert(duplicateCreatedMember.Name, qt.Equals, testName)
	})

	t.Run("DelOrgMember", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Create org
		organization := &Organization{
			Address: testOrgAddress,
		}
		err := testDB.SetOrganization(organization)
		c.Assert(err, qt.IsNil)

		// Create a member to delete
		member := &OrgMember{
			OrgAddress: testOrgAddress,
			Email:      testMemberEmail,
			MemberID:   testMemberID,
			Name:       testName,
		}

		// Create new member
		memberOID, err := testDB.SetOrgMember(testSalt, member)
		c.Assert(err, qt.IsNil)

		// Test deleting with invalid ID
		err = testDB.DelOrgMember("invalid-id")
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test deleting with valid ID
		err = testDB.DelOrgMember(memberOID)
		c.Assert(err, qt.IsNil)

		// Verify the member was deleted
		_, err = testDB.OrgMember(testOrgAddress, memberOID)
		c.Assert(err, qt.Not(qt.IsNil))
	})

	t.Run("GetOrgMember", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Create org
		organization := &Organization{
			Address: testOrgAddress,
		}
		err := testDB.SetOrganization(organization)
		c.Assert(err, qt.IsNil)

		// Test getting member with invalid ID
		_, err = testDB.OrgMember(testOrgAddress, "invalid-id")
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Create a member to retrieve
		member := &OrgMember{
			OrgAddress: testOrgAddress,
			Email:      testMemberEmail,
			MemberID:   testMemberID,
			Name:       testName,
		}

		// Create new member
		memberOID, err := testDB.SetOrgMember(testSalt, member)
		c.Assert(err, qt.IsNil)

		// Test getting member with valid ID
		retrievedMember, err := testDB.OrgMember(testOrgAddress, memberOID)
		c.Assert(err, qt.IsNil)
		c.Assert(retrievedMember.Email, qt.Equals, testMemberEmail)
		c.Assert(retrievedMember.MemberID, qt.Equals, testMemberID)
		c.Assert(retrievedMember.Name, qt.Equals, testName)
		c.Assert(retrievedMember.CreatedAt, qt.Not(qt.IsNil))

		// Test getting non-existent member
		nonExistentID := primitive.NewObjectID().Hex()
		_, err = testDB.OrgMember(testOrgAddress, nonExistentID)
		c.Assert(err, qt.Not(qt.IsNil))
	})

	t.Run("SetBulkOrgMembers", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Create org
		organization := &Organization{
			Address: testOrgAddress,
		}
		err := testDB.SetOrganization(organization)
		c.Assert(err, qt.IsNil)

		// Test bulk insert of new members
		members := []OrgMember{
			{
				Email:    testMemberEmail,
				Phone:    testPhone,
				MemberID: testMemberID,
				Name:     testName,
				Password: testPassword,
			},
			{
				Email:    "member2@test.com",
				Phone:    "+34678678978",
				MemberID: "member456",
				Name:     "Test Member 2",
				Password: "testpass456",
			},
		}

		// Perform bulk upsert
		progressChan, err := testDB.SetBulkOrgMembers(testOrgAddress, testSalt, members)
		c.Assert(err, qt.IsNil)

		// Wait for the operation to complete and get the final status
		var lastStatus *BulkOrgMembersStatus
		for status := range progressChan {
			lastStatus = status
		}

		// Verify the operation completed successfully
		c.Assert(lastStatus, qt.Not(qt.IsNil))
		c.Assert(lastStatus.Progress, qt.Equals, 100)
		c.Assert(lastStatus.Added, qt.Equals, 2)

		// Verify both members were created with hashed fields
		member1, err := testDB.OrgMemberByMemberID(testOrgAddress, testMemberID)
		c.Assert(err, qt.IsNil)
		c.Assert(member1.HashedPhone, qt.DeepEquals, internal.HashOrgData(testOrgAddress, testPhone))
		c.Assert(member1.HashedPass, qt.DeepEquals, internal.HashPassword(testSalt, testPassword))

		member2, err := testDB.OrgMemberByMemberID(testOrgAddress, members[1].MemberID)
		c.Assert(err, qt.IsNil)
		c.Assert(member2.HashedPhone, qt.DeepEquals, internal.HashOrgData(testOrgAddress, members[1].Phone))
		c.Assert(member2.HashedPass, qt.DeepEquals, internal.HashPassword(testSalt, members[1].Password))

		// Test updating existing members
		members[0].ID = member1.ID // Use the existing ID for the first member
		members[0].Name = "Updated Name"
		members[1].ID = member2.ID // Use the existing ID for the second member
		members[1].Phone = "+34678678971"

		// Perform bulk upsert again
		progressChan, err = testDB.SetBulkOrgMembers(testOrgAddress, testSalt, members)
		c.Assert(err, qt.IsNil)

		// Wait for the operation to complete and get the final status
		for status := range progressChan {
			lastStatus = status
		}

		// Verify the operation completed successfully
		c.Assert(lastStatus, qt.Not(qt.IsNil))
		c.Assert(lastStatus.Progress, qt.Equals, 100)
		c.Assert(lastStatus.Added, qt.Equals, 2) // Both documents should be updated

		// Verify updates for both members
		updatedMember1, err := testDB.OrgMemberByMemberID(testOrgAddress, testMemberID)
		c.Assert(err, qt.IsNil)
		c.Assert(updatedMember1.Name, qt.Equals, "Updated Name")
		c.Assert(updatedMember1.Email, qt.Equals, testMemberEmail)

		updatedMember2, err := testDB.OrgMemberByMemberID(testOrgAddress, "member456")
		c.Assert(err, qt.IsNil)
		c.Assert(updatedMember2.HashedPhone, qt.DeepEquals, internal.HashOrgData(testOrgAddress, members[1].Phone))
		c.Assert(updatedMember2.Name, qt.Equals, "Test Member 2")

		// Test with empty organization address
		_, err = testDB.SetBulkOrgMembers("", testSalt, members)
		c.Assert(err, qt.Not(qt.IsNil))
	})
}
