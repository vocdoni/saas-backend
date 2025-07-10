package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
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
				OrgAddress:  internal.HexBytes{0x01, 0x23, 0x45},
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
}
