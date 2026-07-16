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
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.vocdoni.io/dvote/crypto/ethereum"
)

// TestFullElectionLifecycle walks the complete organizer-and-voter flow end to end
// through the public/protected API exactly as a SaaS client would: it creates an
// organization (with eager on-chain account provisioning), adds members, builds and
// publishes a group census, creates and publishes a binary election, then has every
// member authenticate via the CSP and relay a vote. Finally it ends the election and
// asserts the on-chain tally returned by GET /process/{id}/results matches the votes
// that were cast.
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
	orgInfo := &apicommon.CreateOrganizationRequest{
		OrganizationInfo: apicommon.OrganizationInfo{
			Type:    string(db.CompanyType),
			Website: fmt.Sprintf("https://e2e-%d.com", internal.RandomInt(100000)),
		},
		ProvisionAccount: true,
	}
	org := requestAndParse[apicommon.OrganizationInfo](t, http.MethodPost, token, orgInfo, organizationsEndpoint)
	orgAddress := org.Address

	plans, err := testDB.Plans()
	c.Assert(err, qt.IsNil)
	c.Assert(len(plans) > 1, qt.IsTrue)
	setOrganizationSubscription(t, orgAddress, plans[1].ID)

	// --- members + group-based CSP census ---
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
	censusID, _, _ := createGroupBasedCensus(t, token, orgAddress, authFields, twoFaFields, memberIDs(members)...)

	// --- create the election draft via the API, then publish it on-chain ---
	electionParams := &db.ElectionParams{
		Title:         db.MultiLangString{"default": "Full lifecycle election"},
		EndDate:       time.Now().Add(2 * time.Hour),
		MaxCensusSize: 100,
		Questions: []db.Question{{
			Title: db.MultiLangString{"default": "Do you approve?"},
			Choices: []db.Choice{
				{Title: db.MultiLangString{"default": "No"}, Value: 0},
				{Title: db.MultiLangString{"default": "Yes"}, Value: 1},
			},
		}},
		VoteType:     db.VoteType{MaxCount: 1, MaxValue: 1},
		ElectionType: db.ElectionType{Autostart: true, Interruptible: true},
	}
	draftID := requestAndParse[primitive.ObjectID](t, http.MethodPost, token,
		&apicommon.CreateProcessRequest{OrgAddress: orgAddress, ElectionParams: electionParams},
		processCreateEndpoint)
	c.Assert(draftID.IsZero(), qt.IsFalse)

	pubJob := enqueueAndPollJob(t, http.MethodPost, token, nil, "process", draftID.Hex(), "publish")
	c.Assert(pubJob.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish error: %s", pubJob.Error))
	c.Assert(len(pubJob.Result.Address) > 0, qt.IsTrue)
	c.Assert(pubJob.Result.Status, qt.Equals, "READY")
	addr := pubJob.Result.Address

	// --- bundle links the census to the on-chain process for the CSP voter flow ---
	bundleID, _ := postProcessBundle(t, token, censusID, addr.Bytes())

	// --- each member authenticates with the CSP and relays a vote ---
	seenNullifiers := make(map[string]struct{}, numVoters)
	for i := range members {
		authToken := testCSPAuthenticateWithFields(t, bundleID, &handlers.AuthRequest{
			Name:         members[i].Name,
			Surname:      members[i].Surname,
			MemberNumber: members[i].MemberNumber,
			Email:        members[i].Email,
		})

		voter := ethereum.SignKeys{}
		c.Assert(voter.Generate(), qt.IsNil)
		voterAddr := internal.HexBytes(voter.Address().Bytes())
		signature := testCSPSign(t, bundleID, authToken, addr, voterAddr)
		proof := testGenerateVoteProof(addr, voterAddr, signature, 1)

		// the vote package must decode to state.VotePackage{Votes []int} ({"votes":[N]});
		// a bare ["N"] array is accepted as an envelope but tallies to zero weight.
		nullifier := testRelayVoteRequest(t, &voter, addr, proof,
			fmt.Appendf(nil, `{"votes":[%d]}`, votePlan[i]))
		c.Assert(nullifier, qt.Not(qt.HasLen), 0)
		_, dup := seenNullifiers[nullifier.String()]
		c.Assert(dup, qt.IsFalse, qt.Commentf("voter %d reused nullifier %s", i, nullifier.String()))
		seenNullifiers[nullifier.String()] = struct{}{}
	}

	// --- end the election; the chain auto-advances ENDED -> RESULTS once tallied ---
	endJob := enqueueAndPollJob(t, http.MethodPut, token,
		&apicommon.SetProcessStatusRequest{Status: "ended"}, "process", draftID.Hex(), "status")
	c.Assert(endJob.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("end error: %s", endJob.Error))
	c.Assert(endJob.Result.Status, qt.Equals, "ENDED")
	waitForElectionStatus(t, addr, "ENDED", "RESULTS")

	// --- fetch and verify the results are final and the tally is correct ---
	var res apicommon.ProcessResultsResponse
	for i := 0; i < 20; i++ {
		res = requestAndParse[apicommon.ProcessResultsResponse](
			t, http.MethodGet, "", nil, "process", draftID.Hex(), "results")
		if res.FinalResults && res.VoteCount == uint64(numVoters) {
			break
		}
		time.Sleep(time.Second)
	}
	c.Assert(res.VoteCount, qt.Equals, uint64(numVoters))
	c.Assert(res.FinalResults, qt.IsTrue)
	c.Assert(res.Status, qt.Not(qt.HasLen), 0)

	// the tally is Results[question][value]; with a binary vote type question 0 has two
	// buckets. votePlan {1,1,0} => two "Yes" (value 1) and one "No" (value 0).
	c.Assert(res.Results, qt.HasLen, 1)
	c.Assert(res.Results[0], qt.HasLen, 2)
	c.Assert(res.Results[0][0], qt.Equals, "1") // value 0 ("No")
	c.Assert(res.Results[0][1], qt.Equals, "2") // value 1 ("Yes")
}
