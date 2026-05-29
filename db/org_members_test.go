package db

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestOrgMembers(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	t.Run("SetOrgMember", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		// Create org
		organization := &Organization{
			Address: testOrgAddress,
		}
		err := testDB.SetOrganization(organization)
		c.Assert(err, qt.IsNil)

		// Test creating a new member
		member := &OrgMember{
			OrgAddress:     testOrgAddress,
			Email:          testMemberEmail,
			PlaintextPhone: testPlaintextPhone,
			MemberNumber:   testMemberNumber,
			Name:           testName,
			Password:       testPassword,
		}

		// Create new member
		memberOID, err := testDB.SetOrgMember(testSalt, member)
		c.Assert(err, qt.IsNil)
		c.Assert(memberOID, qt.Not(qt.Equals), "")

		// Verify the member was created correctly
		createdMember, err := testDB.OrgMember(testOrgAddress, memberOID)
		c.Assert(err, qt.IsNil)
		c.Assert(createdMember.Email, qt.Equals, testMemberEmail)
		c.Assert(createdMember.Phone.Bytes(), qt.DeepEquals, internal.HashOrgData(testOrgAddress, testPlaintextPhone))
		c.Assert(createdMember.MemberNumber, qt.Equals, member.MemberNumber)
		c.Assert(createdMember.Name, qt.Equals, testName)
		c.Assert(createdMember.HashedPass, qt.DeepEquals, internal.HashPassword(testSalt, testPassword))
		c.Assert(createdMember.CreatedAt, qt.Not(qt.IsNil))

		// Test updating an existing member
		newName := "Updated Name"
		newPhone := "+34655432100"
		createdMember.Name = newName
		createdMember.PlaintextPhone = newPhone

		// Update member
		updatedID, err := testDB.SetOrgMember(testSalt, createdMember)
		c.Assert(err, qt.IsNil)
		c.Assert(updatedID, qt.Equals, memberOID)

		// Verify the member was updated correctly
		updatedMember, err := testDB.OrgMember(testOrgAddress, updatedID)
		c.Assert(err, qt.IsNil)
		c.Assert(updatedMember.Name, qt.Equals, newName)
		c.Assert(updatedMember.Phone.Bytes(), qt.DeepEquals, internal.HashOrgData(testOrgAddress, newPhone))
		c.Assert(updatedMember.CreatedAt, qt.Equals, createdMember.CreatedAt)

		duplicateMember := &OrgMember{
			ID:             updatedMember.ID, // Use the same ID to simulate a duplicate
			OrgAddress:     testOrgAddress,
			Email:          testMemberEmail,
			PlaintextPhone: testPlaintextPhone,
			MemberNumber:   testMemberNumber,
			Name:           testName,
			Password:       testPassword,
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
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
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
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
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

	t.Run("BirthDateParsing", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		organization := &Organization{Address: testOrgAddress}
		c.Assert(testDB.SetOrganization(organization), qt.IsNil)

		type birthTest struct {
			in          string
			want        string
			expectError qt.Checker
		}

		for _, tc := range []birthTest{
			{in: "03/02/2001", want: "2001-02-03", expectError: qt.IsNil},
			{in: "03-02-2001", want: "2001-02-03", expectError: qt.IsNil},
			{in: "2001 02 03", want: "2001-02-03", expectError: qt.IsNil},
			{in: "12/31/2025", expectError: qt.IsNotNil},
		} {
			member := &OrgMember{
				OrgAddress: testOrgAddress,
				Email: fmt.Sprintf(
					"member-%s@test.com",
					strings.ReplaceAll(strings.ReplaceAll(tc.in, " ", "-"), "/", "-"),
				),
				Name:      "Test",
				BirthDate: tc.in,
			}

			id, err := testDB.SetOrgMember(testSalt, member)
			c.Assert(err, tc.expectError)
			if tc.expectError != qt.IsNil {
				continue
			}

			dbMember, err := testDB.OrgMember(testOrgAddress, id)
			c.Assert(err, qt.IsNil)
			c.Assert(dbMember.BirthDate, qt.Equals, tc.want)
			c.Assert(dbMember.ParsedBirthDate.IsZero(), qt.IsFalse)
		}
	})

	t.Run("SetBulkOrgMembers", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		// Create org
		organization := &Organization{
			Address: testOrgAddress,
		}
		err := testDB.SetOrganization(organization)
		c.Assert(err, qt.IsNil)

		// Test bulk insert of new members
		members := []*OrgMember{
			{
				OrgAddress:     testOrgAddress,
				Email:          testMemberEmail,
				PlaintextPhone: testPlaintextPhone,
				MemberNumber:   testMemberNumber,
				Name:           testName,
				Password:       testPassword,
			},
			{
				OrgAddress:     testOrgAddress,
				Email:          "member2@test.com",
				PlaintextPhone: "+34678678978",
				MemberNumber:   "member456",
				Name:           "Test Member 2",
				Password:       "testpass456",
			},
		}

		// Perform bulk upsert
		progressChan, err := testDB.AddBulkOrgMembers(testOrg, members, testSalt)
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
		c.Assert(member1.Phone.Bytes(), qt.DeepEquals, internal.HashOrgData(testOrgAddress, testPlaintextPhone))
		c.Assert(member1.HashedPass, qt.DeepEquals, internal.HashPassword(testSalt, testPassword))

		member2, err := testDB.OrgMemberByMemberNumber(testOrgAddress, members[1].MemberNumber)
		c.Assert(err, qt.IsNil)
		c.Assert(member2.Phone.Bytes(), qt.DeepEquals, internal.HashOrgData(testOrgAddress, members[1].PlaintextPhone))
		c.Assert(member2.HashedPass, qt.DeepEquals, internal.HashPassword(testSalt, members[1].Password))

		// Verify that the org members count is correct
		count, err := testDB.CountOrgMembers(testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(count, qt.Equals, int64(2))

		// Test with empty organization address
		testOrgWithEmptyAddress := &Organization{
			Address: common.Address{},
			Country: "ES",
		}
		_, err = testDB.AddBulkOrgMembers(testOrgWithEmptyAddress, members, testSalt)
		c.Assert(err, qt.Not(qt.IsNil))
	})

	t.Run("ZeroAddressValidation", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		// Test SetOrgMember with zero address - should fail
		zeroAddrMember := &OrgMember{
			OrgAddress:   common.Address{}, // Zero address
			Email:        testMemberEmail,
			MemberNumber: testMemberNumber,
			Name:         testName,
		}
		_, err := testDB.SetOrgMember(testSalt, zeroAddrMember)
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test OrgMember with zero address - should fail
		_, err = testDB.OrgMember(common.Address{}, "some-id")
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test OrgMembers with zero address - should fail
		_, _, err = testDB.OrgMembers(common.Address{}, 0, 10, "")
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test DeleteOrgMembers with zero address - should fail
		_, err = testDB.DeleteOrgMembers(common.Address{}, []string{"some-id"})
		c.Assert(err, qt.Equals, ErrInvalidData)
	})
}

type orgMembersByFieldFixture struct {
	org   *Organization
	alice *OrgMember
	bob   *OrgMember
	carol *OrgMember
	other *OrgMember
}

func setupOrgMembersByFieldFixtureForTrackedTests(t *testing.T) *orgMembersByFieldFixture {
	t.Helper()
	c := qt.New(t)

	org := &Organization{
		Address: testOrgAddress,
		Country: "ES",
	}
	otherOrg := &Organization{
		Address: testAnotherOrgAddress,
		Country: "ES",
	}
	c.Assert(testDB.SetOrganization(org), qt.IsNil)
	c.Assert(testDB.SetOrganization(otherOrg), qt.IsNil)

	mkMember := func(orgAddress common.Address, memberNumber, email, phone, nationalID string) *OrgMember {
		member := &OrgMember{
			OrgAddress:     orgAddress,
			MemberNumber:   memberNumber,
			Email:          email,
			PlaintextPhone: phone,
			NationalID:     nationalID,
			Name:           memberNumber,
		}
		memberID, err := testDB.SetOrgMember(testSalt, member)
		c.Assert(err, qt.IsNil)

		stored, err := testDB.OrgMember(orgAddress, memberID)
		c.Assert(err, qt.IsNil)
		return stored
	}

	alice := mkMember(testOrgAddress, "P001", "shared@x.com", "+34611111111", "DNI001")
	bob := mkMember(testOrgAddress, "P002", "bob@x.com", "+34622222222", "DNI002")
	carol := mkMember(testOrgAddress, "P003", "shared@x.com", "+34633333333", "DNI003")
	other := mkMember(testAnotherOrgAddress, "P004", "shared@x.com", "+34611111111", "DNI004")

	return &orgMembersByFieldFixture{
		org:   org,
		alice: alice,
		bob:   bob,
		carol: carol,
		other: other,
	}
}

func TestOrgMembersByFieldTrackedSuite(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	t.Run("validation rejects invalid field and empty values", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		_ = setupOrgMembersByFieldFixtureForTrackedTests(t)

		_, err := testDB.OrgMembersByField(testOrgAddress, OrgMemberLookupField("invalid-field"), "Alice")
		c.Assert(err, qt.Equals, ErrInvalidData)

		_, err = testDB.OrgMembersByField(testOrgAddress, OrgMemberLookupFieldEmail, "")
		c.Assert(err, qt.Equals, ErrInvalidData)
	})

	t.Run("lookup by email returns all matches within the organization", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		fixture := setupOrgMembersByFieldFixtureForTrackedTests(t)

		got, err := testDB.OrgMembersByField(testOrgAddress, OrgMemberLookupFieldEmail, "shared@x.com")
		c.Assert(err, qt.IsNil)
		c.Assert(got, qt.HasLen, 2)

		gotIDs := map[string]bool{}
		for _, member := range got {
			gotIDs[member.ID.Hex()] = true
		}
		c.Assert(gotIDs[fixture.alice.ID.Hex()], qt.IsTrue)
		c.Assert(gotIDs[fixture.carol.ID.Hex()], qt.IsTrue)
	})

	t.Run("lookup by member number and national ID returns the unique match", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		fixture := setupOrgMembersByFieldFixtureForTrackedTests(t)

		gotByNumber, err := testDB.OrgMembersByField(testOrgAddress, OrgMemberLookupFieldMemberNumber, "P002")
		c.Assert(err, qt.IsNil)
		c.Assert(gotByNumber, qt.HasLen, 1)
		c.Assert(gotByNumber[0].ID.Hex(), qt.Equals, fixture.bob.ID.Hex())

		gotByNationalID, err := testDB.OrgMembersByField(testOrgAddress, OrgMemberLookupFieldNationalID, "DNI001")
		c.Assert(err, qt.IsNil)
		c.Assert(gotByNationalID, qt.HasLen, 1)
		c.Assert(gotByNationalID[0].ID.Hex(), qt.Equals, fixture.alice.ID.Hex())
	})

	t.Run("lookup by phone uses the hashed value and remains org-scoped", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		fixture := setupOrgMembersByFieldFixtureForTrackedTests(t)

		hashedPhone, err := NewHashedPhone("+34611111111", fixture.org)
		c.Assert(err, qt.IsNil)

		got, err := testDB.OrgMembersByField(testOrgAddress, OrgMemberLookupFieldPhone, hashedPhone)
		c.Assert(err, qt.IsNil)
		c.Assert(got, qt.HasLen, 1)
		c.Assert(got[0].ID.Hex(), qt.Equals, fixture.alice.ID.Hex())
		c.Assert(got[0].ID.Hex(), qt.Not(qt.Equals), fixture.other.ID.Hex())
	})

	t.Run("no matches return an empty slice without error", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		_ = setupOrgMembersByFieldFixtureForTrackedTests(t)

		got, err := testDB.OrgMembersByField(testOrgAddress, OrgMemberLookupFieldEmail, "missing@x.com")
		c.Assert(err, qt.IsNil)
		c.Assert(got, qt.HasLen, 0)
	})
}
