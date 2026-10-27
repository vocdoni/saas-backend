package api

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/statussync"
	"go.vocdoni.io/dvote/crypto/ethereum"
)

// TestBallotCounts drives the ballot-counts results processor end to end: a weighted CSP process with a
// singlechoice and a multichoice question gets one blank and one non-blank ballot per question, both
// questions are ended, and once the elections are final the reads serve the tally without counts (this
// API has no syncer). A local syncer with the ballot-counts processor then computes and persists them,
// and every read serves them next to the unchanged tally.
func TestBallotCounts(t *testing.T) {
	c := qt.New(t)

	token := testCreateUser(t, "superpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID) // plan grants weighted voting

	// two members, weights 1 (casts the blank ballots) and 3
	members := newOrgMembers(2)
	members[0].Weight = "1"
	members[1].Weight = "3"
	posted := postOrgMembers(t, token, orgAddress, members...)
	byNationalID := make(map[string]string, len(posted))
	for _, m := range posted {
		byNationalID[m.NationalID] = m.ID
	}
	for i := range members {
		members[i].ID = byNationalID[members[i].NationalID]
		c.Assert(members[i].ID, qt.Not(qt.Equals), "", qt.Commentf("member %d got no id", i))
	}

	req := minimalVotingProcessRequest(orgAddress)
	req.StartDate = ""
	req.Census = apicommon.CensusSpec{
		AuthFields: db.OrgMemberAuthFields{
			db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname, db.OrgMemberAuthFieldsMemberNumber,
		},
		TwoFaFields: db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail},
		MemberIDs:   memberIDs(members),
		Weighted:    true,
	}
	// Q1 singlechoice Yes(0)/No(1); Q2 multichoice over three choices, up to two selections
	req.Questions = append(req.Questions, apicommon.VotingProcessQuestionRequest{
		Title: db.MultiLangString{"default": "Q2"},
		Choices: []db.Choice{
			{Title: db.MultiLangString{"default": "A"}, Value: 0},
			{Title: db.MultiLangString{"default": "B"}, Value: 1},
			{Title: db.MultiLangString{"default": "C"}, Value: 2},
		},
		Type:      db.VotingTypeMultiChoice,
		TypeSetup: db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 2},
	})
	pid, elections := publishProcessRequest(t, token, req)
	c.Assert(elections, qt.HasLen, 2)

	// member 0 casts blank ballots, member 1 votes No on Q1 and A+C on Q2
	packages := [][]string{
		{`{"votes":[]}`, `{"votes":[0,0,0]}`},
		{`{"votes":[1]}`, `{"votes":[1,0,1]}`},
	}
	for i := range members {
		authToken := testCSPAuthenticateWithFields(t, pid, &handlers.AuthRequest{
			Name:         members[i].Name,
			Surname:      members[i].Surname,
			MemberNumber: members[i].MemberNumber,
			Email:        members[i].Email,
		})
		weight := new(big.Int).SetBytes(fetchCSPUserWeight(t, pid, authToken)).Uint64()
		c.Assert(weight, qt.Equals, uint64(1+2*i), qt.Commentf("member %d", i))
		voter := ethereum.SignKeys{}
		c.Assert(voter.Generate(), qt.IsNil)
		voterAddr := internal.HexBytes(voter.Address().Bytes())
		for q, election := range elections {
			signature := testCSPSign(t, pid, authToken, election, voterAddr)
			proof := testGenerateVoteProof(election, voterAddr, signature, weight)
			nullifier := testRelayVoteRequest(t, &voter, election, proof, []byte(packages[i][q]), nil)
			c.Assert(nullifier, qt.Not(qt.HasLen), 0)
		}
	}

	// end both questions and wait for their final results
	before := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(before.Questions, qt.HasLen, 2)
	for _, q := range before.Questions {
		job := enqueueAndPollJob(t, http.MethodPut, token,
			&apicommon.SetProcessStatusRequest{Status: "ended"}, "processes", pid, "questions", q.ID.Hex(), "status")
		c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("end error: %s", job.Errors))
	}
	for _, election := range elections {
		waitForElectionStatus(t, election, "ENDED", "RESULTS")
	}
	allFinal := func(info apicommon.VotingProcessResponse) bool {
		for _, q := range info.Questions {
			if q.Results == nil || !q.Results.FinalResults || q.Results.VoteCount != 2 {
				return false
			}
		}
		return true
	}
	var info apicommon.VotingProcessResponse
	for range 30 {
		info = requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
		if allFinal(info) {
			break
		}
		time.Sleep(time.Second)
	}
	c.Assert(allFinal(info), qt.IsTrue, qt.Commentf("questions never reached final results"))

	// the tally as the chain computes it: Q1 buckets [Yes, No]; Q2 one [notSelected, selected] row per
	// choice. Weights multiply: member 1 (weight 3) selected No, A and C; member 0 (weight 1) nothing.
	wantResults := [][][]string{
		{{"0", "3"}},
		{{"1", "3"}, {"4", "0"}, {"1", "3"}},
	}
	wantCounts := []*db.BallotCounts{
		{
			BlankVoteCount: 1, BlankVoteWeight: "1", InvalidVoteCount: 0, InvalidVoteWeight: "0",
			OptionVoteCounts: []uint64{0, 1}, OptionVoteWeights: []string{"0", "3"},
		},
		{
			BlankVoteCount: 1, BlankVoteWeight: "1", InvalidVoteCount: 0, InvalidVoteWeight: "0",
			OptionVoteCounts: []uint64{1, 0, 1}, OptionVoteWeights: []string{"3", "0", "3"},
		},
	}

	// without a syncer nothing computes the counts: the tally is served, the counts are absent
	res := requestAndParse[apicommon.VotingProcessResultsResponse](t, http.MethodGet, "", nil, "processes", pid, "results")
	c.Assert(res.Questions, qt.HasLen, 2)
	for i, q := range res.Questions {
		c.Assert(q.Results, qt.DeepEquals, wantResults[i])
		c.Assert(q.BallotCounts, qt.IsNil)
	}

	// a local syncer with the ballot-counts processor computes and persists them
	syncer := statussync.New(context.Background(), &statussync.Config{
		DB:                testDB,
		Account:           testAPI.account,
		ResultsProcessors: []statussync.ResultsProcessor{statussync.NewBallotCountsProcessor(testAPI.account)},
	})
	for _, election := range elections {
		syncer.EnqueueResults(election)
	}
	c.Assert(syncer.ProcessPending(), qt.Equals, 0, qt.Commentf("no status task was queued"))

	res = requestAndParse[apicommon.VotingProcessResultsResponse](t, http.MethodGet, "", nil, "processes", pid, "results")
	c.Assert(res.Questions, qt.HasLen, 2)
	for i, q := range res.Questions {
		c.Assert(q.Results, qt.DeepEquals, wantResults[i], qt.Commentf("question %d", i))
		c.Assert(q.BallotCounts, qt.DeepEquals, wantCounts[i], qt.Commentf("question %d", i))
	}

	info = requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(info.Questions, qt.HasLen, 2)
	for i, q := range info.Questions {
		c.Assert(q.Results, qt.Not(qt.IsNil))
		c.Assert(q.Results.Results, qt.DeepEquals, wantResults[i], qt.Commentf("question %d", i))
		c.Assert(q.Results.BallotCounts, qt.DeepEquals, wantCounts[i], qt.Commentf("question %d", i))

		pub := requestAndParse[apicommon.PublicQuestionResponse](
			t, http.MethodGet, "", nil, "processes", pid, "questions", q.ID.Hex())
		c.Assert(pub.Results, qt.Not(qt.IsNil))
		c.Assert(pub.Results.BallotCounts, qt.DeepEquals, wantCounts[i], qt.Commentf("public question %d", i))
	}

	// the counts reach the wire under the flattened field names
	raw, code := testRequest(t, http.MethodGet, "", nil, "processes", pid, "results")
	c.Assert(code, qt.Equals, http.StatusOK)
	c.Assert(string(raw), qt.Contains, fmt.Sprintf(`"blankVoteCount":%d`, 1))
	c.Assert(string(raw), qt.Contains, `"optionVoteWeights":["0","3"]`)
}
