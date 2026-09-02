package api

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"testing"
	"time"

	blind "github.com/vocdoni/go-blindsecp256k1"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/types"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// requestBlindPoint runs a single round-1 blind-point call for one election and returns its result.
func requestBlindPoint(t *testing.T, pid string, tok, election internal.HexBytes) handlers.BlindPointResult {
	t.Helper()
	point := requestAndParse[handlers.BlindPointResponse](t, http.MethodPost, "",
		&handlers.BlindPointRequest{AuthToken: tok, Elections: []internal.HexBytes{election}},
		"processes", pid, "blind-point")
	qt.Assert(t, point.Points, qt.HasLen, 1)
	return point.Points[0]
}

// testBlindSignBallot drives the two-round blind CSP flow for one election and returns the on-chain
// ProofCA the voter casts. It retries the ~1/256 case where the client's blinded message is not a
// valid blind-signature input (the CSP returns invalid_blinded_message without consuming the nonce,
// and blind-point is idempotent, so the same R is reused). The voter blinds hash(CAbundle) exactly
// as the Vochain verifier re-derives it (blind.Verify over HashRaw(marshal(bundle))).
func testBlindSignBallot(t *testing.T, pid string, tok, election, voterAddr internal.HexBytes) *models.Proof {
	t.Helper()
	c := qt.New(t)
	for range 40 {
		point := requestAndParse[handlers.BlindPointResponse](t, http.MethodPost, "",
			&handlers.BlindPointRequest{AuthToken: tok, Elections: []internal.HexBytes{election}},
			"processes", pid, "blind-point")
		c.Assert(point.Points, qt.HasLen, 1)
		pt := point.Points[0]
		c.Assert(pt.Code, qt.Equals, "", qt.Commentf("blind-point error: %s", pt.Error))
		signerR, err := blind.NewPointFromBytes(pt.TokenR)
		c.Assert(err, qt.IsNil)

		// build the CA bundle with the weight the CSP authorized in round 1, then blind its hash
		bundle := &models.CAbundle{ProcessId: election, Address: voterAddr, VoteWeight: pt.Weight}
		bundleBytes, err := proto.Marshal(bundle)
		c.Assert(err, qt.IsNil)
		m := new(big.Int).SetBytes(ethereum.HashRaw(bundleBytes))
		msgBlinded, userSecretData, err := blind.Blind(m, signerR)
		c.Assert(err, qt.IsNil)

		sign := requestAndParse[handlers.BlindSignResponse](t, http.MethodPost, "",
			&handlers.BlindSignRequest{AuthToken: tok, Ballots: []handlers.BlindSignBallot{
				{UpstreamID: election, BlindedMessage: msgBlinded.Bytes()},
			}}, "processes", pid, "blind-sign")
		c.Assert(sign.Signatures, qt.HasLen, 1)
		res := sign.Signatures[0]
		if res.Code == "invalid_blinded_message" {
			continue // re-blind against the same (idempotent) R
		}
		c.Assert(res.Code, qt.Equals, "", qt.Commentf("blind-sign error: %s", res.Error))
		signature, err := blind.Unblind(new(big.Int).SetBytes(res.Signature), userSecretData)
		c.Assert(err, qt.IsNil)
		return &models.Proof{Payload: &models.Proof_Ca{Ca: &models.ProofCA{
			Type:      models.ProofCA_ECDSA_BLIND_PIDSALTED,
			Signature: signature.BytesUncompressed(),
			Bundle:    bundle,
		}}}
	}
	c.Fatalf("blind sign did not produce a valid signature within the retry budget")
	return nil
}

// TestProcessBlindCSP exercises the blind (anonymous, OFF_CHAIN_CA_V2) CSP flow end to end: an
// anonymous process publishes with the V2 origin, the plain ECDSA sign endpoints are rejected, a
// voter completes the two-round blind protocol and casts an unlinkable vote on chain, the election
// is single-use, and sign-info omits the address/nullifier the CSP never learns.
func TestProcessBlindCSP(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID) // plan grants Features.Anonymous
	// two members so the voter (members[1]) carries weight 2 — newOrgMembers assigns weights 1,2 —
	// which lets the tally below prove the round-1-pinned weight reaches the weighted on-chain count.
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)
	voterMember := members[1] // weight 2

	// an anonymous, weighted census that authenticates by name+surname with an email OTP
	req := newVotingProcessRequest(orgAddress, ids)
	req.StartDate = ""
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname}
	req.Census.Anonymous = true
	req.Census.Weighted = true
	req.Questions = req.Questions[:1] // keep a single, everyone-eligible question
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint)
	pid := created.ProcessID

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))

	// the process read and the public per-question read both report the census as anonymous, so a
	// client learns which signing flow to use (fixes the PublicQuestionResponseFromDB omission).
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Census.Anonymous, qt.IsTrue)
	c.Assert(got.Questions, qt.HasLen, 1)
	election := got.Questions[0].UpstreamID
	qid := got.Questions[0].ID.Hex()
	pubQ := requestAndParse[apicommon.PublicQuestionResponse](t, http.MethodGet, "", nil,
		"processes", pid, "questions", qid)
	c.Assert(pubQ.Census.Anonymous, qt.IsTrue)

	// the election is published under the V2 census origin
	vc := testNewVocdoniClient(t)
	onchain, err := vc.Election(types.HexBytes(election))
	c.Assert(err, qt.IsNil)
	c.Assert(onchain.Census, qt.Not(qt.IsNil))
	c.Assert(onchain.Census.CensusOrigin, qt.Equals,
		models.CensusOrigin_name[int32(models.CensusOrigin_OFF_CHAIN_CA_V2)])

	tok := authProcessCSP(t, pid, &handlers.AuthRequest{
		Name: voterMember.Name, Surname: voterMember.Surname, Email: voterMember.Email,
	})

	// the plain ECDSA sign endpoints reject an anonymous census (client must use the blind flow)
	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, "",
		&handlers.SignRequest{AuthToken: tok, ProcessID: election, Payload: hex.EncodeToString(voter.Address().Bytes())},
		"processes", pid, "sign")
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, "",
		&handlers.SignBatchRequest{AuthToken: tok, Ballots: []handlers.SignBatchBallot{
			{UpstreamID: election, Address: voter.Address().Bytes()},
		}}, "processes", pid, "sign-batch")

	// two-round blind sign, then cast the unblinded proof on chain. The vote package must decode to
	// state.VotePackage{Votes []int} ({"votes":[N]}); a bare ["N"] array is accepted as an envelope
	// but tallies to zero weight. Value 0 is "Yes" in newVotingProcessRequest.
	proof := testBlindSignBallot(t, pid, tok, election, voter.Address().Bytes())
	nullifier := testCastVote(t, vc, &voter, election, proof, []byte(`{"votes":[0]}`))
	c.Assert(nullifier, qt.Not(qt.HasLen), 0)

	// the vote is not just accepted but counted correctly: the tally reflects one voter whose weight-2
	// "Yes" was carried unforgeably through the blind bundle (single field, buckets [value0, value1]).
	var res apicommon.VotingProcessResultsResponse
	for range 20 {
		res = requestAndParse[apicommon.VotingProcessResultsResponse](
			t, http.MethodGet, "", nil, "processes", pid, "results")
		if len(res.Questions) > 0 && res.Questions[0].VoteCount > 0 {
			break
		}
		time.Sleep(time.Second)
	}
	c.Assert(res.Questions, qt.HasLen, 1)
	t.Logf("blind tally: results=%v voteCount=%d (want results=[[2 0]] voteCount=1)",
		res.Questions[0].Results, res.Questions[0].VoteCount)
	c.Assert(res.Questions[0].VoteCount, qt.Equals, uint64(1))
	c.Assert(res.Questions[0].Results, qt.DeepEquals, [][]string{{"2", "0"}})

	// single-use: the election is consumed, so a fresh blind-point is refused
	point := requestAndParse[handlers.BlindPointResponse](t, http.MethodPost, "",
		&handlers.BlindPointRequest{AuthToken: tok, Elections: []internal.HexBytes{election}},
		"processes", pid, "blind-point")
	c.Assert(point.Points, qt.HasLen, 1)
	c.Assert(point.Points[0].Code, qt.Equals, "already_consumed")

	// sign-info reports the consumed question but omits the address/nullifier the CSP never saw
	signInfo := requestAndParse[handlers.ProcessSignInfoResponse](t, http.MethodPost, "",
		&handlers.ConsumedAddressRequest{AuthToken: tok}, "processes", pid, "sign-info")
	c.Assert(signInfo.Consumed, qt.HasLen, 1)
	c.Assert(signInfo.Consumed[0].Address, qt.HasLen, 0)
	c.Assert(signInfo.Consumed[0].Nullifier, qt.HasLen, 0)
}

// TestProcessBlindCSPWeightPin is the regression test for the round-1 weight pin: it arms a blind
// point at the member's weight, then changes the member's live weight, and asserts the re-armed
// blind-point and the blind-sign response still report the ORIGINAL weight — and that the vote signed
// under it verifies on chain and tallies at that weight, not the drifted one. Before the pin, a
// re-armed blind-point paired the sticky R with the live weight, so the client blinded a bundle the
// signature could never verify.
func TestProcessBlindCSPWeightPin(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)
	voterMember := members[1] // weight "2"

	req := newVotingProcessRequest(orgAddress, ids)
	req.StartDate = ""
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname}
	req.Census.Anonymous = true
	req.Census.Weighted = true
	req.Questions = req.Questions[:1]
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint)
	pid := created.ProcessID
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	election := got.Questions[0].UpstreamID

	vc := testNewVocdoniClient(t)
	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)
	tok := authProcessCSP(t, pid, &handlers.AuthRequest{
		Name: voterMember.Name, Surname: voterMember.Surname, Email: voterMember.Email,
	})

	weight2 := internal.HexBytes(big.NewInt(2).Bytes())

	// round 1 arms at the member's weight (2)
	pt1 := requestBlindPoint(t, pid, tok, election)
	c.Assert(pt1.Code, qt.Equals, "", qt.Commentf("blind-point error: %s", pt1.Error))
	c.Assert(pt1.Weight, qt.DeepEquals, weight2)

	// a manager changes the member's live weight to 9
	upd := voterMember
	upd.Phone = "" // a read-back member carries a trimmed phone hash; clear it before the update
	upd.Weight = "9"
	putOrgMember(t, token, orgAddress, upd)
	// the CSP now computes weight 9 live ...
	live := requestAndParse[handlers.UserWeightResponse](t, http.MethodPost, "",
		&handlers.UserWeightRequest{AuthToken: tok}, "processes", pid, "weight")
	c.Assert(live.Weight, qt.DeepEquals, internal.HexBytes(big.NewInt(9).Bytes()))

	// ... but a re-armed blind-point still pins the round-1 R and weight (2), not the live 9
	pt2 := requestBlindPoint(t, pid, tok, election)
	c.Assert(pt2.Code, qt.Equals, "", qt.Commentf("blind-point error: %s", pt2.Error))
	c.Assert(pt2.TokenR, qt.DeepEquals, pt1.TokenR)
	c.Assert(pt2.Weight, qt.DeepEquals, weight2)

	// round 2 signs under the pinned weight; the proof verifies on chain and tallies at 2, not 9
	proof := testBlindSignBallot(t, pid, tok, election, voter.Address().Bytes())
	c.Assert(proof.GetCa().Bundle.VoteWeight, qt.DeepEquals, []byte(weight2))
	nullifier := testCastVote(t, vc, &voter, election, proof, []byte(`{"votes":[0]}`))
	c.Assert(nullifier, qt.Not(qt.HasLen), 0)

	var res apicommon.VotingProcessResultsResponse
	for range 20 {
		res = requestAndParse[apicommon.VotingProcessResultsResponse](
			t, http.MethodGet, "", nil, "processes", pid, "results")
		if len(res.Questions) > 0 && res.Questions[0].VoteCount > 0 {
			break
		}
		time.Sleep(time.Second)
	}
	c.Assert(res.Questions, qt.HasLen, 1)
	t.Logf("weight-pin tally: results=%v voteCount=%d (want results=[[2 0]] — pinned weight 2, not live 9)",
		res.Questions[0].Results, res.Questions[0].VoteCount)
	c.Assert(res.Questions[0].Results, qt.DeepEquals, [][]string{{"2", "0"}})
}

// pollBlindResults reads the process results until the given question's tally matches want (or the
// budget runs out), then asserts it — used to wait for a live tally to settle after a cast.
func pollBlindResults(t *testing.T, pid string, want [][]string) apicommon.VotingProcessQuestionResults {
	t.Helper()
	var q apicommon.VotingProcessQuestionResults
	for range 20 {
		res := requestAndParse[apicommon.VotingProcessResultsResponse](
			t, http.MethodGet, "", nil, "processes", pid, "results")
		if len(res.Questions) > 0 {
			q = res.Questions[0]
			if fmt.Sprint(q.Results) == fmt.Sprint(want) {
				break
			}
		}
		time.Sleep(time.Second)
	}
	t.Logf("blind overwrite tally: results=%v voteCount=%d (want %v)", q.Results, q.VoteCount, want)
	qt.Assert(t, q.Results, qt.DeepEquals, want)
	return q
}

// TestProcessBlindCSPOverwrite proves a blind voter can overwrite their vote. Blind CSP issues
// exactly one signature per (user, election), but that ProofCA is a signature over the bundle only,
// so it is reusable: the voter re-casts on chain with the same proof and a different choice. The
// election must allow overwrites (MaxVoteOverwrites > 0), which a singlechoice question does not
// derive, so a raw ballot protocol enables it. The overwrite needs no second CSP call — and the CSP
// still refuses one.
func TestProcessBlindCSPOverwrite(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID) // grants Anonymous + Overwrite
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)
	voterMember := members[1] // weight 2

	req := newVotingProcessRequest(orgAddress, ids)
	req.StartDate = ""
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname}
	req.Census.Anonymous = true
	req.Census.Weighted = true
	req.Questions = req.Questions[:1]
	// a supplied ballot protocol wins over the named type, so this enables one on-chain overwrite on
	// the otherwise-singlechoice question (Yes=value 0, No=value 1).
	req.Questions[0].BallotProtocol = &db.BallotProtocol{MaxCount: 1, MaxValue: 1, MaxVoteOverwrites: 1}
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint)
	pid := created.ProcessID
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	election := got.Questions[0].UpstreamID

	vc := testNewVocdoniClient(t)
	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)
	tok := authProcessCSP(t, pid, &handlers.AuthRequest{
		Name: voterMember.Name, Surname: voterMember.Surname, Email: voterMember.Email,
	})

	// a single blind signature, reused for the vote and the overwrite
	proof := testBlindSignBallot(t, pid, tok, election, voter.Address().Bytes())

	// first vote: Yes (value 0) → weight-2 in bucket 0
	null1 := testCastVote(t, vc, &voter, election, proof, []byte(`{"votes":[0]}`))
	c.Assert(null1, qt.Not(qt.HasLen), 0)
	first := pollBlindResults(t, pid, [][]string{{"2", "0"}})
	c.Assert(first.VoteCount, qt.Equals, uint64(1))

	// overwrite: reuse the SAME proof, vote No (value 1) → the tally flips, still one voter
	null2 := testCastVote(t, vc, &voter, election, proof, []byte(`{"votes":[1]}`))
	c.Assert(null2, qt.DeepEquals, null1) // same voter address ⇒ same nullifier (an overwrite, not a new vote)
	second := pollBlindResults(t, pid, [][]string{{"0", "2"}})
	c.Assert(second.VoteCount, qt.Equals, uint64(1))

	// single-use holds: the CSP still refuses a second signature, even though the chain accepted the
	// overwrite (which reused the first proof rather than a new signature).
	c.Assert(requestBlindPoint(t, pid, tok, election).Code, qt.Equals, "already_consumed")
}
