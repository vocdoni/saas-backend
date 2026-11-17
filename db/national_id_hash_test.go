package db

import (
	"context"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestNationalIDLoginHashIsCanonical: a typed national ID matches the stored one whatever its
// formatting, as long as both are a valid DNI or NIE; anything else still hashes as typed.
func TestNationalIDLoginHashIsCanonical(t *testing.T) {
	c := qt.New(t)
	auth := OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}
	hash := func(id string) []byte { return HashAuthTwoFaFields(OrgMember{NationalID: id}, auth, nil) }

	c.Assert(hash("1234567-l"), qt.DeepEquals, hash("01234567L"))
	c.Assert(hash(" x-01234567 l"), qt.DeepEquals, hash("X1234567L"))
	c.Assert(hash("12.345.678-z"), qt.DeepEquals, hash("12345678Z"))
	// a member stored without the letter matches the typed or certificate DNI/NIE with it
	c.Assert(hash("12345678"), qt.DeepEquals, hash("12345678Z"))
	c.Assert(hash("1234567"), qt.DeepEquals, hash("01234567L"))
	c.Assert(hash("Y1234567"), qt.DeepEquals, hash("Y1234567X"))
	// a short number is never taken for a DNI
	c.Assert(hash("123"), qt.Not(qt.DeepEquals), hash("00000123L"))
	// not a valid DNI/NIE: compared as before, case-insensitively only
	c.Assert(hash("DNI002"), qt.DeepEquals, hash("dni002"))
	c.Assert(hash("1234567-z"), qt.Not(qt.DeepEquals), hash("01234567Z"))
}

// TestRepairCanonicalizesNationalID: the repair tooling hashes the national ID exactly as the
// login path does, so a participant stored under a non-canonical spelling is rehashed to the hash a
// canonical login computes.
func TestRepairCanonicalizesNationalID(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)

	auth := OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}
	census := &Census{OrgAddress: testOrgAddress, AuthFields: auth, CreatedAt: time.Now()}
	censusID, err := testDB.SetCensus(census)
	c.Assert(err, qt.IsNil)
	member := &OrgMember{ID: bson.NewObjectID(), OrgAddress: testOrgAddress, NationalID: "1234567-l", CreatedAt: time.Now()}
	seedLegacyParticipant(t, censusID, member, unfoldedHash(*member, auth, nil))

	report := runRepair(t, migrations.RepairOptions{Apply: true})
	c.Assert(report.ParticipantsRehashed, qt.Equals, 1)

	var got CensusParticipant
	c.Assert(testDB.censusParticipants.FindOne(context.Background(),
		bson.M{"participantID": member.ID.Hex(), "censusId": censusID}).Decode(&got), qt.IsNil)
	c.Assert(got.LoginHash, qt.DeepEquals, HashAuthTwoFaFields(OrgMember{NationalID: "01234567L"}, auth, nil))
	// the stored value is not rewritten
	stored, err := testDB.OrgMember(testOrgAddress, member.ID.Hex())
	c.Assert(err, qt.IsNil)
	c.Assert(stored.NationalID, qt.Equals, "1234567-l")
}
