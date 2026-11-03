package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
)

// TestOrgMemberNationalIDOriginalReadOnly pins that nationalIdOriginal is read-only over the API: it
// is whatever the server derived when it filled in a DNI check letter, never what a client sends.
func TestOrgMemberNationalIDOriginalReadOnly(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	members := postOrgMembers(t, token, orgAddress,
		apicommon.OrgMember{Email: "letterless@example.com", NationalID: "12345678", NationalIDOriginal: "forged"},
		apicommon.OrgMember{Email: "lettered@example.com", NationalID: "87654321x", NationalIDOriginal: "forged"},
	)
	byEmail := make(map[string]apicommon.OrgMember, len(members))
	for _, m := range members {
		byEmail[m.Email] = m
	}
	letterless, lettered := byEmail["letterless@example.com"], byEmail["lettered@example.com"]
	c.Assert(letterless.NationalID, qt.Equals, "12345678Z")
	c.Assert(letterless.NationalIDOriginal, qt.Equals, "12345678")
	c.Assert(lettered.NationalID, qt.Equals, "87654321X")
	c.Assert(lettered.NationalIDOriginal, qt.Equals, "")

	// an update that does not carry nationalId keeps the derived marker, whatever the body says
	requestAndParse[apicommon.UpsertOrgMemberResponse](t, http.MethodPut, token, map[string]any{
		"id": letterless.ID, "name": "Ana", "nationalIdOriginal": "forged",
	}, organizationMembersURL(orgAddress.String()))
	got := getOrgMember(t, token, orgAddress, letterless.ID)
	c.Assert(got.Name, qt.Equals, "Ana")
	c.Assert(got.NationalIDOriginal, qt.Equals, "12345678")

	// a create carrying it with a lettered DNI stores no marker
	created := requestAndParse[apicommon.UpsertOrgMemberResponse](t, http.MethodPut, token, map[string]any{
		"email": "created@example.com", "nationalId": "11111111H", "nationalIdOriginal": "forged",
	}, organizationMembersURL(orgAddress.String()))
	got = getOrgMember(t, token, orgAddress, created.ID)
	c.Assert(got.NationalID, qt.Equals, "11111111H")
	c.Assert(got.NationalIDOriginal, qt.Equals, "")
}
