package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// setupCheckBundleFixture seeds an organization, three org members, a census
// with two of them as participants, and a process bundle pointing at that
// census. It is the fixture for all bundle-participants-check helper tests.
//
// Members:
//
//	Alice (P001, email "shared@x.com",   phone +34611111111, nationalId DNI001) — in census
//	Bob   (P002, email "bob@x.com",      phone +34622222222, nationalId DNI002) — in census
//	Carol (P003, email "shared@x.com",   phone +34633333333, nationalId DNI003) — NOT in census
func setupCheckBundleFixture(t *testing.T) *checkBundleFixture {
	t.Helper()
	c := qt.New(t)

	org := &Organization{
		Address:   testOrgAddress,
		Country:   "ES",
		CreatedAt: time.Now(),
	}
	c.Assert(testDB.SetOrganization(org), qt.IsNil)

	mkMember := func(num, name, surname, email, phone, nationalID string) *OrgMember {
		m := &OrgMember{
			OrgAddress:     testOrgAddress,
			MemberNumber:   num,
			Name:           name,
			Surname:        surname,
			Email:          email,
			PlaintextPhone: phone,
			NationalID:     nationalID,
		}
		id, err := testDB.SetOrgMember(testSalt, m)
		c.Assert(err, qt.IsNil)
		stored, err := testDB.OrgMember(testOrgAddress, id)
		c.Assert(err, qt.IsNil)
		return stored
	}

	alice := mkMember("P001", "Alice", "Alpha", "shared@x.com", "+34611111111", "DNI001")
	bob := mkMember("P002", "Bob", "Beta", "bob@x.com", "+34622222222", "DNI002")
	carol := mkMember("P003", "Carol", "Gamma", "shared@x.com", "+34633333333", "DNI003")

	census := &Census{
		OrgAddress: testOrgAddress,
		AuthFields: OrgMemberAuthFields{},
		CreatedAt:  time.Now(),
	}
	censusIDStr, err := testDB.SetCensus(census)
	c.Assert(err, qt.IsNil)
	oid, err := bson.ObjectIDFromHex(censusIDStr)
	c.Assert(err, qt.IsNil)
	census.ID = oid

	// Alice and Bob become participants; Carol stays out.
	for _, m := range []*OrgMember{alice, bob} {
		c.Assert(testDB.SetCensusParticipant(&CensusParticipant{
			ParticipantID: m.ID.Hex(),
			CensusID:      censusIDStr,
			CreatedAt:     time.Now(),
		}), qt.IsNil)
	}

	bundleID := internal.HexBytesFromString(bson.NewObjectID().Hex())

	return &checkBundleFixture{
		org:      org,
		alice:    alice,
		bob:      bob,
		carol:    carol,
		census:   census,
		bundleID: bundleID,
	}
}

type checkBundleFixture struct {
	org      *Organization
	alice    *OrgMember
	bob      *OrgMember
	carol    *OrgMember
	census   *Census
	bundleID internal.HexBytes
}

func TestOrgMembersByField(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	t.Run("invalid field returns ErrInvalidData", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		_ = setupCheckBundleFixture(t)

		_, err := testDB.OrgMembersByField(testOrgAddress, OrgMemberLookupField("invalid-field"), "Alice")
		c.Assert(err, qt.Equals, ErrInvalidData)
	})

	t.Run("empty value returns ErrInvalidData", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		_ = setupCheckBundleFixture(t)

		_, err := testDB.OrgMembersByField(testOrgAddress, OrgMemberLookupFieldEmail, "")
		c.Assert(err, qt.Equals, ErrInvalidData)
	})

	t.Run("lookup by email returns every matching member", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		fixture := setupCheckBundleFixture(t)

		got, err := testDB.OrgMembersByField(testOrgAddress, OrgMemberLookupFieldEmail, "shared@x.com")
		c.Assert(err, qt.IsNil)
		c.Assert(got, qt.HasLen, 2)

		ids := map[string]bool{}
		for _, m := range got {
			ids[m.ID.Hex()] = true
		}
		c.Assert(ids[fixture.alice.ID.Hex()], qt.IsTrue)
		c.Assert(ids[fixture.carol.ID.Hex()], qt.IsTrue)
	})

	t.Run("lookup by memberNumber returns the unique member", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		fixture := setupCheckBundleFixture(t)

		got, err := testDB.OrgMembersByField(testOrgAddress, OrgMemberLookupFieldMemberNumber, "P002")
		c.Assert(err, qt.IsNil)
		c.Assert(got, qt.HasLen, 1)
		c.Assert(got[0].ID.Hex(), qt.Equals, fixture.bob.ID.Hex())
	})

	t.Run("lookup by nationalId returns the unique member", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		fixture := setupCheckBundleFixture(t)

		got, err := testDB.OrgMembersByField(testOrgAddress, OrgMemberLookupFieldNationalID, "DNI001")
		c.Assert(err, qt.IsNil)
		c.Assert(got, qt.HasLen, 1)
		c.Assert(got[0].ID.Hex(), qt.Equals, fixture.alice.ID.Hex())
	})

	t.Run("lookup by phone matches against the hashed value", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		fixture := setupCheckBundleFixture(t)

		hashed, err := NewHashedPhone("+34611111111", fixture.org)
		c.Assert(err, qt.IsNil)

		got, err := testDB.OrgMembersByField(testOrgAddress, OrgMemberLookupFieldPhone, hashed)
		c.Assert(err, qt.IsNil)
		c.Assert(got, qt.HasLen, 1)
		c.Assert(got[0].ID.Hex(), qt.Equals, fixture.alice.ID.Hex())
	})

	t.Run("no match returns empty slice without error", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		_ = setupCheckBundleFixture(t)

		got, err := testDB.OrgMembersByField(testOrgAddress, OrgMemberLookupFieldEmail, "missing@x.com")
		c.Assert(err, qt.IsNil)
		c.Assert(got, qt.HasLen, 0)
	})

	t.Run("results are scoped to the requested org", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		_ = setupCheckBundleFixture(t)

		got, err := testDB.OrgMembersByField(testAnotherOrgAddress, OrgMemberLookupFieldEmail, "shared@x.com")
		c.Assert(err, qt.IsNil)
		c.Assert(got, qt.HasLen, 0)
	})
}

func TestCensusParticipantsByMemberIDs(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	t.Run("empty censusID returns ErrInvalidData", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		fixture := setupCheckBundleFixture(t)

		_, err := testDB.CensusParticipantsByMemberIDs("", []string{fixture.alice.ID.Hex()})
		c.Assert(err, qt.Equals, ErrInvalidData)
	})

	t.Run("empty memberIDs returns empty slice without error", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		fixture := setupCheckBundleFixture(t)

		got, err := testDB.CensusParticipantsByMemberIDs(fixture.census.ID.Hex(), nil)
		c.Assert(err, qt.IsNil)
		c.Assert(got, qt.HasLen, 0)
	})

	t.Run("returns only IDs that are participants of the census", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		fixture := setupCheckBundleFixture(t)

		got, err := testDB.CensusParticipantsByMemberIDs(
			fixture.census.ID.Hex(),
			[]string{fixture.alice.ID.Hex(), fixture.bob.ID.Hex(), fixture.carol.ID.Hex()},
		)
		c.Assert(err, qt.IsNil)
		c.Assert(got, qt.HasLen, 2)

		gotIDs := map[string]bool{}
		for _, p := range got {
			gotIDs[p.ParticipantID] = true
		}
		c.Assert(gotIDs[fixture.alice.ID.Hex()], qt.IsTrue)
		c.Assert(gotIDs[fixture.bob.ID.Hex()], qt.IsTrue)
		c.Assert(gotIDs[fixture.carol.ID.Hex()], qt.IsFalse)
	})

	t.Run("results are scoped to the requested census", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		fixture := setupCheckBundleFixture(t)

		// Different census ID; same memberID must not be returned.
		other := &Census{OrgAddress: testOrgAddress, CreatedAt: time.Now()}
		otherID, err := testDB.SetCensus(other)
		c.Assert(err, qt.IsNil)

		got, err := testDB.CensusParticipantsByMemberIDs(otherID, []string{fixture.alice.ID.Hex()})
		c.Assert(err, qt.IsNil)
		c.Assert(got, qt.HasLen, 0)
	})
}

// seedUsedCSPProcess seeds a verified CSP auth token for the given member/bundle
// and consumes the given process with it, so that CSPProcessByUserAndProcess
// reports a used process for (memberID, processID).
func seedUsedCSPProcess(t *testing.T, memberID string, bundleID, processID internal.HexBytes) {
	t.Helper()
	c := qt.New(t)
	token := internal.HexBytes(append([]byte{0xC5, 0x90}, processID...))
	userID := internal.HexBytesFromString(memberID)
	address := internal.HexBytes{0x11, 0x22, 0x33}
	c.Assert(testDB.SetCSPAuth(token, userID, bundleID, ""), qt.IsNil)
	c.Assert(testDB.VerifyCSPAuth(token), qt.IsNil)
	c.Assert(testDB.ConsumeCSPProcess(token, processID, address), qt.IsNil)
}

func TestCSPProcessesByMembers(t *testing.T) {
	c := qt.New(t)
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	fixture := setupCheckBundleFixture(t)
	alice, bob := fixture.alice.ID.Hex(), fixture.bob.ID.Hex()
	p1 := internal.HexBytes{0xAA, 0xBB, 0xCC, 0xDD}
	p2 := internal.HexBytes{0xDE, 0xAD, 0xBE, 0xEF}
	seedUsedCSPProcess(t, alice, fixture.bundleID, p1)
	seedUsedCSPProcess(t, alice, fixture.bundleID, p2)

	// no elections or no (valid) members: nothing to ask
	got, err := testDB.CSPProcessesByMembers(nil, []string{alice})
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.HasLen, 0)
	got, err = testDB.CSPProcessesByMembers([]internal.HexBytes{p1}, []string{"not-hex"})
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.HasLen, 0)

	// only the asked elections, only members with a row; a non-hex id is skipped
	got, err = testDB.CSPProcessesByMembers([]internal.HexBytes{p1}, []string{alice, bob, "not-hex"})
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.HasLen, 1)
	c.Assert(got[0].UserID.String(), qt.Equals, alice)
	c.Assert(got[0].ProcessID, qt.DeepEquals, p1)
	c.Assert(got[0].Used, qt.IsTrue)
	c.Assert(got[0].VotedOnChain, qt.IsFalse)

	got, err = testDB.CSPProcessesByMembers([]internal.HexBytes{p1, p2}, []string{alice, bob})
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.HasLen, 2)
}

func TestMarkCSPProcessVotedOnChain(t *testing.T) {
	c := qt.New(t)
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	fixture := setupCheckBundleFixture(t)
	alice := fixture.alice.ID.Hex()
	aliceID := internal.HexBytesFromString(alice)
	p1 := internal.HexBytes{0xAA, 0xBB, 0xCC, 0xDD}
	p2 := internal.HexBytes{0xDE, 0xAD, 0xBE, 0xEF}
	seedUsedCSPProcess(t, alice, fixture.bundleID, p1)
	seedUsedCSPProcess(t, alice, fixture.bundleID, p2)

	c.Assert(testDB.MarkCSPProcessVotedOnChain(nil, p1), qt.ErrorIs, ErrBadInputs)
	c.Assert(testDB.MarkCSPProcessVotedOnChain(aliceID, nil), qt.ErrorIs, ErrBadInputs)
	// a pair with no row has nothing to cache on, and is not an error
	c.Assert(testDB.MarkCSPProcessVotedOnChain(aliceID, internal.HexBytes{0x01}), qt.IsNil)

	c.Assert(testDB.MarkCSPProcessVotedOnChain(aliceID, p1), qt.IsNil)
	row, err := testDB.CSPProcessByUserAndProcess(aliceID, p1)
	c.Assert(err, qt.IsNil)
	c.Assert(row.VotedOnChain, qt.IsTrue)
	c.Assert(row.Used, qt.IsTrue, qt.Commentf("the flag must not touch the consumption"))
	c.Assert(row.TimesVoted, qt.Equals, 1)
	// only that row
	other, err := testDB.CSPProcessByUserAndProcess(aliceID, p2)
	c.Assert(err, qt.IsNil)
	c.Assert(other.VotedOnChain, qt.IsFalse)
}
