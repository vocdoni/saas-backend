package api

import (
	"net/http"
	"net/url"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
)

// TestVotingProcessParticipantsNationalIDLookup pins the admin nationalId lookup: a member stored
// canonical is found from any variant, and a value stored verbatim (a member migration 34 skipped
// on a collision) is still found from that exact value.
func TestVotingProcessParticipantsNationalIDLookup(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	imported := postOrgMembers(t, token, orgAddress, apicommon.OrgMember{MemberNumber: "P-1", NationalID: "87.654.321-x"})
	c.Assert(imported, qt.HasLen, 1)
	c.Assert(imported[0].NationalID, qt.Equals, "87654321X")
	canonical := imported[0].ID
	letterless := insertRawMember(t, orgAddress, "12345678")
	lettered := insertRawMember(t, orgAddress, "12345678Z")
	pid, _ := publishCensusProcess(t, token, orgAddress, apicommon.CensusSpec{
		AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID},
		MemberIDs:  []string{canonical, letterless, lettered},
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

	c.Assert(lookup("87.654.321-X"), qt.DeepEquals, []string{canonical})
	c.Assert(lookup(" 87654321x "), qt.DeepEquals, []string{canonical})
	c.Assert(lookup("87654321X"), qt.DeepEquals, []string{canonical})
	// the letterless value finds the skipped member verbatim, and its canonical twin too
	c.Assert(lookup("12345678"), qt.ContentEquals, []string{letterless, lettered})
	c.Assert(lookup("12345678Z"), qt.DeepEquals, []string{lettered})
	c.Assert(lookup("00000000T"), qt.HasLen, 0)
}
