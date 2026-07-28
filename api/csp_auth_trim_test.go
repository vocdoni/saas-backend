package api

import (
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
)

// TestCSPAuthNormalizesInput covers the login normalization end to end, over HTTP.
//
// The CSP login hash is a byte-exact match over the census auth fields, so any
// disagreement between how a field is stored and how it is typed at login locks
// the voter out. Members are stored trimmed and the login input is normalized the
// same way, so surrounding whitespace on either side is irrelevant; and the values
// feeding the hash are folded to lowercase on both sides, so casing is too — while
// the member document keeps the casing it was imported with.
func TestCSPAuthNormalizesInput(t *testing.T) {
	c := qt.New(t)

	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)

	// Padded exactly the way a spreadsheet or CSV import pads them.
	members := []apicommon.OrgMember{
		{
			MemberNumber: "M-001 ",
			Name:         " John",
			Surname:      "Doe ",
			NationalID:   "DNI001",
			BirthDate:    "1980-01-01",
		},
		{
			MemberNumber: "M-002",
			Name:         "Jane",
			Surname:      "Roe",
			NationalID:   "DNI002",
			BirthDate:    "1981-02-03",
		},
	}
	orgMembers := postOrgMembers(t, adminToken, orgAddress, members...)
	c.Assert(orgMembers, qt.HasLen, 2)

	// postOrgMembers re-reads the members through the listing endpoint, so the
	// returned order is the database's, not the submitted one. Index by national
	// ID, which no test here pads.
	byNationalID := make(map[string]apicommon.OrgMember, len(orgMembers))
	for _, m := range orgMembers {
		byNationalID[m.NationalID] = m
	}
	john, jane := byNationalID["DNI001"], byNationalID["DNI002"]
	c.Assert(john.ID, qt.Not(qt.Equals), "")
	c.Assert(jane.ID, qt.Not(qt.Equals), "")

	// The padding is gone the moment the member is stored.
	c.Assert(john.Name, qt.Equals, "John")
	c.Assert(john.Surname, qt.Equals, "Doe")
	c.Assert(john.MemberNumber, qt.Equals, "M-001")

	t.Run("padded member data is stored trimmed and authenticates clean", func(_ *testing.T) {
		// An auth-only census: no second factor, so auth/0 resolves the
		// participant and returns a token in one step.
		authFields := db.OrgMemberAuthFields{
			db.OrgMemberAuthFieldsName,
			db.OrgMemberAuthFieldsSurname,
			db.OrgMemberAuthFieldsMemberNumber,
		}
		censusID, _, _ := createGroupBasedCensus(t, adminToken, orgAddress, authFields,
			db.OrgMemberTwoFaFields{}, john.ID, jane.ID)
		bundleID, _ := postProcessBundle(t, adminToken, censusID, randomProcessID())

		// The values as a voter would type them, with no padding at all. Before
		// the fix this member could never authenticate.
		postProcessBundleAuth0(t, bundleID, &handlers.AuthRequest{
			Name:         "John",
			Surname:      "Doe",
			MemberNumber: "M-001",
		})

		// And padded input authenticates too, so a copied-and-pasted value with a
		// stray space still works.
		postProcessBundleAuth0(t, bundleID, &handlers.AuthRequest{
			Name:         " John ",
			Surname:      " Doe",
			MemberNumber: "M-001  ",
		})
	})

	t.Run("login is case-insensitive while the member keeps its casing", func(_ *testing.T) {
		authFields := db.OrgMemberAuthFields{
			db.OrgMemberAuthFieldsName,
			db.OrgMemberAuthFieldsSurname,
			db.OrgMemberAuthFieldsMemberNumber,
		}
		censusID, _, _ := createGroupBasedCensus(t, adminToken, orgAddress, authFields,
			db.OrgMemberTwoFaFields{}, john.ID)
		bundleID, _ := postProcessBundle(t, adminToken, censusID, randomProcessID())

		// The member was imported as "John Doe" / "M-001"; any casing resolves.
		for _, req := range []*handlers.AuthRequest{
			{Name: "John", Surname: "Doe", MemberNumber: "M-001"},
			{Name: "john", Surname: "doe", MemberNumber: "m-001"},
			{Name: "JOHN", Surname: "DOE", MemberNumber: "M-001"},
			{Name: " jOhN ", Surname: "dOe", MemberNumber: " m-001"},
		} {
			postProcessBundleAuth0(t, bundleID, req)
		}

		// Folding is confined to the hash: the member still reads back with the
		// casing it was imported with, which is what gets displayed and exported.
		stored := getOrgMember(t, adminToken, orgAddress, john.ID)
		c.Assert(stored.Name, qt.Equals, "John")
		c.Assert(stored.Surname, qt.Equals, "Doe")
		c.Assert(stored.MemberNumber, qt.Equals, "M-001")
	})

	t.Run("birthdate is canonicalized on both sides", func(_ *testing.T) {
		// Stored birthdates are always canonicalized to YYYY-MM-DD, so the login
		// input has to be canonicalized the same way for the hashes to agree.
		authFields := db.OrgMemberAuthFields{
			db.OrgMemberAuthFieldsName,
			db.OrgMemberAuthFieldsBirthDate,
		}
		censusID, _, _ := createGroupBasedCensus(t, adminToken, orgAddress, authFields,
			db.OrgMemberTwoFaFields{}, jane.ID)
		bundleID, _ := postProcessBundle(t, adminToken, censusID, randomProcessID())

		// Canonical form.
		postProcessBundleAuth0(t, bundleID, &handlers.AuthRequest{
			Name:      "Jane",
			BirthDate: "1981-02-03",
		})

		// Day-first form, which internal.ParseBirthDate also accepts on import.
		postProcessBundleAuth0(t, bundleID, &handlers.AuthRequest{
			Name:      "Jane",
			BirthDate: "03/02/1981",
		})
	})
}
