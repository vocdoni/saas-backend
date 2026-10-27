package api

import (
	"encoding/hex"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/csp/signers/electionkey"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/apiclient"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/types"
	"go.vocdoni.io/dvote/util"
	"go.vocdoni.io/proto/build/go/models"
)

// twoQuestionCSPRequest returns a draft request over every member of the organization, with an
// email-OTP census authenticated by name and surname and two yes/no questions open to everyone.
func twoQuestionCSPRequest(orgAddress common.Address, members []apicommon.OrgMember,
) *apicommon.CreateVotingProcessRequest {
	req := newVotingProcessRequest(orgAddress, memberIDs(members))
	req.StartDate = ""
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname}
	second := req.Questions[0]
	second.Title = db.MultiLangString{"default": "Q2"}
	req.Questions = []apicommon.VotingProcessQuestionRequest{req.Questions[0], second}
	return req
}

// loadProcess returns a voting process, its questions and its census as stored.
func loadProcess(c *qt.C, pid string) (*db.VotingProcess, []db.VotingProcessQuestion, *db.Census) {
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	vp, questions, err := testDB.ProcessWithQuestions(oid)
	c.Assert(err, qt.IsNil)
	census, err := testDB.Census(vp.CensusID.Hex())
	c.Assert(err, qt.IsNil)
	return vp, questions, census
}

// TestCSPKeyPerElectionPublish publishes a two-question ECDSA CSP process and checks that each
// question's election uses OFF_CHAIN_CA_V3_ECDSA with its own census root, that the question
// records both, that the election ids are the ones the organization's election index predicts,
// and that ballots signed in one batch carry the ECDSA proof type and vote.
func TestCSPKeyPerElectionPublish(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(1)...)

	pid, elections := publishProcessRequest(t, token, twoQuestionCSPRequest(orgAddress, members))
	vp, questions, census := loadProcess(c, pid)
	c.Assert(questions, qt.HasLen, 2)
	// the census is published without a root: each question's election has its own
	c.Assert(census.Published.CreatedAt.IsZero(), qt.IsFalse)
	c.Assert(census.Published.Root, qt.HasLen, 0)

	index, err := testAPI.account.AccountElectionIndex(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(index, qt.Equals, uint32(2))

	vc := testNewVocdoniClient(t)
	for i := range questions {
		q := &questions[i]
		_, wantRoot, err := testCSP.ElectionCensus(q.ID, false)
		c.Assert(err, qt.IsNil)
		c.Assert(q.UpstreamID, qt.DeepEquals, elections[i])
		c.Assert(q.CensusOrigin, qt.Equals, electionkey.OriginECDSAName)
		c.Assert(q.CensusRoot, qt.DeepEquals, wantRoot)

		onchain, err := vc.Election(types.HexBytes(q.UpstreamID))
		c.Assert(err, qt.IsNil)
		c.Assert(onchain.Census, qt.Not(qt.IsNil))
		c.Assert(onchain.Census.CensusOrigin, qt.Equals, electionkey.OriginECDSAName)
		c.Assert([]byte(onchain.Census.CensusRoot), qt.DeepEquals, []byte(wantRoot))

		ep, err := electionParamsForQuestion(vp, q, census)
		c.Assert(err, qt.IsNil)
		id, err := testAPI.account.ElectionID(&account.ElectionIDParams{
			OrgAddress: orgAddress, CensusOrigin: electionkey.OriginECDSA, Params: ep, Index: uint32(i),
		})
		c.Assert(err, qt.IsNil)
		c.Assert(id, qt.DeepEquals, q.UpstreamID)
	}
	c.Assert(questions[0].CensusRoot, qt.Not(qt.DeepEquals), questions[1].CensusRoot)

	// one batch signs both ballots with each election's key, and both votes are accepted
	tok := authProcessCSP(t, pid, &handlers.AuthRequest{
		Name: members[0].Name, Surname: members[0].Surname, Email: members[0].Email,
	})
	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)
	address := internal.HexBytes(voter.Address().Bytes())
	batch := requestAndParse[handlers.SignBatchResponse](t, http.MethodPost, "", &handlers.SignBatchRequest{
		AuthToken: tok, Ballots: []handlers.SignBatchBallot{
			{UpstreamID: elections[0], Address: address},
			{UpstreamID: elections[1], Address: address},
		},
	}, "processes", pid, "sign-batch")
	c.Assert(batch.Signatures, qt.HasLen, 2)
	for i, res := range batch.Signatures {
		c.Assert(res.Code, qt.Equals, "", qt.Commentf("sign error: %s", res.Error))
		c.Assert(res.ProofType, qt.Equals, models.ProofCA_ECDSA.String())
		c.Assert(res.WeightCertificate, qt.HasLen, 0)
		proof := testGenerateVoteProof(elections[i], address, res.Signature, 1)
		testCastVote(t, vc, &voter, elections[i], proof, []byte(`{"votes":[0]}`))
		count, err := electionVoteCount(vc, elections[i])
		c.Assert(err, qt.IsNil)
		c.Assert(count, qt.Equals, uint64(1))
	}
}

// TestPublishAdoptsOrphanElection creates the election of one question of a draft process the way a
// publish that lost its batch response would have left it, then publishes the process: the publish
// records that election for the question instead of creating a second one.
func TestPublishAdoptsOrphanElection(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(1)...)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](t, http.MethodPost, token,
		twoQuestionCSPRequest(orgAddress, members), processesCreateEndpoint)
	pid := created.ProcessID

	// the election of question 1 is on chain, but no publish recorded it
	vp, questions, census := loadProcess(c, pid)
	orphan := &questions[1]
	ep, err := electionParamsForQuestion(vp, orphan, census)
	c.Assert(err, qt.IsNil)
	origin, root, err := testCSP.ElectionCensus(orphan.ID, false)
	c.Assert(err, qt.IsNil)
	tx, err := testAPI.account.BuildNewProcessTx(&account.NewProcessParams{
		OrgAddress: orgAddress, Params: ep, CensusRoot: root, CensusURI: testAPI.serverURL, CensusOrigin: origin,
	})
	c.Assert(err, qt.IsNil)
	vc := testNewVocdoniClient(t)
	orphanElection := internal.HexBytes(signAsOrgAndSendVocdoniTx(t, tx, orgAddress, vc))
	c.Assert(orphanElection, qt.HasLen, types.ProcessIDsize)

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))

	_, questions, _ = loadProcess(c, pid)
	c.Assert(questions[1].UpstreamID, qt.DeepEquals, orphanElection)
	c.Assert(questions[0].UpstreamID, qt.HasLen, types.ProcessIDsize)
	c.Assert(questions[0].UpstreamID, qt.Not(qt.DeepEquals), orphanElection)
	index, err := testAPI.account.AccountElectionIndex(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(index, qt.Equals, uint32(2), qt.Commentf("the publish created a duplicate election"))
}

// signAsOrgAndSubmitVocdoniTx funds tx, signs it as the organization and submits it, returning
// the submit error of the chain.
func signAsOrgAndSubmitVocdoniTx(t *testing.T, tx *models.Tx, orgAddress common.Address,
	vocdoniClient *apiclient.HTTPclient,
) error {
	t.Helper()
	c := qt.New(t)
	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	signer, err := account.OrganizationSigner(testSecret, org.SignerSeedValue(), org.Nonce)
	c.Assert(err, qt.IsNil)
	tx, _, err = testAPI.account.FundTransaction(tx, signer.Address())
	c.Assert(err, qt.IsNil)
	stx, err := testAPI.account.SignTransaction(tx, signer)
	c.Assert(err, qt.IsNil)
	_, _, err = vocdoniClient.SendTx(stx)
	return err
}

// TestCSPLegacyPublishRejected builds the election of a question the way a saas-backend without
// per-election CSP keys publishes it, on the legacy OFF_CHAIN_CA (ECDSA) and OFF_CHAIN_CA_V2
// (blind) census origins with the CSP's salted root keys: the chain rejects both, and the
// organization gets no election.
func TestCSPLegacyPublishRejected(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(1)...)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](t, http.MethodPost, token,
		twoQuestionCSPRequest(orgAddress, members), processesCreateEndpoint)
	vp, questions, census := loadProcess(c, created.ProcessID)
	ep, err := electionParamsForQuestion(vp, &questions[0], census)
	c.Assert(err, qt.IsNil)

	ecdsaRoot, err := testCSP.PubKey()
	c.Assert(err, qt.IsNil)
	blindRoot, err := testCSP.BlindPubKey()
	c.Assert(err, qt.IsNil)
	vc := testNewVocdoniClient(t)
	for origin, root := range map[models.CensusOrigin]internal.HexBytes{
		models.CensusOrigin_OFF_CHAIN_CA:    ecdsaRoot,
		models.CensusOrigin_OFF_CHAIN_CA_V2: blindRoot,
	} {
		tx, err := testAPI.account.BuildNewProcessTx(&account.NewProcessParams{
			OrgAddress: orgAddress, Params: ep, CensusRoot: root, CensusURI: testAPI.serverURL, CensusOrigin: origin,
		})
		c.Assert(err, qt.IsNil)
		err = signAsOrgAndSubmitVocdoniTx(t, tx, orgAddress, vc)
		c.Assert(err, qt.ErrorMatches, `(?s).*census origin `+origin.String()+` retired for new processes.*`)
	}
	index, err := testAPI.account.AccountElectionIndex(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(index, qt.Equals, uint32(0))
}

// markLegacyPublished records a single-question draft process as published on election, with the
// empty census origin of a question published on a legacy CSP origin.
func markLegacyPublished(c *qt.C, pid string, election internal.HexBytes) {
	vp, questions, _ := loadProcess(c, pid)
	c.Assert(questions, qt.HasLen, 1)
	c.Assert(testDB.MarkVotingProcessMined(vp.ID), qt.IsNil)
	c.Assert(testDB.SetQuestionPublished(db.QuestionPublishedParams{
		ID: questions[0].ID, UpstreamID: election, Status: db.QuestionStatusReady,
	}), qt.IsNil)
	c.Assert(testDB.SetVotingProcessPublished(vp.ID, "", time.Now()), qt.IsNil)
}

// TestCSPLegacyQuestionsSign covers the questions published on a legacy CSP origin, stored with an
// empty census origin: an ECDSA question signs with the OFF_CHAIN_CA salted proof type and a blind
// one with the OFF_CHAIN_CA_V2 one. test/sequencer/legacycsp checks that these signatures verify
// with the vochain-sequencer verifiers of those origins.
func TestCSPLegacyQuestionsSign(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(1)...)
	draft := func(anonymous bool) string {
		req := twoQuestionCSPRequest(orgAddress, members)
		req.Questions = req.Questions[:1]
		req.Census.Anonymous = anonymous
		return requestAndParse[apicommon.CreateVotingProcessResponse](
			t, http.MethodPost, token, req, processesCreateEndpoint).ProcessID
	}
	ecdsaPid, blindPid := draft(false), draft(true)
	ecdsaElection := internal.HexBytes(util.RandomBytes(types.ProcessIDsize))
	blindElection := internal.HexBytes(util.RandomBytes(types.ProcessIDsize))
	markLegacyPublished(c, ecdsaPid, ecdsaElection)
	markLegacyPublished(c, blindPid, blindElection)

	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)
	address := internal.HexBytes(voter.Address().Bytes())
	authReq := &handlers.AuthRequest{Name: members[0].Name, Surname: members[0].Surname, Email: members[0].Email}

	ecdsaTok := authProcessCSP(t, ecdsaPid, authReq)
	signature := testCSPSignProof(t, ecdsaPid, &handlers.SignRequest{
		AuthToken: ecdsaTok, ProcessID: ecdsaElection, Payload: hex.EncodeToString(address),
	}, models.ProofCA_ECDSA_PIDSALTED)
	c.Assert(signature, qt.HasLen, 65)

	blindTok := authProcessCSP(t, blindPid, authReq)
	// testBlindSignProof also checks that the response carries no weight certificate
	blindProof := testBlindSignProof(t, blindPid, blindTok, blindElection, address, models.ProofCA_ECDSA_BLIND_PIDSALTED)
	c.Assert(blindProof.GetCa().GetSignature(), qt.Not(qt.HasLen), 0)
}
