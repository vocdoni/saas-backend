package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
)

// TestAddMembersNationalIDWarnings pins the import warnings: line-numbered, non-blocking, in the
// sync response and, for an async import, on GET /jobs/{jobId} for a Manager/Admin only.
func TestAddMembersNationalIDWarnings(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	submit := func(prefix string) *apicommon.AddMembersRequest {
		return &apicommon.AddMembersRequest{Members: []apicommon.OrgMember{
			{MemberNumber: prefix + "1", NationalID: "12345678"},
			{MemberNumber: prefix + "2", NationalID: "12.345.678-z"},
			{MemberNumber: prefix + "3", NationalID: "12345678A"},
			{MemberNumber: prefix + "4", NationalID: "K1234567L"},
			{MemberNumber: prefix + "5", NationalID: "PAB123456"},
			{MemberNumber: prefix + "6"},
			{MemberNumber: prefix + "7", NationalID: "X1234567L"},
		}}
	}
	want := []db.NationalIDWarning{
		{Line: 1, Class: "derived_letter", Original: "12345678", Derived: "12345678Z"},
		{Line: 3, Class: "letter_mismatch"},
		{Line: 4, Class: "unsupported_document"},
		{Line: 5, Class: "invalid"},
		{Line: 1, Class: db.NationalIDWarningDuplicate},
		{Line: 2, Class: db.NationalIDWarningDuplicate},
	}

	c.Run("sync", func(c *qt.C) {
		resp := requestAndParse[apicommon.AddMembersResponse](t, http.MethodPost, token, submit("S-"),
			organizationMembersURL(orgAddress.String()))
		c.Assert(resp.Added, qt.Equals, uint32(7)) // warnings never block
		c.Assert(resp.Errors, qt.HasLen, 0)
		c.Assert(resp.NationalIDWarnings, qt.DeepEquals, want)
	})

	c.Run("async", func(c *qt.C) {
		resp := requestAndParse[apicommon.AddMembersResponse](t, http.MethodPost, token, submit("A-"),
			organizationMembersURL(orgAddress.String())+"?async=true")
		c.Assert(resp.JobID, qt.Not(qt.HasLen), 0)

		// an anonymous poll sees the job finish, but not the DNI values in its warnings
		anonymous := pollJob(t, resp.JobID.String())
		c.Assert(anonymous.Status, qt.Equals, db.JobStatusCompleted)
		c.Assert(anonymous.NationalIDWarnings, qt.IsNil)

		admin := requestAndParse[apicommon.JobResponse](t, http.MethodGet, token, nil, "jobs", resp.JobID.String())
		c.Assert(admin.NationalIDWarnings, qt.DeepEquals, want)

		// the org job list is Manager/Admin-only and carries them too
		listed := pollOrgJob(t, token, orgAddress.String(), resp.JobID.String())
		c.Assert(listed.NationalIDWarnings, qt.DeepEquals, want)
	})
}
