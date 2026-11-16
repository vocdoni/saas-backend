package db

import (
	"context"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/v2/bson"
)

var (
	certAuthFields = OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}
	certTwoFaCases = []OrgMemberTwoFaFields{
		{},
		{OrgMemberTwoFaFieldEmail},
		{OrgMemberTwoFaFieldEmail, OrgMemberTwoFaFieldPhone},
	}
)

// TestCertCensusLoginHashFields: a certificate census hashes the national ID alone, whatever its
// 2FA fields, and stores no email or phone variant.
func TestCertCensusLoginHashFields(t *testing.T) {
	c := qt.New(t)
	member := OrgMember{NationalID: "1234567-l", Email: "a@example.com", Phone: HashedPhone("hashed")}
	for _, twoFa := range certTwoFaCases {
		census := Census{AuthFields: certAuthFields, TwoFaFields: twoFa, CertAuth: true}
		auth, gotTwoFa := census.LoginHashFields()
		c.Assert(auth, qt.DeepEquals, certAuthFields)
		c.Assert(gotTwoFa, qt.IsNil)
		c.Assert(calculateParticipantHashes(census, member), qt.DeepEquals, map[string][]byte{
			"loginHash": HashAuthTwoFaFields(OrgMember{NationalID: "01234567L"}, certAuthFields, nil),
		})
	}
	// a census without CertAuth is unchanged
	plain := Census{AuthFields: certAuthFields, TwoFaFields: certTwoFaCases[2]}
	auth, twoFa := plain.LoginHashFields()
	c.Assert(auth, qt.DeepEquals, plain.AuthFields)
	c.Assert(twoFa, qt.DeepEquals, plain.TwoFaFields)
}

// TestRepairMatchesCanonicalHashCertCensus extends TestRepairMatchesCanonicalHash to certificate
// censuses: the repair tooling's computeParticipantHashes and calculateParticipantHashes agree, and
// a census built by the service needs no repair.
func TestRepairMatchesCanonicalHashCertCensus(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	org := &Organization{Address: testOrgAddress, CreatedAt: time.Now(), Country: "ES"}
	c.Assert(testDB.SetOrganization(org), qt.IsNil)
	phone, err := NewHashedPhone(testPlaintextPhone, org)
	c.Assert(err, qt.IsNil)

	type seeded struct {
		memberID bson.ObjectID
		censusID string
		census   Census
	}
	var all []seeded
	for i, twoFa := range certTwoFaCases {
		census := &Census{
			OrgAddress: testOrgAddress, AuthFields: certAuthFields, TwoFaFields: twoFa,
			CertAuth: true, CreatedAt: time.Now(),
		}
		censusID, err := testDB.SetCensus(census)
		c.Assert(err, qt.IsNil)
		member := &OrgMember{
			ID: bson.NewObjectID(), OrgAddress: testOrgAddress, NationalID: []string{"1234567-l", "x-1234567 l", "12.345.678-z"}[i],
			Email: "person@example.com", Phone: phone, CreatedAt: time.Now(),
		}
		// stored as the code before this change would have: the raw value, every 2FA field hashed too
		seedLegacyParticipant(t, censusID, member, unfoldedHash(*member, certAuthFields, twoFa))
		all = append(all, seeded{memberID: member.ID, censusID: censusID, census: *census})
	}

	report := runRepair(t, migrations.RepairOptions{Apply: true})
	c.Assert(report.ParticipantsRehashed, qt.Equals, len(certTwoFaCases))

	for _, s := range all {
		stored, err := testDB.OrgMember(testOrgAddress, s.memberID.Hex())
		c.Assert(err, qt.IsNil)
		var got CensusParticipant
		c.Assert(testDB.censusParticipants.FindOne(context.Background(),
			bson.M{"participantID": s.memberID.Hex(), "censusId": s.censusID}).Decode(&got), qt.IsNil)
		want := calculateParticipantHashes(s.census, *stored)
		c.Assert(got.LoginHash, qt.DeepEquals, want["loginHash"], qt.Commentf("twoFa=%v", s.census.TwoFaFields))
		c.Assert(got.LoginHashEmail, qt.HasLen, 0)
		c.Assert(got.LoginHashPhone, qt.HasLen, 0)
		found, err := testDB.CensusParticipantByLoginHash(s.census, *stored)
		c.Assert(err, qt.IsNil)
		c.Assert(found.ParticipantID, qt.Equals, s.memberID.Hex())
	}

	// a cert census with email+phone 2FA built by the service: a dry run finds nothing to rehash
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(org), qt.IsNil)
	census := &Census{
		OrgAddress: testOrgAddress, AuthFields: certAuthFields, TwoFaFields: certTwoFaCases[2],
		CertAuth: true, CreatedAt: time.Now(),
	}
	_, err = testDB.SetCensus(census)
	c.Assert(err, qt.IsNil)
	var ids []string
	for _, id := range []string{"12345678Z", "1234567-l"} {
		member := &OrgMember{OrgAddress: testOrgAddress, NationalID: id, Email: "p@example.com", PlaintextPhone: testPlaintextPhone}
		memberID, err := testDB.SetOrgMember("test_salt", member)
		c.Assert(err, qt.IsNil)
		ids = append(ids, memberID)
	}
	added, missing, err := testDB.PopulateMembersCensus(census, ids)
	c.Assert(err, qt.IsNil)
	c.Assert(added, qt.Equals, int64(2))
	c.Assert(missing, qt.HasLen, 0)
	dry := runRepair(t, migrations.RepairOptions{})
	c.Assert(dry.ParticipantsScanned, qt.Equals, 2)
	c.Assert(dry.ParticipantsRehashed, qt.Equals, 0)
}

// TestCertCensusMissingData: in a certificate census a member whose national ID is not a valid DNI
// or NIE is missing data, like a member without one; the 2FA channel is still required; and members
// whose national IDs differ only in formatting are duplicates.
func TestCertCensusMissingData(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)

	add := func(nationalID, email string) string {
		id, err := testDB.SetOrgMember("test_salt", &OrgMember{OrgAddress: testOrgAddress, NationalID: nationalID, Email: email})
		c.Assert(err, qt.IsNil)
		return id
	}
	valid := add("X1234567L", "valid@example.com")
	invalid := add("12345678A", "invalid@example.com")
	foreign := add("P1234567", "foreign@example.com")
	noEmail := add("Y0000123E", "")
	twinA := add("12345678Z", "a@example.com")
	twinB := add("12.345.678-z", "b@example.com")
	ids := []string{valid, invalid, foreign, noEmail, twinA, twinB}
	emailOnly := OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail}

	hexes := func(oids []bson.ObjectID) []string {
		out := make([]string, 0, len(oids))
		for _, id := range oids {
			out = append(out, id.Hex())
		}
		return out
	}

	res, err := testDB.CheckMembersFields(testOrgAddress, ids, certAuthFields, emailOnly, true)
	c.Assert(err, qt.IsNil)
	c.Assert(hexes(res.MissingData), qt.ContentEquals, []string{invalid, foreign, noEmail})
	c.Assert(hexes(res.Duplicates), qt.ContentEquals, []string{twinA, twinB})
	c.Assert(hexes(res.Members), qt.DeepEquals, []string{valid})

	// the same members in a census without CertAuth: any national ID counts, and the formatting
	// twins are duplicates there too now that the hash is canonical
	res, err = testDB.CheckMembersFields(testOrgAddress, ids, certAuthFields, emailOnly, false)
	c.Assert(err, qt.IsNil)
	c.Assert(hexes(res.MissingData), qt.DeepEquals, []string{noEmail})

	// the census build leaves the invalid ones out and reports them
	census := &Census{OrgAddress: testOrgAddress, AuthFields: certAuthFields, TwoFaFields: emailOnly, CertAuth: true}
	_, err = testDB.SetCensus(census)
	c.Assert(err, qt.IsNil)
	added, missing, err := testDB.PopulateMembersCensus(census, []string{valid, invalid, foreign, noEmail})
	c.Assert(err, qt.IsNil)
	c.Assert(added, qt.Equals, int64(1))
	c.Assert(hexes(missing), qt.ContentEquals, []string{invalid, foreign, noEmail})
}
