package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestDeleteOrgMembersSweepsParticipantsWrittenBack covers the race where a census build that
// read a member before its deletion writes the member's participant after the revocation: the
// deletion must sweep that participant instead of leaving a row for a member that is gone.
func TestDeleteOrgMembersSweepsParticipantsWrittenBack(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(testOrg), qt.IsNil)

	census := &Census{
		OrgAddress:  testOrgAddress,
		Type:        CensusTypeMail,
		AuthFields:  OrgMemberAuthFields{OrgMemberAuthFieldsMemberNumber, OrgMemberAuthFieldsName},
		TwoFaFields: OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail},
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	censusID, err := testDB.SetCensus(census)
	c.Assert(err, qt.IsNil)

	newMember := func(c *qt.C, n string) *OrgMember {
		m := &OrgMember{
			ID:           bson.NewObjectID(),
			OrgAddress:   testOrgAddress,
			MemberNumber: "sweep-" + n,
			Name:         "Sweep " + n,
			Email:        "sweep" + n + "@example.com",
			CreatedAt:    time.Now(),
		}
		_, err := testDB.SetOrgMember(testSalt, m)
		c.Assert(err, qt.IsNil)
		stored, err := testDB.OrgMember(testOrgAddress, m.ID.Hex())
		c.Assert(err, qt.IsNil)
		return stored
	}
	writeParticipant := func(c *qt.C, m *OrgMember) {
		_, err := testDB.setBulkCensusParticipant(census,
			[]censusMember{{id: m.ID, hashes: calculateParticipantHashes(*census, *m)}})
		c.Assert(err, qt.IsNil)
	}

	for _, tc := range []struct {
		name   string
		delete func(m *OrgMember) error
	}{
		{"selected", func(m *OrgMember) error {
			_, _, err := testDB.DeleteOrgMembers(testOrgAddress, []string{m.ID.Hex()})
			return err
		}},
		{"all", func(*OrgMember) error {
			_, _, err := testDB.DeleteAllOrgMembers(testOrgAddress)
			return err
		}},
	} {
		c.Run(tc.name, func(c *qt.C) {
			m := newMember(c, tc.name)
			writeParticipant(c, m)

			// a census build that read the member before the deletion writes it back afterwards
			afterMemberDeleteHook = func([]string) { writeParticipant(c, m) }
			c.Cleanup(func() { afterMemberDeleteHook = nil })

			c.Assert(tc.delete(m), qt.IsNil)

			_, err := testDB.OrgMember(testOrgAddress, m.ID.Hex())
			c.Assert(err, qt.Not(qt.IsNil))
			participants, err := testDB.CensusParticipants(censusID)
			c.Assert(err, qt.IsNil)
			c.Assert(participants, qt.HasLen, 0)
		})
	}
}
