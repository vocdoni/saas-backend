package api

import (
	"fmt"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/proto/build/go/models"
)

// TestVotingProcessMemos casts several CSP votes carrying free-text memos (one memo repeated, one
// voter with no memo, one memo cast with a non-open choice), ends the election, and asserts the memos
// are folded into the per-question QuestionResults — only for a manager/admin caller, and only the
// memos cast alongside the question's open-value choice. Anonymous callers never receive them.
func TestVotingProcessMemos(t *testing.T) {
	c := qt.New(t)

	token := testCreateUser(t, "superpassword123")
	vocdoniClient := testNewVocdoniClient(t)
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	// census setup for CSP voting
	authFields := db.OrgMemberAuthFields{
		db.OrgMemberAuthFieldsName,
		db.OrgMemberAuthFieldsSurname,
		db.OrgMemberAuthFieldsMemberNumber,
	}
	twoFaFields := db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}

	// four voters. The memo is gated to the open choice (value 1): only memos cast by a vote that
	// selected value 1 are surfaced. One/Two select the open choice with the same memo (both
	// returned), Three selects it with no memo (omitted), Four selects the non-open choice (value 0)
	// with a memo that must be dropped.
	voters := []struct {
		member apicommon.OrgMember
		vote   int
		memo   []byte
	}{
		{apicommon.OrgMember{
			Name: "Voter", Surname: "One", MemberNumber: "M001", NationalID: "MEMO0001A", //nolint:goconst
			BirthDate: "1990-01-01", Email: "memo1@example.com", Phone: "+34699000101", Weight: "1", //nolint:goconst
		}, 1, []byte("lalala")},
		{apicommon.OrgMember{
			Name: "Voter", Surname: "Two", MemberNumber: "M002", NationalID: "MEMO0002B",
			BirthDate: "1990-01-02", Email: "memo2@example.com", Phone: "+34699000102", Weight: "1",
		}, 1, []byte("lalala")},
		{apicommon.OrgMember{
			Name: "Voter", Surname: "Three", MemberNumber: "M003", NationalID: "MEMO0003C",
			BirthDate: "1990-01-03", Email: "memo3@example.com", Phone: "+34699000103", Weight: "1",
		}, 1, nil},
		{apicommon.OrgMember{
			Name: "Voter", Surname: "Four", MemberNumber: "M004", NationalID: "MEMO0004D",
			BirthDate: "1990-01-04", Email: "memo4@example.com", Phone: "+34699000104", Weight: "1",
		}, 0, []byte("ignored")},
	}
	members := make([]apicommon.OrgMember, len(voters))
	for i := range voters {
		members[i] = voters[i].member
	}
	postedOrgMembers := postOrgMembers(t, token, orgAddress, members...)
	idByNationalID := make(map[string]string, len(postedOrgMembers))
	for _, m := range postedOrgMembers {
		idByNationalID[m.NationalID] = m.ID
	}
	for i := range members {
		members[i].ID = idByNationalID[members[i].NationalID]
	}

	// a published process whose single question offers the open choice as value 1
	req := minimalVotingProcessRequest(orgAddress)
	req.StartDate = ""
	req.Census = apicommon.CensusSpec{AuthFields: authFields, TwoFaFields: twoFaFields, MemberIDs: memberIDs(members)}
	req.Questions[0].Choices = []db.Choice{
		{Title: db.MultiLangString{"default": "No"}, Value: 0},
		{Title: db.MultiLangString{"default": "Yes"}, Value: 1, OpenValue: true},
	}
	pid, elections := publishProcessRequest(t, token, req)
	processID := elections[0]

	// cast each voter's ballot with its memo
	for i := range members {
		authToken := testCSPAuthenticateWithFields(t, pid, &handlers.AuthRequest{
			Name:         members[i].Name,
			Surname:      members[i].Surname,
			MemberNumber: members[i].MemberNumber,
			Email:        members[i].Email,
		})
		voter := ethereum.SignKeys{}
		c.Assert(voter.Generate(), qt.IsNil)
		voterAddr := voter.Address().Bytes()
		signature := testCSPSign(t, pid, authToken, processID, voterAddr)
		proof := testGenerateVoteProof(processID, voterAddr, signature, 1)
		// canonical vote package so the results can correlate the memo to the selected choice value.
		votePackage := []byte(fmt.Sprintf(`{"votes":[%d]}`, voters[i].vote))
		testRelayVoteRequest(t, &voter, processID, proof, votePackage, voters[i].memo)
	}

	// end the election so the tally is final and the votes are fully indexed (memos correlate live for
	// this non-encrypted election, but ending makes the assertions deterministic).
	endNonce := fetchVocdoniAccountNonce(t, vocdoniClient, orgAddress)
	endStatus := models.ProcessStatus_ENDED
	endTx := &models.Tx{Payload: &models.Tx_SetProcess{SetProcess: &models.SetProcessTx{
		Txtype:    models.TxType_SET_PROCESS_STATUS,
		Nonce:     endNonce,
		ProcessId: processID.Bytes(),
		Status:    &endStatus,
	}}}
	signAsOrgAndSendVocdoniTx(t, endTx, orgAddress, vocdoniClient)
	waitForElectionStatus(t, processID, "RESULTS")

	// manager GET /processes/{id}/results: the open-value question's results carry only the two
	// open-choice "lalala" memos — the no-memo vote and the non-open-choice "ignored" memo are excluded.
	res := requestAndParse[apicommon.VotingProcessResultsResponse](
		t, http.MethodGet, token, nil, "processes", pid, "results")
	c.Assert(res.Questions, qt.HasLen, 1)
	c.Assert(res.Questions[0].Memos, qt.HasLen, 2)
	for _, m := range res.Questions[0].Memos {
		c.Assert(m, qt.Equals, "lalala")
	}

	// manager GET /processes/{id}: same memos folded inline into the question's results.
	info := requestAndParse[apicommon.VotingProcessResponse](
		t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(info.Questions, qt.HasLen, 1)
	c.Assert(info.Questions[0].Results, qt.Not(qt.IsNil))
	c.Assert(info.Questions[0].Results.Memos, qt.HasLen, 2)

	// memos are manager-only: an anonymous caller reads the results but never the memos.
	anonRes := requestAndParse[apicommon.VotingProcessResultsResponse](
		t, http.MethodGet, "", nil, "processes", pid, "results")
	c.Assert(anonRes.Questions, qt.HasLen, 1)
	c.Assert(anonRes.Questions[0].Memos, qt.HasLen, 0)
	anonInfo := requestAndParse[apicommon.VotingProcessResponse](
		t, http.MethodGet, "", nil, "processes", pid)
	c.Assert(anonInfo.Questions[0].Results, qt.Not(qt.IsNil))
	c.Assert(anonInfo.Questions[0].Results.Memos, qt.HasLen, 0)
}
