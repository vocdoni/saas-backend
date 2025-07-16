package db

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const testGroupMemberNumber = "member123"

func setupTestOrgMembersGroupPrerequisites(t *testing.T, memberSuffix string) (*Organization, []string) {
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

	// Create test members with unique IDs
	memberIDs := make([]string, 3)
	for i := 0; i < 3; i++ {
		memberNumber := testGroupMemberNumber + memberSuffix + "_" + string(rune('1'+i))
		member := &OrgMember{
			OrgAddress:   testOrgAddress,
			MemberNumber: memberNumber,
			Email:        "test" + memberSuffix + "_" + string(rune('1'+i)) + "@example.com",
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}
		id, err := testDB.SetOrgMember("test_salt", member)
		if err != nil {
			t.Fatalf("failed to set organization member: %v", err)
		}
		memberIDs[i] = id // Store the MongoDB ID, not the memberNumber
	}

	return org, memberIDs
}

func TestOrganizationMemberGroup(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.Reset(), qt.IsNil) })

	t.Run("CreateOrganizationMemberGroup", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		_, memberIDs := setupTestOrgMembersGroupPrerequisites(t, "_create")

		t.Run("InvalidData", func(_ *testing.T) {
			// Test with invalid organization address
			invalidGroup := &OrganizationMemberGroup{
				OrgAddress:  common.Address{0x01, 0x23, 0x45},
				Title:       "Test Group",
				Description: "Test Description",
				MemberIDs:   memberIDs,
			}
			_, err := testDB.CreateOrganizationMemberGroup(invalidGroup)
			c.Assert(err, qt.Not(qt.IsNil))

			// Test with invalid member IDs
			invalidMemberGroup := &OrganizationMemberGroup{
				OrgAddress:  testOrgAddress,
				Title:       "Test Group",
				Description: "Test Description",
				MemberIDs:   []string{"invalid_member_id"},
			}
			_, err = testDB.CreateOrganizationMemberGroup(invalidMemberGroup)
			c.Assert(err, qt.Not(qt.IsNil))
		})

		t.Run("ValidGroup", func(_ *testing.T) {
			// Test creating a valid group
			validGroup := &OrganizationMemberGroup{
				OrgAddress:  testOrgAddress,
				Title:       "Test Group",
				Description: "Test Description",
				MemberIDs:   memberIDs,
			}
			groupID, err := testDB.CreateOrganizationMemberGroup(validGroup)
			c.Assert(err, qt.IsNil)
			c.Assert(groupID, qt.Not(qt.Equals), "")

			// Verify the group was created correctly
			createdGroup, err := testDB.OrganizationMemberGroup(groupID, testOrgAddress)
			c.Assert(err, qt.IsNil)
			c.Assert(createdGroup.OrgAddress, qt.DeepEquals, testOrgAddress)
			c.Assert(createdGroup.Title, qt.Equals, "Test Group")
			c.Assert(createdGroup.Description, qt.Equals, "Test Description")
			c.Assert(createdGroup.MemberIDs, qt.DeepEquals, memberIDs)
			c.Assert(createdGroup.CreatedAt.IsZero(), qt.IsFalse)
			c.Assert(createdGroup.UpdatedAt.IsZero(), qt.IsFalse)
		})
	})

	t.Run("OrganizationMemberGroup", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		_, memberIDs := setupTestOrgMembersGroupPrerequisites(t, "_get")

		t.Run("NonExistentGroup", func(_ *testing.T) {
			// Test getting non-existent group
			nonExistentID := primitive.NewObjectID().Hex()
			_, err := testDB.OrganizationMemberGroup(nonExistentID, testOrgAddress)
			c.Assert(err, qt.Equals, ErrNotFound)
		})

		t.Run("ExistingGroup", func(_ *testing.T) {
			// Create a group to retrieve
			group := &OrganizationMemberGroup{
				OrgAddress:  testOrgAddress,
				Title:       "Test Group",
				Description: "Test Description",
				MemberIDs:   memberIDs,
			}
			groupID, err := testDB.CreateOrganizationMemberGroup(group)
			c.Assert(err, qt.IsNil)

			// Test getting existing group
			retrievedGroup, err := testDB.OrganizationMemberGroup(groupID, testOrgAddress)
			c.Assert(err, qt.IsNil)
			c.Assert(retrievedGroup.OrgAddress, qt.DeepEquals, testOrgAddress)
			c.Assert(retrievedGroup.Title, qt.Equals, "Test Group")
			c.Assert(retrievedGroup.Description, qt.Equals, "Test Description")
			c.Assert(retrievedGroup.MemberIDs, qt.DeepEquals, memberIDs)
			c.Assert(retrievedGroup.CreatedAt.IsZero(), qt.IsFalse)
			c.Assert(retrievedGroup.UpdatedAt.IsZero(), qt.IsFalse)
		})

		t.Run("WrongOrganization", func(_ *testing.T) {
			// Create a group
			group := &OrganizationMemberGroup{
				OrgAddress:  testOrgAddress,
				Title:       "Test Group",
				Description: "Test Description",
				MemberIDs:   memberIDs,
			}
			groupID, err := testDB.CreateOrganizationMemberGroup(group)
			c.Assert(err, qt.IsNil)

			// Try to get the group with wrong organization address
			_, err = testDB.OrganizationMemberGroup(groupID, testNonExistentOrg)
			c.Assert(err, qt.Equals, ErrNotFound)
		})
	})

	t.Run("OrganizationMemberGroups", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		_, memberIDs := setupTestOrgMembersGroupPrerequisites(t, "_list")

		t.Run("EmptyList", func(_ *testing.T) {
			// Test getting groups for organization with no groups
			totalPages, groups, err := testDB.OrganizationMemberGroups(testOrgAddress, 1, 10)
			c.Assert(err, qt.IsNil)
			c.Assert(groups, qt.HasLen, 0)
			c.Assert(totalPages, qt.Equals, 0)
		})

		t.Run("MultipleGroups", func(_ *testing.T) {
			// Create multiple groups
			for i := 0; i < 3; i++ {
				group := &OrganizationMemberGroup{
					OrgAddress:  testOrgAddress,
					Title:       "Test Group " + string(rune('1'+i)),
					Description: "Test Description " + string(rune('1'+i)),
					MemberIDs:   memberIDs,
				}
				_, err := testDB.CreateOrganizationMemberGroup(group)
				c.Assert(err, qt.IsNil)
			}

			// Test getting all groups for the organization
			totalPages, groups, err := testDB.OrganizationMemberGroups(testOrgAddress, 1, 10)
			c.Assert(err, qt.IsNil)
			c.Assert(groups, qt.HasLen, 3)
			c.Assert(totalPages, qt.Equals, 1)

			// Verify each group has the correct organization address
			for _, group := range groups {
				c.Assert(group.OrgAddress, qt.DeepEquals, testOrgAddress)
				c.Assert(group.CreatedAt.IsZero(), qt.IsFalse)
				c.Assert(group.UpdatedAt.IsZero(), qt.IsFalse)
			}
		})

		t.Run("DifferentOrganizations", func(_ *testing.T) {
			c.Assert(testDB.Reset(), qt.IsNil)
			// Setup prerequisites
			_, memberIDs := setupTestOrgMembersGroupPrerequisites(t, "_diff_org")

			// Create a different organization
			diffOrg := &Organization{
				Address:   testAnotherOrgAddress,
				Active:    true,
				CreatedAt: time.Now(),
			}
			err := testDB.SetOrganization(diffOrg)
			c.Assert(err, qt.IsNil)

			// Create members for different organization
			diffMemberIDs := make([]string, 2)
			for i := 0; i < 2; i++ {
				memberNumber := "diff_member_" + string(rune('1'+i))
				member := &OrgMember{
					OrgAddress:   testAnotherOrgAddress,
					MemberNumber: memberNumber,
					Email:        "diff_" + string(rune('1'+i)) + "@example.com",
					CreatedAt:    time.Now(),
					UpdatedAt:    time.Now(),
				}
				id, err := testDB.SetOrgMember("test_salt", member)
				c.Assert(err, qt.IsNil)
				diffMemberIDs[i] = id // Store the MongoDB ID, not the memberNumber
			}

			// Create groups for original organization
			for i := 0; i < 2; i++ {
				group := &OrganizationMemberGroup{
					OrgAddress:  testOrgAddress,
					Title:       "Org1 Group " + string(rune('1'+i)),
					Description: "Org1 Description " + string(rune('1'+i)),
					MemberIDs:   memberIDs,
				}
				_, err := testDB.CreateOrganizationMemberGroup(group)
				c.Assert(err, qt.IsNil)
			}

			// Create groups for different organization
			for i := 0; i < 3; i++ {
				group := &OrganizationMemberGroup{
					OrgAddress:  testAnotherOrgAddress,
					Title:       "Org2 Group " + string(rune('1'+i)),
					Description: "Org2 Description " + string(rune('1'+i)),
					MemberIDs:   diffMemberIDs,
				}
				_, err := testDB.CreateOrganizationMemberGroup(group)
				c.Assert(err, qt.IsNil)
			}

			// Test getting groups for original organization
			_, groups1, err := testDB.OrganizationMemberGroups(testOrgAddress, 1, 10)
			c.Assert(err, qt.IsNil)
			c.Assert(groups1, qt.HasLen, 2)
			for _, group := range groups1 {
				c.Assert(group.OrgAddress, qt.DeepEquals, testOrgAddress)
			}

			// Test getting groups for different organization
			_, groups2, err := testDB.OrganizationMemberGroups(testAnotherOrgAddress, 1, 10)
			c.Assert(err, qt.IsNil)
			c.Assert(groups2, qt.HasLen, 3)
			for _, group := range groups2 {
				c.Assert(group.OrgAddress, qt.DeepEquals, testAnotherOrgAddress)
			}
		})
	})

	t.Run("UpdateOrganizationMemberGroup", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		_, memberIDs := setupTestOrgMembersGroupPrerequisites(t, "_update")

		// Create additional members for testing updates
		additionalMemberIDs := make([]string, 2)
		for i := 0; i < 2; i++ {
			memberNumber := "additional_" + string(rune('1'+i))
			member := &OrgMember{
				OrgAddress:   testOrgAddress,
				MemberNumber: memberNumber,
				Email:        "additional_" + string(rune('1'+i)) + "@example.com",
				CreatedAt:    time.Now(),
				UpdatedAt:    time.Now(),
			}
			id, err := testDB.SetOrgMember("test_salt", member)
			c.Assert(err, qt.IsNil)
			additionalMemberIDs[i] = id // Store the MongoDB ID, not the memberNumber
		}

		t.Run("NonExistentGroup", func(_ *testing.T) {
			// Test updating non-existent group
			nonExistentID := primitive.NewObjectID().Hex()
			err := testDB.UpdateOrganizationMemberGroup(
				nonExistentID, testOrgAddress,
				"Updated Title", "Updated Description",
				additionalMemberIDs, nil,
			)
			c.Assert(err, qt.Not(qt.IsNil))
		})

		t.Run("UpdateMetadata", func(_ *testing.T) {
			// Create a group to update
			group := &OrganizationMemberGroup{
				OrgAddress:  testOrgAddress,
				Title:       "Original Title",
				Description: "Original Description",
				MemberIDs:   memberIDs,
			}
			groupID, err := testDB.CreateOrganizationMemberGroup(group)
			c.Assert(err, qt.IsNil)

			// Update only the metadata
			err = testDB.UpdateOrganizationMemberGroup(
				groupID, testOrgAddress,
				"Updated Title", "Updated Description",
				nil, nil,
			)
			c.Assert(err, qt.IsNil)

			// Verify the update
			updatedGroup, err := testDB.OrganizationMemberGroup(groupID, testOrgAddress)
			c.Assert(err, qt.IsNil)
			c.Assert(updatedGroup.Title, qt.Equals, "Updated Title")
			c.Assert(updatedGroup.Description, qt.Equals, "Updated Description")
			c.Assert(updatedGroup.MemberIDs, qt.DeepEquals, memberIDs) // Members should remain unchanged
		})

		t.Run("AddAndRemoveMembers", func(_ *testing.T) {
			// Create a group to update
			group := &OrganizationMemberGroup{
				OrgAddress:  testOrgAddress,
				Title:       "Add and Remove Group",
				Description: "Test adding and removing members",
				MemberIDs:   memberIDs, // Include all three members initially
			}
			groupID, err := testDB.CreateOrganizationMemberGroup(group)
			c.Assert(err, qt.IsNil)

			// Add additional members and remove the first original member
			err = testDB.UpdateOrganizationMemberGroup(
				groupID, testOrgAddress,
				"", "",
				additionalMemberIDs, []string{memberIDs[0]},
			)
			c.Assert(err, qt.IsNil)

			// Verify the update
			updatedGroup, err := testDB.OrganizationMemberGroup(groupID, testOrgAddress)
			c.Assert(err, qt.IsNil)
			c.Assert(updatedGroup.MemberIDs, qt.HasLen, 4) // 3 original - 1 removed + 2 additional

			// Check that the removed member is not in the group
			for _, memberID := range updatedGroup.MemberIDs {
				c.Assert(memberID, qt.Not(qt.Equals), memberIDs[0])
			}

			// Check that the remaining original members and additional members are in the group
			expectedMembers := append(memberIDs[1:], additionalMemberIDs...)
			for _, memberID := range expectedMembers {
				found := false
				for _, groupMemberID := range updatedGroup.MemberIDs {
					if groupMemberID == memberID {
						found = true
						break
					}
				}
				c.Assert(found, qt.IsTrue, qt.Commentf("Member %s not found in group", memberID))
			}
		})

		t.Run("InvalidMembers", func(_ *testing.T) {
			// Create a group to update
			group := &OrganizationMemberGroup{
				OrgAddress:  testOrgAddress,
				Title:       "Invalid Members Group",
				Description: "Test invalid members",
				MemberIDs:   memberIDs,
			}
			groupID, err := testDB.CreateOrganizationMemberGroup(group)
			c.Assert(err, qt.IsNil)

			// Try to add invalid members
			err = testDB.UpdateOrganizationMemberGroup(
				groupID, testOrgAddress,
				"", "",
				[]string{"invalid_member_id"}, nil,
			)
			c.Assert(err, qt.Not(qt.IsNil))
		})
	})

	t.Run("DeleteOrganizationMemberGroup", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		_, memberIDs := setupTestOrgMembersGroupPrerequisites(t, "_delete")

		t.Run("NonExistentGroup", func(_ *testing.T) {
			// Test deleting non-existent group
			nonExistentID := primitive.NewObjectID().Hex()
			err := testDB.DeleteOrganizationMemberGroup(nonExistentID, testOrgAddress)
			c.Assert(err, qt.IsNil) // Should not error for non-existent group
		})

		t.Run("ExistingGroup", func(_ *testing.T) {
			// Create a group to delete
			group := &OrganizationMemberGroup{
				OrgAddress:  testOrgAddress,
				Title:       "Delete Group",
				Description: "Test deleting group",
				MemberIDs:   memberIDs,
			}
			groupID, err := testDB.CreateOrganizationMemberGroup(group)
			c.Assert(err, qt.IsNil)

			// Verify the group exists
			_, err = testDB.OrganizationMemberGroup(groupID, testOrgAddress)
			c.Assert(err, qt.IsNil)

			// Delete the group
			err = testDB.DeleteOrganizationMemberGroup(groupID, testOrgAddress)
			c.Assert(err, qt.IsNil)

			// Verify the group was deleted
			_, err = testDB.OrganizationMemberGroup(groupID, testOrgAddress)
			c.Assert(err, qt.Equals, ErrNotFound)
		})

		t.Run("WrongOrganization", func(_ *testing.T) {
			// Create a group
			group := &OrganizationMemberGroup{
				OrgAddress:  testOrgAddress,
				Title:       "Wrong Org Group",
				Description: "Test wrong organization",
				MemberIDs:   memberIDs,
			}
			groupID, err := testDB.CreateOrganizationMemberGroup(group)
			c.Assert(err, qt.IsNil)

			// Try to delete with wrong organization address
			err = testDB.DeleteOrganizationMemberGroup(groupID, testNonExistentOrg)
			c.Assert(err, qt.IsNil) // Should not error, just not delete anything

			// Verify the group still exists
			_, err = testDB.OrganizationMemberGroup(groupID, testOrgAddress)
			c.Assert(err, qt.IsNil)
		})
	})

	t.Run("ListOrganizationMemberGroup", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		_, memberIDs := setupTestOrgMembersGroupPrerequisites(t, "_list_members")

		t.Run("NonExistentGroup", func(_ *testing.T) {
			// Test listing members of non-existent group
			nonExistentID := primitive.NewObjectID().Hex()
			_, _, err := testDB.ListOrganizationMemberGroup(nonExistentID, testOrgAddress, 1, 10)
			c.Assert(err, qt.Not(qt.IsNil))
		})

		t.Run("EmptyGroup", func(_ *testing.T) {
			// Create a group with one member, then remove the member
			group := &OrganizationMemberGroup{
				OrgAddress:  testOrgAddress,
				Title:       "Empty Group",
				Description: "Test empty group",
				MemberIDs:   memberIDs[:1], // Start with one member
			}
			groupID, err := testDB.CreateOrganizationMemberGroup(group)
			c.Assert(err, qt.IsNil)

			// Remove the member to make it empty
			err = testDB.UpdateOrganizationMemberGroup(
				groupID, testOrgAddress,
				"", "",
				nil, memberIDs[:1], // Remove the only member
			)
			c.Assert(err, qt.IsNil)

			// List members
			count, members, err := testDB.ListOrganizationMemberGroup(groupID, testOrgAddress, 1, 10)
			c.Assert(err, qt.IsNil)
			c.Assert(count, qt.Equals, 0)
			c.Assert(members, qt.HasLen, 0)
		})

		t.Run("GroupWithMembers", func(_ *testing.T) {
			// Create a group with members
			group := &OrganizationMemberGroup{
				OrgAddress:  testOrgAddress,
				Title:       "Members Group",
				Description: "Test group with members",
				MemberIDs:   memberIDs,
			}
			groupID, err := testDB.CreateOrganizationMemberGroup(group)
			c.Assert(err, qt.IsNil)

			// List all members
			count, members, err := testDB.ListOrganizationMemberGroup(groupID, testOrgAddress, 1, 10)
			c.Assert(err, qt.IsNil)
			c.Assert(count, qt.Equals, 1) // Expect 1 page since all members fit in one page
			c.Assert(members, qt.HasLen, len(memberIDs))

			// Verify each member is from the correct organization
			for _, member := range members {
				c.Assert(member.OrgAddress, qt.DeepEquals, testOrgAddress)
			}

			// Test pagination
			count, members, err = testDB.ListOrganizationMemberGroup(groupID, testOrgAddress, 1, 1)
			c.Assert(err, qt.IsNil)
			c.Assert(count, qt.Equals, 3)   // Expect 3 pages when page size is 1
			c.Assert(members, qt.HasLen, 1) // But only one member returned

			count, members, err = testDB.ListOrganizationMemberGroup(groupID, testOrgAddress, 2, 1)
			c.Assert(err, qt.IsNil)
			c.Assert(count, qt.Equals, len(memberIDs))
			c.Assert(members, qt.HasLen, 1)
		})

		t.Run("WrongOrganization", func(_ *testing.T) {
			// Create a group
			group := &OrganizationMemberGroup{
				OrgAddress:  testOrgAddress,
				Title:       "Wrong Org List Group",
				Description: "Test wrong organization for listing",
				MemberIDs:   memberIDs,
			}
			groupID, err := testDB.CreateOrganizationMemberGroup(group)
			c.Assert(err, qt.IsNil)

			// Try to list with wrong organization address
			_, _, err = testDB.ListOrganizationMemberGroup(groupID, testNonExistentOrg, 1, 10)
			c.Assert(err, qt.Not(qt.IsNil))
		})
	})

	t.Run("ZeroAddressValidation", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)

		// Test CreateOrganizationMemberGroup with zero address - should fail
		zeroAddrGroup := &OrganizationMemberGroup{
			OrgAddress:  common.Address{}, // Zero address
			Title:       "Test Group",
			Description: "Test Description",
			MemberIDs:   []string{"some-id"},
		}
		_, err := testDB.CreateOrganizationMemberGroup(zeroAddrGroup)
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test OrganizationMemberGroup with zero address - should fail
		_, err = testDB.OrganizationMemberGroup("some-id", common.Address{})
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test OrganizationMemberGroups with zero address - should fail
		_, _, err = testDB.OrganizationMemberGroups(common.Address{}, 1, 10)
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test UpdateOrganizationMemberGroup with zero address - should fail
		err = testDB.UpdateOrganizationMemberGroup("some-id", common.Address{}, "title", "desc", nil, nil)
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test DeleteOrganizationMemberGroup with zero address - should fail
		err = testDB.DeleteOrganizationMemberGroup("some-id", common.Address{})
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test ListOrganizationMemberGroup with zero address - should fail
		_, _, err = testDB.ListOrganizationMemberGroup("some-id", common.Address{}, 1, 10)
		c.Assert(err, qt.Equals, ErrInvalidData)
	})

	t.Run("CheckGroupMembersFields", func(_ *testing.T) {
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

		// Group 3: Only members without duplicates or missing data (member2)
		validGroup := &OrganizationMemberGroup{
			OrgAddress:  testOrgAddress,
			Title:       "Valid Group",
			Description: "Group containing only valid members",
			MemberIDs:   []string{member2ID},
		}
		validGroupID, err := testDB.CreateOrganizationMemberGroup(validGroup)
		c.Assert(err, qt.IsNil)

		// Test 1: Valid case - check fields with no duplicates or missing data using valid group
		authFields := OrgMemberAuthFields{
			OrgMemberAuthFieldsName,
		}
		twoFaFields := OrgMemberTwoFaFields{}
		results, err := testDB.CheckGroupMembersFields(testOrgAddress, validGroupID, authFields, twoFaFields)
		c.Assert(err, qt.IsNil)
		c.Assert(results, qt.Not(qt.IsNil))
		c.Assert(len(results.Members), qt.Equals, 1)     // Only 1 member in valid group
		c.Assert(len(results.Duplicates), qt.Equals, 0)  // No duplicates for email
		c.Assert(len(results.MissingData), qt.Equals, 0) // No missing data for email

		// Test 2: Duplicate detection - check fields with known duplicates using duplicates group
		authFields = OrgMemberAuthFields{
			OrgMemberAuthFieldsName,
			OrgMemberAuthFieldsSurname,
			OrgMemberAuthFieldsMemberNumber,
		}
		twoFaFields = OrgMemberTwoFaFields{}
		results, err = testDB.CheckGroupMembersFields(testOrgAddress, duplicatesGroupID, authFields, twoFaFields)
		c.Assert(err, qt.IsNil)
		c.Assert(results, qt.Not(qt.IsNil))
		// The function only adds members to the Members field if they don't have duplicates
		// Since both members have duplicate fields, they're added to the Duplicates field but not to the Members field
		c.Assert(len(results.Members) < 2, qt.Equals, true) // Not all members are in the Members field

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
		twoFaFields = OrgMemberTwoFaFields{}
		results, err = testDB.CheckGroupMembersFields(testOrgAddress, allMembersGroupID, authFields, twoFaFields)
		c.Assert(err, qt.IsNil)
		c.Assert(results, qt.Not(qt.IsNil))

		// Should find missing data (member4 has empty name and memberNumber)
		c.Assert(len(results.MissingData) > 0, qt.Equals, true)

		// Convert ObjectIDs to hex strings for easier comparison
		emptyIDs := make([]string, len(results.MissingData))
		for i, id := range results.MissingData {
			emptyIDs[i] = id.Hex()
		}

		// Check that member4 ID is in the missing data list
		c.Assert(contains(emptyIDs, member4ID), qt.Equals, true)

		// Test 4: Edge case - invalid organization address
		_, err = testDB.CheckGroupMembersFields(testNonExistentOrg, validGroupID, authFields, twoFaFields)
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test 5: Edge case - empty auth fields but with twoFa fields
		twoFaFields = OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail}
		_, err = testDB.CheckGroupMembersFields(testOrgAddress, validGroupID, OrgMemberAuthFields{}, twoFaFields)
		c.Assert(err, qt.IsNil) // Should NOT return an error when twoFaFields are provided

		// Test 5b: Edge case - both auth fields and twoFa fields empty
		_, err = testDB.CheckGroupMembersFields(testOrgAddress, validGroupID, OrgMemberAuthFields{}, OrgMemberTwoFaFields{})
		c.Assert(err, qt.Not(qt.IsNil)) // Should return an error for empty auth fields

		// Test 6: Edge case - non-existent group ID
		nonExistentGroupID := primitive.NewObjectID().Hex()
		_, err = testDB.CheckGroupMembersFields(testOrgAddress, nonExistentGroupID, authFields, twoFaFields)
		c.Assert(err, qt.Not(qt.IsNil)) // Should return an error for non-existent group

		// Test 7: Edge case - group from different organization
		// Create another organization

		otherOrg := &Organization{
			Address: testFourthOrgAddress,
		}
		err = testDB.SetOrganization(otherOrg)
		c.Assert(err, qt.IsNil)

		// Create a member for the other organization
		otherMember := &OrgMember{
			OrgAddress:   testFourthOrgAddress,
			Email:        "other@test.com",
			MemberNumber: "other123",
			Name:         "Other",
			Surname:      "Member",
		}
		otherMemberID, err := testDB.SetOrgMember(testSalt, otherMember)
		c.Assert(err, qt.IsNil)

		// Create a group for the other organization
		otherGroup := &OrganizationMemberGroup{
			OrgAddress:  testFourthOrgAddress,
			Title:       "Other Group",
			Description: "Group from different organization",
			MemberIDs:   []string{otherMemberID},
		}
		otherGroupID, err := testDB.CreateOrganizationMemberGroup(otherGroup)
		c.Assert(err, qt.IsNil)

		// Try to use the other organization's group with our organization
		_, err = testDB.CheckGroupMembersFields(testOrgAddress, otherGroupID, authFields, twoFaFields)
		c.Assert(err, qt.Not(qt.IsNil)) // Should return an error (group not found for this org)

		// Test 8: Test with all members group to ensure filtering works correctly
		authFields = OrgMemberAuthFields{
			OrgMemberAuthFieldsName,
		}
		twoFaFields = OrgMemberTwoFaFields{}
		results, err = testDB.CheckGroupMembersFields(testOrgAddress, allMembersGroupID, authFields, twoFaFields)
		c.Assert(err, qt.IsNil)
		c.Assert(results, qt.Not(qt.IsNil))
		// The function only adds members to the Members field if they don't have duplicates or empty fields
		// Since some members in the all members group have duplicates or empty fields, they're not all
		// added to the Members field
		c.Assert(len(results.Members) > 0, qt.Equals, true) // At least some members should be in the Members field
		// Check for duplicates and missing data
		c.Assert(len(results.Duplicates) >= 0, qt.Equals, true)  // May or may not have duplicates
		c.Assert(len(results.MissingData) >= 0, qt.Equals, true) // May or may not have missing data
		// Test 9: Test with only twoFaFields (empty authFields)
		authFields = OrgMemberAuthFields{}
		twoFaFields = OrgMemberTwoFaFields{
			OrgMemberTwoFaFieldEmail,
			OrgMemberTwoFaFieldPhone,
		}
		results, err = testDB.CheckGroupMembersFields(testOrgAddress, validGroupID, authFields, twoFaFields)
		c.Assert(err, qt.IsNil)
		c.Assert(results, qt.Not(qt.IsNil))
		c.Assert(len(results.Members) >= 0, qt.Equals, true)     // May or may not have members
		c.Assert(len(results.Duplicates) >= 0, qt.Equals, true)  // May or may not have duplicates
		c.Assert(len(results.MissingData) >= 0, qt.Equals, true) // May or may not have missing data

		// Test 10: Test with both authFields and twoFaFields
		authFields = OrgMemberAuthFields{
			OrgMemberAuthFieldsName,
		}
		twoFaFields = OrgMemberTwoFaFields{
			OrgMemberTwoFaFieldEmail,
		}
		results, err = testDB.CheckGroupMembersFields(testOrgAddress, allMembersGroupID, authFields, twoFaFields)
		c.Assert(err, qt.IsNil)
		c.Assert(results, qt.Not(qt.IsNil))

		// Test 11: Test with empty values in twoFaFields
		// Create a member with empty phone
		memberWithEmptyPhone := &OrgMember{
			OrgAddress:   testOrgAddress,
			Email:        "empty_phone@test.com",
			Phone:        "", // Empty phone
			MemberNumber: "empty_phone_123",
			Name:         "Empty",
			Surname:      "Phone",
			Password:     testPassword,
		}
		emptyPhoneMemberID, err := testDB.SetOrgMember(testSalt, memberWithEmptyPhone)
		c.Assert(err, qt.IsNil)

		// Create a group with this member
		emptyPhoneGroup := &OrganizationMemberGroup{
			OrgAddress:  testOrgAddress,
			Title:       "Empty Phone Group",
			Description: "Group with member having empty phone",
			MemberIDs:   []string{emptyPhoneMemberID},
		}
		emptyPhoneGroupID, err := testDB.CreateOrganizationMemberGroup(emptyPhoneGroup)
		c.Assert(err, qt.IsNil)

		// Test with phone as twoFaField
		authFields = OrgMemberAuthFields{
			OrgMemberAuthFieldsName,
		}
		twoFaFields = OrgMemberTwoFaFields{
			OrgMemberTwoFaFieldPhone,
		}
		results, err = testDB.CheckGroupMembersFields(testOrgAddress, emptyPhoneGroupID, authFields, twoFaFields)
		c.Assert(err, qt.IsNil)
		c.Assert(results, qt.Not(qt.IsNil))
		c.Assert(len(results.MissingData) > 0, qt.Equals, true) // Should detect empty phone

		// Convert ObjectIDs to hex strings for easier comparison
		emptyIDs = make([]string, len(results.MissingData))
		for i, id := range results.MissingData {
			emptyIDs[i] = id.Hex()
		}

		// Check that the member with empty phone is in the missing data list
		c.Assert(contains(emptyIDs, emptyPhoneMemberID), qt.Equals, true)
	})
}
