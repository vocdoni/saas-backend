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

	t.Run("CheckOrgMemberAuthFields", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Create org
		organization := &Organization{
			Address: testOrgAddress,
		}
		err := testDB.SetOrganization(organization)
		c.Assert(err, qt.IsNil)

		// Create members with various field combinations for testing
		// Member 1: All fields valid
		member1 := &OrgMember{
			OrgAddress:   testOrgAddress,
			Email:        testMemberEmail,
			Phone:        testPhone,
			MemberNumber: testMemberNumber,
			Name:         testName,
			Surname:      "Smith",
			Password:     testPassword,
		}
		member1ID, err := testDB.SetOrgMember(testSalt, member1)
		c.Assert(err, qt.IsNil)

		// Member 2: All fields valid (different from member1)
		member2 := &OrgMember{
			OrgAddress:   testOrgAddress,
			Email:        "member2@test.com",
			Phone:        "+34678909091",
			MemberNumber: "member456",
			Name:         "Jane",
			Surname:      "Doe",
			Password:     testPassword,
		}
		member2ID, err := testDB.SetOrgMember(testSalt, member2)
		c.Assert(err, qt.IsNil)

		// Member 3: Duplicate fields with member1 (same name, surname, memberNumber)
		member3 := &OrgMember{
			OrgAddress:   testOrgAddress,
			Email:        "member3@test.com",
			Phone:        "+34678909092",
			MemberNumber: testMemberNumber, // Same as member1
			Name:         testName,         // Same as member1
			Surname:      "Smith",          // Same as member1
			Password:     testPassword,
		}
		member3ID, err := testDB.SetOrgMember(testSalt, member3)
		c.Assert(err, qt.IsNil)

		// Member 4: Empty fields
		member4 := &OrgMember{
			OrgAddress:   testOrgAddress,
			Email:        "member4@test.com",
			Phone:        "+34678909093",
			MemberNumber: "", // Empty memberNumber
			Name:         "", // Empty name
			Surname:      "Johnson",
			Password:     testPassword,
		}
		member4ID, err := testDB.SetOrgMember(testSalt, member4)
		c.Assert(err, qt.IsNil)

		// Create groups for testing
		// Group 1: All members
		allMembersGroup := &OrganizationMemberGroup{
			OrgAddress:  testOrgAddress,
			Title:       "All Members",
			Description: "Group containing all test members",
			MemberIDs:   []string{member1ID, member2ID, member3ID, member4ID},
		}
		allMembersGroupID, err := testDB.CreateOrganizationMemberGroup(allMembersGroup)
		c.Assert(err, qt.IsNil)

		// Group 2: Only members with duplicates (member1 and member3)
		duplicatesGroup := &OrganizationMemberGroup{
			OrgAddress:  testOrgAddress,
			Title:       "Duplicates Group",
			Description: "Group containing members with duplicate fields",
			MemberIDs:   []string{member1ID, member3ID},
		}
		duplicatesGroupID, err := testDB.CreateOrganizationMemberGroup(duplicatesGroup)
		c.Assert(err, qt.IsNil)

		// Group 3: Only members without duplicates or empties (member2)
		validGroup := &OrganizationMemberGroup{
			OrgAddress:  testOrgAddress,
			Title:       "Valid Group",
			Description: "Group containing only valid members",
			MemberIDs:   []string{member2ID},
		}
		validGroupID, err := testDB.CreateOrganizationMemberGroup(validGroup)
		c.Assert(err, qt.IsNil)

		// Note: We can't create an empty group through CreateOrganizationMemberGroup
		// as it validates that MemberIDs is not empty, so we test this case with non-existent groups

		// Test 1: Valid case - check fields with no duplicates or empties using valid group
		authFields := OrgMemberAuthFields{
			OrgMemberAuthFieldsEmail,
		}
		results, err := testDB.CheckOrgMemberAuthFields(testOrgAddress, validGroupID, authFields)
		c.Assert(err, qt.IsNil)
		c.Assert(results, qt.Not(qt.IsNil))
		c.Assert(len(results.Members), qt.Equals, 1)    // Only 1 member in valid group
		c.Assert(len(results.Duplicates), qt.Equals, 0) // No duplicates for email
		c.Assert(len(results.Empties), qt.Equals, 0)    // No empties for email

		// Test 2: Duplicate detection - check fields with known duplicates using duplicates group
		authFields = OrgMemberAuthFields{
			OrgMemberAuthFieldsName,
			OrgMemberAuthFieldsSurname,
			OrgMemberAuthFieldsMemberNumber,
		}
		results, err = testDB.CheckOrgMemberAuthFields(testOrgAddress, duplicatesGroupID, authFields)
		c.Assert(err, qt.IsNil)
		c.Assert(results, qt.Not(qt.IsNil))
		c.Assert(len(results.Members), qt.Equals, 2) // Both members in duplicates group

		// Should find duplicates (member1 and member3 have same name+surname+memberNumber)
		c.Assert(len(results.Duplicates) >= 2, qt.Equals, true)

		// Convert ObjectIDs to hex strings for easier comparison
		duplicateIDs := make([]string, len(results.Duplicates))
		for i, id := range results.Duplicates {
			duplicateIDs[i] = id.Hex()
		}

		// Check that member1 and member3 IDs are in the duplicates list
		c.Assert(contains(duplicateIDs, member1ID) && contains(duplicateIDs, member3ID), qt.Equals, true)

		// Test 3: Empty field detection using all members group
		authFields = OrgMemberAuthFields{
			OrgMemberAuthFieldsName,
			OrgMemberAuthFieldsMemberNumber,
		}
		results, err = testDB.CheckOrgMemberAuthFields(testOrgAddress, allMembersGroupID, authFields)
		c.Assert(err, qt.IsNil)
		c.Assert(results, qt.Not(qt.IsNil))

		// Should find empties (member4 has empty name and memberNumber)
		c.Assert(len(results.Empties) > 0, qt.Equals, true)

		// Convert ObjectIDs to hex strings for easier comparison
		emptyIDs := make([]string, len(results.Empties))
		for i, id := range results.Empties {
			emptyIDs[i] = id.Hex()
		}

		// Check that member4 ID is in the empties list
		c.Assert(contains(emptyIDs, member4ID), qt.Equals, true)

		// Test 4: Edge case - invalid organization address
		_, err = testDB.CheckOrgMemberAuthFields("", validGroupID, authFields)
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test 5: Edge case - empty auth fields
		_, err = testDB.CheckOrgMemberAuthFields(testOrgAddress, validGroupID, OrgMemberAuthFields{})
		c.Assert(err, qt.Not(qt.IsNil)) // Should return an error for empty auth fields

		// Test 6: Edge case - non-existent group ID
		nonExistentGroupID := primitive.NewObjectID().Hex()
		_, err = testDB.CheckOrgMemberAuthFields(testOrgAddress, nonExistentGroupID, authFields)
		c.Assert(err, qt.Not(qt.IsNil)) // Should return an error for non-existent group

		// Test 7: Edge case - group from different organization
		// Create another organization
		otherOrg := &Organization{
			Address: "0xotherorg",
		}
		err = testDB.SetOrganization(otherOrg)
		c.Assert(err, qt.IsNil)

		// Create a member for the other organization
		otherMember := &OrgMember{
			OrgAddress:   "0xotherorg",
			Email:        "other@test.com",
			MemberNumber: "other123",
			Name:         "Other",
			Surname:      "Member",
		}
		otherMemberID, err := testDB.SetOrgMember(testSalt, otherMember)
		c.Assert(err, qt.IsNil)

		// Create a group for the other organization
		otherGroup := &OrganizationMemberGroup{
			OrgAddress:  "0xotherorg",
			Title:       "Other Group",
			Description: "Group from different organization",
			MemberIDs:   []string{otherMemberID},
		}
		otherGroupID, err := testDB.CreateOrganizationMemberGroup(otherGroup)
		c.Assert(err, qt.IsNil)

		// Try to use the other organization's group with our organization
		_, err = testDB.CheckOrgMemberAuthFields(testOrgAddress, otherGroupID, authFields)
		c.Assert(err, qt.Not(qt.IsNil)) // Should return an error (group not found for this org)

		// Test 8: Test with all members group to ensure filtering works correctly
		authFields = OrgMemberAuthFields{
			OrgMemberAuthFieldsEmail,
		}
		results, err = testDB.CheckOrgMemberAuthFields(testOrgAddress, allMembersGroupID, authFields)
		c.Assert(err, qt.IsNil)
		c.Assert(results, qt.Not(qt.IsNil))
		c.Assert(len(results.Members), qt.Equals, 4)    // All 4 members should be found
		c.Assert(len(results.Duplicates), qt.Equals, 0) // No duplicates for email
		c.Assert(len(results.Empties), qt.Equals, 0)    // No empties for email
	})

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
			OrgAddress:   testOrgAddress,
			Email:        testMemberEmail,
			Phone:        testPhone,
			MemberNumber: testMemberNumber,
			Name:         testName,
			Password:     testPassword,
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
		c.Assert(createdMember.MemberNumber, qt.Equals, member.MemberNumber)
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
			ID:           updatedMember.ID, // Use the same ID to simulate a duplicate
			OrgAddress:   testOrgAddress,
			Email:        testMemberEmail,
			Phone:        testPhone,
			MemberNumber: testMemberNumber,
			Name:         testName,
			Password:     testPassword,
		}

		// Attempt to update member
		duplicateMember.ID = updatedMember.ID
		duplicateID, err := testDB.SetOrgMember(testSalt, duplicateMember)
		c.Assert(err, qt.IsNil)
		c.Assert(duplicateID, qt.Equals, memberOID)

		// Verify the duplicate member was not created but updated
		duplicateCreatedMember, err := testDB.OrgMember(testOrgAddress, duplicateID)
		c.Assert(err, qt.IsNil)
		c.Assert(duplicateCreatedMember.MemberNumber, qt.Equals, testMemberNumber)
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
			OrgAddress:   testOrgAddress,
			Email:        testMemberEmail,
			MemberNumber: testMemberNumber,
			Name:         testName,
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
			OrgAddress:   testOrgAddress,
			Email:        testMemberEmail,
			MemberNumber: testMemberNumber,
			Name:         testName,
		}

		// Create new member
		memberOID, err := testDB.SetOrgMember(testSalt, member)
		c.Assert(err, qt.IsNil)

		// Test getting member with valid ID
		retrievedMember, err := testDB.OrgMember(testOrgAddress, memberOID)
		c.Assert(err, qt.IsNil)
		c.Assert(retrievedMember.Email, qt.Equals, testMemberEmail)
		c.Assert(retrievedMember.MemberNumber, qt.Equals, testMemberNumber)
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
				Email:        testMemberEmail,
				Phone:        testPhone,
				MemberNumber: testMemberNumber,
				Name:         testName,
				Password:     testPassword,
			},
			{
				Email:        "member2@test.com",
				Phone:        "+34678678978",
				MemberNumber: "member456",
				Name:         "Test Member 2",
				Password:     "testpass456",
			},
		}

		// Perform bulk upsert
		progressChan, err := testDB.SetBulkOrgMembers(testOrgAddress, testSalt, members)
		c.Assert(err, qt.IsNil)

		// Wait for the operation to complete and get the final status
		var lastStatus *BulkOrgMembersJob
		for status := range progressChan {
			lastStatus = status
		}

		// Verify the operation completed successfully
		c.Assert(lastStatus, qt.Not(qt.IsNil))
		c.Assert(lastStatus.Progress, qt.Equals, 100)
		c.Assert(lastStatus.Added, qt.Equals, 2)
		c.Assert(lastStatus.Errors, qt.HasLen, 0)

		// Verify both members were created with hashed fields
		member1, err := testDB.OrgMemberByMemberNumber(testOrgAddress, testMemberNumber)
		c.Assert(err, qt.IsNil)
		c.Assert(member1.HashedPhone, qt.DeepEquals, internal.HashOrgData(testOrgAddress, testPhone))
		c.Assert(member1.HashedPass, qt.DeepEquals, internal.HashPassword(testSalt, testPassword))

		member2, err := testDB.OrgMemberByMemberNumber(testOrgAddress, members[1].MemberNumber)
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
		c.Assert(lastStatus.Errors, qt.HasLen, 0)

		// Verify updates for both members
		updatedMember1, err := testDB.OrgMemberByMemberNumber(testOrgAddress, testMemberNumber)
		c.Assert(err, qt.IsNil)
		c.Assert(updatedMember1.Name, qt.Equals, "Updated Name")
		c.Assert(updatedMember1.Email, qt.Equals, testMemberEmail)

		updatedMember2, err := testDB.OrgMemberByMemberNumber(testOrgAddress, "member456")
		c.Assert(err, qt.IsNil)
		c.Assert(updatedMember2.HashedPhone, qt.DeepEquals, internal.HashOrgData(testOrgAddress, members[1].Phone))
		c.Assert(updatedMember2.Name, qt.Equals, "Test Member 2")

		// Test with empty organization address
		_, err = testDB.SetBulkOrgMembers("", testSalt, members)
		c.Assert(err, qt.Not(qt.IsNil))
	})
}
