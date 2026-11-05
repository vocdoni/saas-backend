package api

import (
	"net/http"
	"net/url"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
)

// TestVotingProcessParticipantsNationalIDLookup pins the admin nationalId lookup under raw storage:
// a member stored in canonical form is found from any variant; a member stored verbatim with
// separators or non-canonical casing is found only by that exact stored value (R17).
func TestVotingProcessParticipantsNationalIDLookup(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	// canonical-stored: imported as 12345678Z — trimmed storage leaves it in canonical form.
	imported := postOrgMembers(t, token, orgAddress, apicommon.OrgMember{MemberNumber: "P-1", NationalID: "12345678Z"})
	c.Assert(imported, qt.HasLen, 1)
	canonical := imported[0].ID
	// letterless-stored: 87654321 has no check letter; found verbatim or by its canonical form.
	letterless := insertRawMember(t, orgAddress, "87654321")
	// variant-stored: 55.555.565-P has separators; found verbatim but NOT by its canonical spelling.
	variantStored := insertRawMember(t, orgAddress, "55.555.565-P")
	pid, _ := publishCensusProcess(t, token, orgAddress, apicommon.CensusSpec{
		AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID},
		MemberIDs:  []string{canonical, letterless, variantStored},
	}, 1)

	lookup := func(value string) []string {
		t.Helper()
		resp := requestAndParse[apicommon.ProcessParticipantsResponse](t, http.MethodGet, token, nil,
			"processes", pid, "participants?field="+string(db.OrgMemberLookupFieldNationalID)+"&value="+url.QueryEscape(value))
		ids := make([]string, 0, len(resp.Participants))
		for _, p := range resp.Participants {
			ids = append(ids, p.MemberID)
		}
		return ids
	}

	// canonical-stored member (12345678Z) is found from any variant: 12.345.678-Z normalises to 12345678Z
	c.Assert(lookup("12.345.678-Z"), qt.Contains, canonical)
	c.Assert(lookup("12345678Z"), qt.Contains, canonical)
	// letterless-stored member (87654321) is found verbatim; 87654321X also matches via the canonical $in
	c.Assert(lookup("87654321"), qt.Contains, letterless)
	// variant-stored member (55.555.565-P) is found verbatim — the limitation: canonical spelling misses it
	c.Assert(lookup("55.555.565-P"), qt.Contains, variantStored)
	c.Assert(lookup("55555565P"), qt.Not(qt.Contains), variantStored)
	c.Assert(lookup("00000000T"), qt.HasLen, 0)
}
