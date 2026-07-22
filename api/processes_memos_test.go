package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/proto/build/go/models"
)

// TestVotingProcessMemos casts several CSP votes carrying free-text memos (one memo repeated,
// one voter with no memo), ends the election so it reaches RESULTS status, and asserts
// GET /processes/{id}/results/memos returns every memo repeated once per vote that carried it,
// gated to the owning org's managers. Memos are only exposed once the election is in results.
func TestVotingProcessMemos(t *testing.T) {
	c := qt.New(t)

	token := testCreateUser(t, "superpassword123")
	vocdoniClient := testNewVocdoniClient(t)
	orgAddress := testCreateOrganization(t, token)

	// subscribe the organization to a plan
	plans, err := testDB.Plans()
	c.Assert(err, qt.IsNil)
	c.Assert(len(plans) > 1, qt.IsTrue)
	c.Assert(testDB.SetOrganizationSubscription(orgAddress, &db.OrganizationSubscription{
		PlanID:          plans[1].ID,
		StartDate:       time.Now(),
		RenewalDate:     time.Now().Add(time.Hour * 24),
		LastPaymentDate: time.Now(),
		Active:          true,
	}), qt.IsNil)

	// create the organization account on-chain
	orgName := fmt.Sprintf("memoorg-%d", internal.RandomInt(1000))
	orgInfoURI := fmt.Sprintf("https://example.com/%d", internal.RandomInt(1000))
	nonce := uint32(0)
	accountTx := &models.Tx{Payload: &models.Tx_SetAccount{SetAccount: &models.SetAccountTx{
		Nonce:   &nonce,
		Txtype:  models.TxType_CREATE_ACCOUNT,
		Account: orgAddress.Bytes(),
		Name:    &orgName,
		InfoURI: &orgInfoURI,
	}}}
	signRemoteSignerAndSendVocdoniTx(t, accountTx, token, vocdoniClient, orgAddress)

	// create an on-chain election whose census root is the CSP public key
	cspPubKey, err := testCSP.PubKey()
	c.Assert(err, qt.IsNil)
	processNonce := fetchVocdoniAccountNonce(t, vocdoniClient, orgAddress)
	processTx := &models.Tx{Payload: &models.Tx_NewProcess{NewProcess: &models.NewProcessTx{
		Txtype: models.TxType_NEW_PROCESS,
		Nonce:  processNonce,
		Process: &models.Process{
			EntityId:      orgAddress.Bytes(),
			Duration:      120,
			Status:        models.ProcessStatus_READY,
			CensusOrigin:  models.CensusOrigin_OFF_CHAIN_CA,
			CensusRoot:    cspPubKey,
			MaxCensusSize: 10,
			EnvelopeType:  &models.EnvelopeType{Anonymous: false, CostFromWeight: false},
			VoteOptions:   &models.ProcessVoteOptions{MaxCount: 1, MaxValue: 5},
			Mode:          &models.ProcessMode{AutoStart: true, Interruptible: true},
		},
	}}}
	processIDBytes := signRemoteSignerAndSendVocdoniTx(t, processTx, token, vocdoniClient, orgAddress)
	processID := internal.HexBytes(processIDBytes)

	// census / group / bundle setup for CSP voting
	authFields := db.OrgMemberAuthFields{
		db.OrgMemberAuthFieldsName,
		db.OrgMemberAuthFieldsSurname,
		db.OrgMemberAuthFieldsMemberNumber,
	}
	twoFaFields := db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}

	// four voters. The memo is gated to the open choice (value 1): only memos cast by a vote that
	// selected value 1 are surfaced. One/Two select the open choice with the same memo (both
	// returned), Three selects it with no memo (omitted), Four selects the non-open choice (value 2)
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
		}, 2, []byte("ignored")},
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

	group := postGroup(t, token, orgAddress, memberIDs(members)...)
	censusID := postCensus(t, token, orgAddress, authFields, twoFaFields)
	requestAndParse[apicommon.PublishedCensusResponse](
		t, http.MethodPost, token, &apicommon.PublishCensusGroupRequest{
			AuthFields:  authFields,
			TwoFaFields: twoFaFields,
		}, "census", censusID, "group", group.ID, "publish")
	bundleID, _ := postProcessBundle(t, token, censusID, processIDBytes)

	// seed the multi-question voting process pointing at the on-chain election, published.
	// Must exist before voting: the relay resolves the target election via QuestionByUpstreamID.
	vpID, err := testDB.SetVotingProcess(&db.VotingProcess{
		OrgAddress: orgAddress,
		Published:  true,
		Title:      db.MultiLangString{"default": "Memo process"}, //nolint:goconst
	})
	c.Assert(err, qt.IsNil)
	_, err = testDB.SetQuestion(&db.VotingProcessQuestion{
		ProcessID:  vpID,
		OrgAddress: orgAddress,
		Order:      0,
		Title:      db.MultiLangString{"default": "Q1"},
		Choices: []db.Choice{
			{Title: db.MultiLangString{"default": "Yes"}, Value: 1, OpenValue: true},
			{Title: db.MultiLangString{"default": "No"}, Value: 2},
		},
		UpstreamID: processID,
	})
	c.Assert(err, qt.IsNil)

	// cast each voter's ballot with its memo
	for i := range members {
		authToken := testCSPAuthenticateWithFields(t, bundleID, &handlers.AuthRequest{
			Name:         members[i].Name,
			Surname:      members[i].Surname,
			MemberNumber: members[i].MemberNumber,
			Email:        members[i].Email,
		})
		voter := ethereum.SignKeys{}
		c.Assert(voter.Generate(), qt.IsNil)
		voterAddr := voter.Address().Bytes()
		signature := testCSPSign(t, bundleID, authToken, processID, voterAddr)
		proof := testGenerateVoteProof(processID, voterAddr, signature, 1)
		// canonical vote package so the memos endpoint can read the selected choice value.
		votePackage := []byte(fmt.Sprintf(`{"votes":[%d]}`, voters[i].vote))
		testRelayVoteRequest(t, &voter, processID, proof, votePackage, voters[i].memo)
	}

	// end the election so it advances to RESULTS status — memos are only exposed then.
	endNonce := fetchVocdoniAccountNonce(t, vocdoniClient, orgAddress)
	endStatus := models.ProcessStatus_ENDED
	endTx := &models.Tx{Payload: &models.Tx_SetProcess{SetProcess: &models.SetProcessTx{
		Txtype:    models.TxType_SET_PROCESS_STATUS,
		Nonce:     endNonce,
		ProcessId: processID.Bytes(),
		Status:    &endStatus,
	}}}
	signRemoteSignerAndSendVocdoniTx(t, endTx, token, vocdoniClient, orgAddress)
	waitForElectionStatus(t, processID, "RESULTS")

	// manager reads the memos: only the two open-choice "lalala" memos come back — the no-memo vote
	// and the non-open-choice "ignored" memo are both excluded.
	resp := requestAndParse[apicommon.VotingProcessMemosResponse](
		t, http.MethodGet, token, nil, "processes", vpID.Hex(), "results", "memos")
	c.Assert(resp.Questions, qt.HasLen, 1)
	c.Assert(resp.Questions[0].QuestionID, qt.Not(qt.HasLen), 0)
	memos := resp.Questions[0].Memos
	c.Assert(memos, qt.HasLen, 2)
	for _, m := range memos {
		c.Assert(m, qt.Equals, "lalala")
	}

	// a user with no role for the org cannot read the memos.
	otherToken := testCreateUser(t, "outsiderpass123")
	requestAndAssertCode(http.StatusUnauthorized,
		t, http.MethodGet, otherToken, nil, "processes", vpID.Hex(), "results", "memos")
}
