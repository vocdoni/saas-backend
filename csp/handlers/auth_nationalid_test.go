package handlers

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/db"
)

// TestNationalIDRewritten pins when auth/0 tries the raw (verbatim) login hash before the
// canonical one: only on a census logging in with the nationalId, and only when canonicalisation
// changes the trimmed typed value beyond case.
func TestNationalIDRewritten(t *testing.T) {
	c := qt.New(t)
	withID := &db.Census{AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID}}
	withoutID := &db.Census{AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}}

	for _, tc := range []struct {
		typed     string
		rewritten bool
	}{
		{"12345678", true},       // derived letter
		{" 12.345.678-Z ", true}, // separators
		{"2345678T", true},       // padding
		{"12 345 678", true},
		{"12345678Z", false}, // canonical already
		{" 12345678z ", false},
		{"12345678A", false}, // wrong letter: passed through
		{"12.345.678-A", false},
		{"PAB123456", false},
		{"", false},
	} {
		input := (&db.OrgMember{NationalID: tc.typed, MemberNumber: "M-1"}).Normalized()
		before := *input
		c.Assert(nationalIDRewritten(withID, input), qt.Equals, tc.rewritten, qt.Commentf("typed %q", tc.typed))
		c.Assert(nationalIDRewritten(withoutID, input), qt.IsFalse, qt.Commentf("typed %q", tc.typed))
		// the predicate never rewrites the login it inspects
		c.Assert(*input, qt.DeepEquals, before)
	}
}

// TestParticipantByLoginNationalID pins the exact-first lookup over a census holding a collision
// survivor: a letterless member (12345678) left on its raw hash next to its lettered twin
// (12345678Z), as migration 0036 leaves them. Each one resolves from their own input and the
// letterless input never resolves to the twin; every other login resolves canonically.
func TestParticipantByLoginNationalID(t *testing.T) {
	c := qt.New(t)
	org := common.Address{0x1d, 0x5a, byte(time.Now().UnixNano())}
	c.Assert(testDB.SetOrganization(&db.Organization{Address: org, CreatedAt: time.Now()}), qt.IsNil)
	setMember := func(nationalID string) string {
		id, err := testDB.SetOrgMember("salt", &db.OrgMember{OrgAddress: org, NationalID: nationalID, Weight: 1})
		c.Assert(err, qt.IsNil)
		return id
	}
	twin, variant, survivorID := setMember("12345678Z"), setMember("2.345.678-t"), setMember("12345678")

	census := &db.Census{OrgAddress: org, AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID}}
	_, _, err := testDB.PopulateMembersCensus(census, []string{twin, variant})
	c.Assert(err, qt.IsNil)
	// the survivor's participant keeps the raw hash of its stored value, as written before 0036
	survivor, err := testDB.OrgMember(org, survivorID)
	c.Assert(err, qt.IsNil)
	c.Assert(testDB.SetCensusParticipant(&db.CensusParticipant{
		ParticipantID: survivorID, CensusID: census.ID.Hex(),
		LoginHash: db.HashAuthTwoFaFields(*survivor, census.AuthFields, census.TwoFaFields),
	}), qt.IsNil)

	h := New(&csp.CSP{Storage: testDB}, testDB, nil)
	login := func(typed string) (*db.CensusParticipant, error) {
		return h.participantByLogin(census, (&db.OrgMember{OrgAddress: org, NationalID: typed}).Normalized())
	}
	for typed, want := range map[string]string{
		"12345678":   survivorID,
		" 12345678 ": survivorID,
		"12345678Z":  twin,
		"12345678z":  twin,
		// no raw holder: the canonical fallback resolves to the twin, the ambiguity the spec
		// accepts for a variant the survivor never matched before normalisation either
		"12.345.678-Z": twin,
		"12.345.678":   twin,
		// a participant written now holds the canonical hash, so even its stored variant resolves
		// through the fallback
		"2.345.678-t": variant,
		"02345678T":   variant,
		"2345678":     variant,
	} {
		found, err := login(typed)
		c.Assert(err, qt.IsNil, qt.Commentf("typed %q", typed))
		c.Assert(found.ParticipantID, qt.Equals, want, qt.Commentf("typed %q", typed))
	}
	_, err = login("87654321X")
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
}
