package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

// TestCertificateCensusAuthMethod pins the census authMethod rules: a certificate census logs in with
// exactly the nationalId and no 2FA field, an unknown method is refused, and the method round-trips
// through SetCensus.
func TestCertificateCensusAuthMethod(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)

	nationalID := OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}
	email := OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail}
	for _, tc := range []struct {
		method CensusAuthMethod
		auth   OrgMemberAuthFields
		twoFa  OrgMemberTwoFaFields
		ok     bool
	}{
		{method: CensusAuthMethodCertificate, auth: nationalID, ok: true},
		{method: CensusAuthMethodCertificate, auth: nationalID, twoFa: email},
		{method: CensusAuthMethodCertificate, auth: OrgMemberAuthFields{OrgMemberAuthFieldsName}},
		{method: CensusAuthMethodCertificate, auth: OrgMemberAuthFields{OrgMemberAuthFieldsNationalID, OrgMemberAuthFieldsName}},
		{method: CensusAuthMethodCertificate},
		{method: "fingerprint", auth: nationalID},
		{auth: nationalID, twoFa: email, ok: true},
	} {
		comment := qt.Commentf("method=%q auth=%v twoFa=%v", tc.method, tc.auth, tc.twoFa)
		err := ValidateCensusAuthMethod(tc.method, tc.auth, tc.twoFa)
		if tc.ok {
			c.Assert(err, qt.IsNil, comment)
			continue
		}
		c.Assert(err, qt.ErrorIs, ErrInvalidCensusAuthMethod, comment)
		_, err = testDB.SetCensus(&Census{
			OrgAddress: testOrgAddress, AuthMethod: tc.method, AuthFields: tc.auth, TwoFaFields: tc.twoFa,
		})
		c.Assert(err, qt.ErrorIs, ErrInvalidCensusAuthMethod, comment)
	}

	id, err := testDB.SetCensus(&Census{
		OrgAddress: testOrgAddress, AuthMethod: CensusAuthMethodCertificate, AuthFields: nationalID, Anonymous: true,
	})
	c.Assert(err, qt.IsNil)
	stored, err := testDB.Census(id)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.AuthMethod, qt.Equals, CensusAuthMethodCertificate)
	// a certificate census is auth-only: the blind path keys on Anonymous alone
	c.Assert(stored.Type, qt.Equals, CensusTypeAuthOnly)
	c.Assert(stored.Anonymous, qt.IsTrue)

	// a typed-fields census stores no authMethod at all
	id, err = testDB.SetCensus(&Census{OrgAddress: testOrgAddress, AuthFields: nationalID})
	c.Assert(err, qt.IsNil)
	stored, err = testDB.Census(id)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.AuthMethod, qt.Equals, CensusAuthMethod(""))
}

// TestCertAuthCounterWindows pins the windows of the certificate sign-in counters: creates per UTC
// minute, failed redeems per UTC hour.
func TestCertAuthCounterWindows(t *testing.T) {
	c := qt.New(t)
	c.Assert(CSPCounterWindow(CSPCounterCertAuthCreates), qt.Equals, time.Minute)
	c.Assert(CSPCounterWindow(CSPCounterCertAuthFailures), qt.Equals, time.Hour)
	now := time.Date(2026, 11, 4, 10, 15, 42, 0, time.UTC)
	creates := currentCSPCounterBucket(CSPCounterCertAuthCreates, []byte("ip|process"), now)
	c.Assert(creates.resetAt.Equal(time.Date(2026, 11, 4, 10, 16, 0, 0, time.UTC)), qt.IsTrue)
	c.Assert(creates.expiry.Equal(time.Date(2026, 11, 4, 10, 17, 0, 0, time.UTC)), qt.IsTrue)
}

// TestCertificateCensusBuild pins how a certificate census is built (contract section 2): a member
// without a nationalId is skipped and left out of the size, while a present but unmatchable value
// (a wrong check letter) is a participant, counted, that the canonical twin number never matches.
func TestCertificateCensusBuild(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)

	addMember := func(nationalID string) string {
		id, err := testDB.SetOrgMember(testSalt, &OrgMember{OrgAddress: testOrgAddress, NationalID: nationalID})
		c.Assert(err, qt.IsNil)
		return id
	}
	separators := addMember("12.345.678-z")
	wrongLetter := addMember("87654321A") // canonical twin: 87654321X
	missing := addMember("")

	census := &Census{
		OrgAddress: testOrgAddress, AuthMethod: CensusAuthMethodCertificate,
		AuthFields: OrgMemberAuthFields{OrgMemberAuthFieldsNationalID},
	}
	_, err := testDB.SetCensus(census)
	c.Assert(err, qt.IsNil)
	size, skipped, err := testDB.PopulateMembersCensus(census, []string{separators, wrongLetter, missing})
	c.Assert(err, qt.IsNil)
	c.Assert(size, qt.Equals, int64(2))
	c.Assert(skipped, qt.HasLen, 1)
	c.Assert(skipped[0].Hex(), qt.Equals, missing)
	stored, err := testDB.Census(census.ID.Hex())
	c.Assert(err, qt.IsNil)
	c.Assert(stored.Size, qt.Equals, int64(2))
	c.Assert(stored.AuthMethod, qt.Equals, CensusAuthMethodCertificate)

	// what a certificate presents is always the canonical 9-character number
	lookup := func(number string) (*CensusParticipant, error) {
		return testDB.CensusParticipantByLoginHash(*stored, OrgMember{NationalID: number})
	}
	p, err := lookup("12345678Z")
	c.Assert(err, qt.IsNil)
	c.Assert(p.ParticipantID, qt.Equals, separators)
	_, err = lookup("87654321X")
	c.Assert(err, qt.ErrorIs, ErrNotFound)
	// the member's own stored value is still matched by itself, as it always was
	p, err = lookup("87654321A")
	c.Assert(err, qt.IsNil)
	c.Assert(p.ParticipantID, qt.Equals, wrongLetter)
}
