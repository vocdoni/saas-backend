package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const testMembershipMemberID = "member123"

func setupTestCensusMembershipPrerequisites(t *testing.T, memberSuffix string) (*Census, string) {
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
	memberID := testMembershipMemberID + memberSuffix
	member := &OrgMember{
		OrgAddress: testOrgAddress,
		MemberID:   memberID,
		Email:      "test" + memberSuffix + "@example.com",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
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
		memberID := testMembershipMemberID + "_set"
		membership := &CensusMembership{
			MemberID: memberID,
			CensusID: censusID,
		}

		// Test with invalid data
		t.Run("InvalidData", func(_ *testing.T) {
			invalidMembership := &CensusMembership{
				MemberID: "",
				CensusID: censusID,
			}
			err := testDB.SetCensusMembership(invalidMembership)
			c.Assert(err, qt.Equals, ErrInvalidData)

			invalidMembership = &CensusMembership{
				MemberID: testMembershipMemberID,
				CensusID: "",
			}
			err = testDB.SetCensusMembership(invalidMembership)
			c.Assert(err, qt.Equals, ErrInvalidData)
		})

		t.Run("NonExistentCensus", func(_ *testing.T) {
			nonExistentMembership := &CensusMembership{
				MemberID: testMembershipMemberID,
				CensusID: primitive.NewObjectID().Hex(),
			}
			err := testDB.SetCensusMembership(nonExistentMembership)
			c.Assert(err, qt.Not(qt.IsNil))
		})

		t.Run("NonExistentMember", func(_ *testing.T) {
			nonExistentMemberMembership := &CensusMembership{
				MemberID: "non-existent",
				CensusID: censusID,
			}
			err := testDB.SetCensusMembership(nonExistentMemberMembership)
			c.Assert(err, qt.Not(qt.IsNil))
		})

		t.Run("CreateAndUpdate", func(_ *testing.T) {
			// Create new membership
			err := testDB.SetCensusMembership(membership)
			c.Assert(err, qt.IsNil)

			// Verify the membership was created correctly
			createdMembership, err := testDB.CensusMembership(censusID, memberID)
			c.Assert(err, qt.IsNil)
			c.Assert(createdMembership.MemberID, qt.Equals, memberID)
			c.Assert(createdMembership.CensusID, qt.Equals, censusID)
			c.Assert(createdMembership.CreatedAt.IsZero(), qt.IsFalse)
			c.Assert(createdMembership.UpdatedAt.IsZero(), qt.IsFalse)

			// Test updating an existing membership
			time.Sleep(time.Millisecond) // Ensure different UpdatedAt timestamp
			err = testDB.SetCensusMembership(membership)
			c.Assert(err, qt.IsNil)

			// Verify the membership was updated correctly
			updatedMembership, err := testDB.CensusMembership(censusID, memberID)
			c.Assert(err, qt.IsNil)
			c.Assert(updatedMembership.MemberID, qt.Equals, memberID)
			c.Assert(updatedMembership.CensusID, qt.Equals, censusID)
			c.Assert(updatedMembership.CreatedAt, qt.Equals, createdMembership.CreatedAt)
			c.Assert(updatedMembership.UpdatedAt.After(createdMembership.UpdatedAt), qt.IsTrue)
		})
	})

	t.Run("GetCensusMembership", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		_, censusID := setupTestCensusMembershipPrerequisites(t, "_get")
		memberID := testMembershipMemberID + "_get"

		t.Run("InvalidData", func(_ *testing.T) {
			// Test getting membership with invalid data
			_, err := testDB.CensusMembership("", memberID)
			c.Assert(err, qt.Equals, ErrInvalidData)

			_, err = testDB.CensusMembership(censusID, "")
			c.Assert(err, qt.Equals, ErrInvalidData)
		})

		t.Run("NonExistentMembership", func(_ *testing.T) {
			// Test getting non-existent membership
			_, err := testDB.CensusMembership(censusID, memberID)
			c.Assert(err, qt.Equals, ErrNotFound)
		})

		t.Run("ExistingMembership", func(_ *testing.T) {
			// Create a membership to retrieve
			membership := &CensusMembership{
				MemberID: memberID,
				CensusID: censusID,
			}
			err := testDB.SetCensusMembership(membership)
			c.Assert(err, qt.IsNil)

			// Test getting existing membership
			retrievedMembership, err := testDB.CensusMembership(censusID, memberID)
			c.Assert(err, qt.IsNil)
			c.Assert(retrievedMembership.MemberID, qt.Equals, memberID)
			c.Assert(retrievedMembership.CensusID, qt.Equals, censusID)
			c.Assert(retrievedMembership.CreatedAt.IsZero(), qt.IsFalse)
			c.Assert(retrievedMembership.UpdatedAt.IsZero(), qt.IsFalse)
		})
	})

	t.Run("DeleteCensusMembership", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		_, censusID := setupTestCensusMembershipPrerequisites(t, "_delete")
		memberID := testMembershipMemberID + "_delete"

		t.Run("InvalidData", func(_ *testing.T) {
			// Test deleting with invalid data
			err := testDB.DelCensusMembership("", memberID)
			c.Assert(err, qt.Equals, ErrInvalidData)

			err = testDB.DelCensusMembership(censusID, "")
			c.Assert(err, qt.Equals, ErrInvalidData)
		})

		t.Run("ExistingMembership", func(_ *testing.T) {
			// Create a membership to delete
			membership := &CensusMembership{
				MemberID: memberID,
				CensusID: censusID,
			}
			err := testDB.SetCensusMembership(membership)
			c.Assert(err, qt.IsNil)

			// Test deleting existing membership
			err = testDB.DelCensusMembership(censusID, memberID)
			c.Assert(err, qt.IsNil)

			// Verify the membership was deleted
			_, err = testDB.CensusMembership(censusID, memberID)
			c.Assert(err, qt.Equals, ErrNotFound)
		})

		t.Run("NonExistentMembership", func(_ *testing.T) {
			// Test deleting non-existent membership (should not error)
			err := testDB.DelCensusMembership(censusID, memberID)
			c.Assert(err, qt.IsNil)
		})
	})

	t.Run("BulkCensusMembership", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		_, censusID := setupTestCensusMembershipPrerequisites(t, "_bulk")

		t.Run("EmptyMembers", func(_ *testing.T) {
			// Test with empty members
			progressChan, err := testDB.SetBulkCensusMembership("test_salt", censusID, nil)
			c.Assert(err, qt.IsNil)

			// Channel should be closed immediately for empty members
			_, open := <-progressChan
			c.Assert(open, qt.IsFalse)
		})

		t.Run("InvalidData", func(_ *testing.T) {
			// Test with empty census ID
			members := []OrgMember{
				{
					MemberID: "test1",
					Email:    "test1@example.com",
					Phone:    "1234567890",
					Password: "password1",
				},
			}
			progressChan, err := testDB.SetBulkCensusMembership("test_salt", "", members)
			c.Assert(err, qt.Equals, ErrInvalidData)

			// Channel should be closed immediately for invalid data
			_, open := <-progressChan
			c.Assert(open, qt.IsFalse)
		})

		t.Run("NonExistentCensus", func(_ *testing.T) {
			members := []OrgMember{
				{
					MemberID: "test1",
					Email:    "test1@example.com",
					Phone:    "1234567890",
					Password: "password1",
				},
			}
			// Test with non-existent census
			progressChan, err := testDB.SetBulkCensusMembership("test_salt", primitive.NewObjectID().Hex(), members)
			c.Assert(err, qt.Not(qt.IsNil))

			// Channel should be closed immediately for non-existent census
			_, open := <-progressChan
			c.Assert(open, qt.IsFalse)
		})

		t.Run("SuccessfulBulkCreation", func(_ *testing.T) {
			// Test successful bulk creation
			members := []OrgMember{
				{
					MemberID: "test1",
					Email:    "test1@example.com",
					Phone:    "1234567890",
					Password: "password1",
				},
				{
					MemberID: "test2",
					Email:    "test2@example.com",
					Phone:    "0987654321",
					Password: "password2",
				},
			}

			progressChan, err := testDB.SetBulkCensusMembership("test_salt", censusID, members)
			c.Assert(err, qt.IsNil)
			c.Assert(progressChan, qt.Not(qt.IsNil))

			// Wait for the operation to complete by draining the channel
			var lastProgress *BulkCensusMembershipStatus
			for progress := range progressChan {
				lastProgress = progress
			}
			// Final progress should be 100%
			c.Assert(lastProgress.Progress, qt.Equals, 100)

			// Verify members were created with hashed data
			for _, p := range members {
				member, err := testDB.OrgMemberByID(testOrgAddress, p.MemberID)
				c.Assert(err, qt.IsNil)
				c.Assert(member.Email, qt.Not(qt.Equals), "")
				c.Assert(member.Phone, qt.Equals, "")
				c.Assert(member.HashedPhone, qt.Not(qt.Equals), "")
				c.Assert(member.Password, qt.Equals, "")
				c.Assert(member.HashedPass, qt.Not(qt.Equals), "")
				c.Assert(member.CreatedAt.IsZero(), qt.IsFalse)

				// Verify memberships were created
				membership, err := testDB.CensusMembership(censusID, p.MemberID)
				c.Assert(err, qt.IsNil)
				c.Assert(membership.MemberID, qt.Equals, p.MemberID)
				c.Assert(membership.CensusID, qt.Equals, censusID)
				c.Assert(membership.CreatedAt.IsZero(), qt.IsFalse)
			}
		})

		t.Run("UpdateExistingMembers", func(_ *testing.T) {
			// Create members first
			members := []OrgMember{
				{
					MemberID: "update1",
					Email:    "update1@example.com",
					Phone:    "1234567890",
					Password: "password1",
				},
				{
					MemberID: "update2",
					Email:    "update2@example.com",
					Phone:    "0987654321",
					Password: "password2",
				},
			}

			// Create initial members
			progressChan, err := testDB.SetBulkCensusMembership("test_salt", censusID, members)
			c.Assert(err, qt.IsNil)
			c.Assert(progressChan, qt.Not(qt.IsNil))

			// Wait for the operation to complete
			//revive:disable:empty-block
			for range progressChan {
				// Just drain the channel
			}
			//revive:enable:empty-block

			// Test updating existing members and memberships
			members[0].Email = "updated1@example.com"
			members[1].Phone = "1111111111"

			progressChan, err = testDB.SetBulkCensusMembership("test_salt", censusID, members)
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
			for i, p := range members {
				member, err := testDB.OrgMemberByID(testOrgAddress, p.MemberID)
				c.Assert(err, qt.IsNil)
				c.Assert(member.Email, qt.Equals, members[i].Email)
				c.Assert(member.Phone, qt.Equals, "")
				c.Assert(member.HashedPhone, qt.Not(qt.Equals), "")
			}
		})
	})
}
