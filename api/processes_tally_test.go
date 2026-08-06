package api

import (
	"encoding/hex"
	"net/http"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/types"
)

// TestVotingProcessMultichoiceTally is the regression test issue #619 needed and did not have: it
// casts a real multichoice ballot on chain and reads the tally back.
//
// The bug was invisible through the API. A multichoice question mapped typeSetup.uniqueChoices onto
// the on-chain uniqueValues, which the dense layout (one 0/1 field per choice) can never satisfy —
// so the election accepted every envelope, voteCount rose, and the scrutinizer discarded them all.
// Every assertion an authoring test could make still passed; only the tally showed it, as zeros.
//
// So this test votes {1,0,1,0} over four choices and asserts the matrix that implies: the selected
// fields tallied, the unselected ones not.
func TestVotingProcessMultichoiceTally(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	choices := make([]db.Choice, 4)
	for i := range choices {
		choices[i] = db.Choice{Title: db.MultiLangString{"default": string(rune('A' + i))}, Value: uint32(i)}
	}
	req := &apicommon.CreateVotingProcessRequest{
		OrgAddress: orgAddress.Bytes(),
		Census: apicommon.CensusSpec{
			AuthFields:  db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname},
			TwoFaFields: db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail},
			MemberIDs:   ids,
			Weighted:    true,
		},
		Title:   db.MultiLangString{"default": "Multichoice tally"},
		EndDate: time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339),
		Questions: []apicommon.VotingProcessQuestionRequest{{
			Title:     db.MultiLangString{"default": "Pick up to two"},
			Choices:   choices,
			Type:      db.VotingTypeMultiChoice,
			TypeSetup: db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 2},
		}},
	}
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint)
	pid := created.ProcessID

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Questions, qt.HasLen, 1)
	election := got.Questions[0].UpstreamID
	c.Assert(election, qt.Not(qt.HasLen), 0)
	c.Assert(*got.Questions[0].BallotProtocol, qt.Equals, db.BallotProtocol{
		MaxCount: 4, MaxValue: 1, CostExponent: 1, MaxTotalCost: 2,
	})

	// what actually reached the chain: four 0/1 fields, and uniqueValues off. This is the #619
	// assertion at its only authoritative source — the election's own vote options.
	onChain, err := testNewVocdoniClient(t).Election(types.HexBytes(election))
	c.Assert(err, qt.IsNil)
	c.Assert(onChain.VoteMode.UniqueValues, qt.IsFalse, qt.Commentf("a dense layout with unique values takes no vote"))
	c.Assert(onChain.TallyMode.MaxCount, qt.Equals, uint32(4))
	c.Assert(onChain.TallyMode.MaxValue, qt.Equals, uint32(1))

	// one member authenticates, gets the CSP signature for this election and relays the ballot
	authToken := authProcessCSP(t, pid, &handlers.AuthRequest{
		Name: members[0].Name, Surname: members[0].Surname, Email: members[0].Email,
	})
	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)
	voterAddr := internal.HexBytes(voter.Address().Bytes())
	sign := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "",
		&handlers.SignRequest{AuthToken: authToken, ProcessID: election, Payload: hex.EncodeToString(voterAddr)},
		"processes", pid, "sign")
	c.Assert(sign.Signature, qt.Not(qt.HasLen), 0)

	nullifier := testRelayVoteRequest(t, &voter, election,
		testGenerateVoteProof(election, voterAddr, sign.Signature, 1), []byte(`{"votes":[1,0,1,0]}`), nil)
	c.Assert(nullifier, qt.Not(qt.HasLen), 0)

	// live results (the election is not secretUntilTheEnd, so no need to end it first)
	var res apicommon.VotingProcessResultsResponse
	for i := 0; i < 20; i++ {
		res = requestAndParse[apicommon.VotingProcessResultsResponse](
			t, http.MethodGet, "", nil, "processes", pid, "results")
		if len(res.Questions) > 0 && res.Questions[0].VoteCount > 0 {
			break
		}
		time.Sleep(time.Second)
	}
	c.Assert(res.Questions, qt.HasLen, 1)
	c.Assert(res.Questions[0].VoteCount, qt.Equals, uint64(1))
	// one row per choice, each [not selected, selected]: the ballot picked choices A and C. Before
	// the fix this was four rows of {"0","0"} — a counted vote that tallied to nothing.
	c.Assert(res.Questions[0].Results, qt.DeepEquals, [][]string{
		{"0", "1"}, {"1", "0"}, {"0", "1"}, {"1", "0"},
	})
}
