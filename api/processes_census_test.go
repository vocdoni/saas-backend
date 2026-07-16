package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
)

// TestValidateProcessCensus exercises POST /processes/census/validation over the whole org, an
// explicit memberIds subset (db.CheckMembersFields), and the duplicate-detection / auth paths.
func TestValidateProcessCensus(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	members := postOrgMembers(t, token, orgAddress, newOrgMembers(3)...)
	authNameSurname := db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname}

	validate := func(jwt string, spec apicommon.CensusSpec) int {
		_, code := testRequest(t, http.MethodPost, jwt,
			&apicommon.ValidateProcessCensusRequest{OrgAddress: orgAddress, Census: spec},
			"processes", "census", "validation")
		return code
	}

	// whole org (no group, no memberIds): 3 distinct members validate cleanly.
	c.Assert(validate(token, apicommon.CensusSpec{AuthFields: authNameSurname}), qt.Equals, http.StatusOK)
	// explicit memberIds subset (distinct) also validates.
	c.Assert(validate(token, apicommon.CensusSpec{
		AuthFields: authNameSurname, MemberIDs: []string{members[0].ID, members[1].ID},
	}), qt.Equals, http.StatusOK)

	// add a member that duplicates member[0] on name+surname.
	dup := apicommon.OrgMember{
		MemberNumber: "DUP1", Name: members[0].Name, Surname: members[0].Surname,
		Email: "dup1@example.com", Phone: "+34699999991", Password: "pw", NationalID: "DNIDUP1", BirthDate: "1980-01-01",
	}
	all := postOrgMembers(t, token, orgAddress, dup)
	var dupID string
	for _, m := range all {
		if m.Email == dup.Email {
			dupID = m.ID
		}
	}
	c.Assert(dupID, qt.Not(qt.Equals), "")

	// whole org now contains a name+surname duplicate → 400.
	c.Assert(validate(token, apicommon.CensusSpec{AuthFields: authNameSurname}), qt.Equals, http.StatusBadRequest)
	// the memberIds subset that includes the duplicate is also rejected.
	c.Assert(validate(token, apicommon.CensusSpec{
		AuthFields: authNameSurname, MemberIDs: []string{members[0].ID, dupID},
	}), qt.Equals, http.StatusBadRequest)

	// no auth/2FA fields at all → 400.
	c.Assert(validate(token, apicommon.CensusSpec{}), qt.Equals, http.StatusBadRequest)

	// a user with no role for the org cannot validate.
	other := testCreateUser(t, "otherpass123")
	c.Assert(validate(other, apicommon.CensusSpec{AuthFields: authNameSurname}), qt.Equals, http.StatusUnauthorized)
}
