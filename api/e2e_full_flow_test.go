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
)

// TestFullElectionLifecycle walks the complete organizer-and-voter flow end to end
// through the public/protected API exactly as a SaaS client would: it creates an
// organization (with eager on-chain account provisioning), adds members, creates and
// publishes a process with a binary question over them, then has every member
// authenticate via the CSP and relay a vote. Finally it ends the election and asserts
// the on-chain tally surfaced by the /processes reads matches the votes that were cast.
func TestFullElectionLifecycle(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()

	// 3 voters, unit weights, one binary question — the smallest setup that
	// still proves a non-trivial tally (a bucket with 2 and a bucket with 1). The vote
	// plan and expected buckets are derived from this slice, so extend it to scale up.
	votePlan := []int{1, 1, 0}
	numVoters := len(votePlan)

	// --- organizer: user, org with on-chain account, plan subscription ---
	token := testCreateUser(t, "superpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	// --- members ---
	members := newOrgMembers(numVoters)
	for i := range members {
		// unit weights keep the tally equal to the vote counts regardless of the
		// census weighting mode (the proof weight must match the CSP-signed weight).
		members[i].Weight = "1"
	}
	posted := postOrgMembers(t, token, orgAddress, members...)
	byNationalID := make(map[string]string, len(posted))
	for _, m := range posted {
		byNationalID[m.NationalID] = m.ID
	}
	for i := range members {
		members[i].ID = byNationalID[members[i].NationalID]
		c.Assert(members[i].ID, qt.Not(qt.Equals), "", qt.Commentf("member %d got no id", i))
	}

	authFields := db.OrgMemberAuthFields{
		db.OrgMemberAuthFieldsName,
		db.OrgMemberAuthFieldsSurname,
		db.OrgMemberAuthFieldsMemberNumber,
	}
	twoFaFields := db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}

	// --- create and publish the process: the binary question plus a second one left READY ---
	req := minimalVotingProcessRequest(orgAddress)
	req.StartDate = ""
	req.Census = apicommon.CensusSpec{AuthFields: authFields, TwoFaFields: twoFaFields, MemberIDs: memberIDs(members)}
	req.Questions[0].Title = db.MultiLangString{"default": "Do you approve?"}
	req.Questions[0].Choices = []db.Choice{
		{Title: db.MultiLangString{"default": "No"}, Value: 0},
		{Title: db.MultiLangString{"default": "Yes"}, Value: 1},
	}
	req.Questions = append(req.Questions, req.Questions[0])
	pid, elections := publishProcessRequest(t, token, req)
	addr := elections[0]

	// --- each member authenticates with the CSP and relays a vote ---
	seenNullifiers := make(map[string]struct{}, numVoters)
	for i := range members {
		authToken := testCSPAuthenticateWithFields(t, pid, &handlers.AuthRequest{
			Name:         members[i].Name,
			Surname:      members[i].Surname,
			MemberNumber: members[i].MemberNumber,
			Email:        members[i].Email,
		})

		voter := ethereum.SignKeys{}
		c.Assert(voter.Generate(), qt.IsNil)
		voterAddr := internal.HexBytes(voter.Address().Bytes())
		signature := testCSPSign(t, pid, authToken, addr, voterAddr)
		proof := testGenerateVoteProof(addr, voterAddr, signature, 1)

		// the vote package must decode to state.VotePackage{Votes []int} ({"votes":[N]});
		// a bare ["N"] array is accepted as an envelope but tallies to zero weight.
		nullifier := testRelayVoteRequest(t, &voter, addr, proof,
			fmt.Appendf(nil, `{"votes":[%d]}`, votePlan[i]), nil)
		c.Assert(nullifier, qt.Not(qt.HasLen), 0)
		_, dup := seenNullifiers[nullifier.String()]
		c.Assert(dup, qt.IsFalse, qt.Commentf("voter %d reused nullifier %s", i, nullifier.String()))
		seenNullifiers[nullifier.String()] = struct{}{}
	}

	// --- end the first question's election; the chain auto-advances ENDED -> RESULTS once tallied ---
	before := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	qID := before.Questions[0].ID.Hex()
	endJob := enqueueAndPollJob(t, http.MethodPut, token,
		&apicommon.SetProcessStatusRequest{Status: "ended"}, "processes", pid, "questions", qID, "status")
	c.Assert(endJob.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("end error: %s", endJob.Errors))
	waitForElectionStatus(t, addr, "ENDED", "RESULTS")

	// --- manager GET /processes/{id}: the ended question carries the final tally inline ---
	var info apicommon.VotingProcessResponse
	for i := 0; i < 20; i++ {
		info = requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
		if qr := info.Questions[0].Results; qr != nil && qr.FinalResults && qr.VoteCount == uint64(numVoters) {
			break
		}
		time.Sleep(time.Second)
	}
	c.Assert(info.Questions, qt.HasLen, 2)
	qr := info.Questions[0].Results
	c.Assert(qr, qt.Not(qt.IsNil))
	c.Assert(qr.VoteCount, qt.Equals, uint64(numVoters))
	c.Assert(qr.FinalResults, qt.IsTrue)
	// singlechoice question -> one ballot field -> one results row of value buckets [No, Yes].
	// votePlan {1,1,0} => one "No" (value 0) and two "Yes" (value 1).
	c.Assert(qr.Results, qt.DeepEquals, [][]string{{"1", "2"}})
	// the second question was never voted nor ended
	c.Assert(info.Questions[1].Results, qt.Not(qt.IsNil))
	c.Assert(info.Questions[1].Results.FinalResults, qt.IsFalse)

	// public GET /processes/{id}/questions/{qId}: same tally on the voter-facing read.
	pub := requestAndParse[apicommon.PublicQuestionResponse](
		t, http.MethodGet, "", nil, "processes", pid, "questions", qID)
	c.Assert(pub.Results, qt.Not(qt.IsNil))
	c.Assert(pub.Results.VoteCount, qt.Equals, uint64(numVoters))
	c.Assert(pub.Results.Results, qt.DeepEquals, [][]string{{"1", "2"}})
}
