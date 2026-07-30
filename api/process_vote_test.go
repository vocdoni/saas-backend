package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/apiclient"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/types"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// waitForElectionStatus polls the chain until the election reports one of the
// accepted status strings (e.g. "PAUSED") or the retries are exhausted. Several
// values are accepted because an ended election is auto-advanced to "RESULTS"
// by the chain once its results are tallied.
func waitForElectionStatus(t *testing.T, address internal.HexBytes, accepted ...string) {
	t.Helper()
	c := qt.New(t)
	client := testNewVocdoniClient(t)
	var lastStatus string
	for i := 0; i < 20; i++ {
		election, err := client.Election(types.HexBytes(address))
		if err == nil {
			lastStatus = election.Status
			for _, s := range accepted {
				if lastStatus == s {
					return
				}
			}
		}
		time.Sleep(time.Second)
	}
	c.Fatalf("election %s never reached status %v (last seen %q)", address.String(), accepted, lastStatus)
}

// TestProcessStatusLifecycle publishes a draft as an on-chain election and then
// drives it through paused → ready → ended via the status endpoint, asserting the
// chain reflects each transition.
func TestProcessStatusLifecycle(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "password123")

	// create an org with eager on-chain account provisioning
	orgInfo := &apicommon.CreateOrganizationRequest{
		OrganizationInfo: apicommon.OrganizationInfo{
			Type:    string(db.CompanyType),
			Website: fmt.Sprintf("https://status-%d.com", internal.RandomInt(100000)),
		},
		ProvisionAccount: true,
	}
	org := requestAndParse[apicommon.OrganizationInfo](t, http.MethodPost, token, orgInfo, organizationsEndpoint)
	orgAddress := org.Address
	c.Assert(orgAddress, qt.Not(qt.Equals), common.Address{})

	// subscribe to a plan so NEW_PROCESS permission passes
	plans, err := testDB.Plans()
	c.Assert(err, qt.IsNil)
	c.Assert(len(plans) > 1, qt.IsTrue)
	c.Assert(testDB.SetOrganizationSubscription(orgAddress, &db.OrganizationSubscription{
		PlanID:          plans[1].ID,
		StartDate:       time.Now(),
		RenewalDate:     time.Now().Add(24 * time.Hour),
		LastPaymentDate: time.Now(),
		Active:          true,
	}), qt.IsNil)

	// seed a draft with election params (interruptible so the status can change)
	draftID, err := testDB.SetProcess(&db.Process{
		OrgAddress: orgAddress,
		ElectionParams: &db.ElectionParams{
			Title:         db.MultiLangString{"default": "Status lifecycle election"},
			EndDate:       time.Now().Add(2 * time.Hour),
			MaxCensusSize: 100,
			Questions: []db.Question{{
				Title: db.MultiLangString{"default": "Question 1"},
				Choices: []db.Choice{
					{Title: db.MultiLangString{"default": "Yes"}, Value: 0},
					{Title: db.MultiLangString{"default": "No"}, Value: 1},
				},
			}},
			VoteType:     db.VoteType{MaxCount: 1, MaxValue: 1},
			ElectionType: db.ElectionType{Autostart: true, Interruptible: true},
		},
	})
	c.Assert(err, qt.IsNil)
	c.Assert(draftID.IsZero(), qt.IsFalse)

	// publish (async) to obtain the on-chain process address
	pubJob := enqueueAndPollJob(t, http.MethodPost, token, nil, "process", draftID.Hex(), "publish")
	c.Assert(pubJob.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("error: %s", pubJob.Errors))
	c.Assert(len(pubJob.Result.Address) > 0, qt.IsTrue)
	c.Assert(pubJob.Result.Status, qt.Equals, "READY")
	addr := pubJob.Result.Address

	// drive the status transitions and assert the chain reflects each change. An
	// ended election is auto-advanced to "RESULTS" by the chain once tallied, so
	// the terminal transition accepts either.
	transitions := []struct {
		request     string
		respStatus  string
		chainStatus []string
	}{
		{"paused", "PAUSED", []string{"PAUSED"}},
		{"ready", "READY", []string{"READY"}},
		{"ended", "ENDED", []string{"ENDED", "RESULTS"}},
	}
	for _, tr := range transitions {
		job := enqueueAndPollJob(t, http.MethodPut, token,
			&apicommon.SetProcessStatusRequest{Status: tr.request}, "process", draftID.Hex(), "status")
		c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("error: %s", job.Errors))
		c.Assert(job.Result.Status, qt.Equals, tr.respStatus)
		waitForElectionStatus(t, addr, tr.chainStatus...)
	}
}

// testSignVoteTx builds a vote envelope for processID and signs it as the voter would,
// returning the marshaled models.SignedTx the relay endpoints take as their payload.
func testSignVoteTx(t *testing.T, signer *ethereum.SignKeys, processID internal.HexBytes,
	proof *models.Proof, votePackage []byte,
) internal.HexBytes {
	t.Helper()
	c := qt.New(t)
	tx := &models.Tx{Payload: &models.Tx_Vote{Vote: &models.VoteEnvelope{
		ProcessId: processID.Bytes(), Nonce: internal.RandomBytes(16), Proof: proof, VotePackage: votePackage,
	}}}
	txBytes, err := proto.Marshal(tx)
	c.Assert(err, qt.IsNil)
	// the voter signs with the chain id (same as signAndSendVocdoniTx uses)
	signature, err := signer.SignVocdoniTx(txBytes, fetchVocdoniChainID(t, testNewVocdoniClient(t)))
	c.Assert(err, qt.IsNil)
	stx, err := proto.Marshal(&models.SignedTx{Tx: txBytes, Signature: signature})
	c.Assert(err, qt.IsNil)
	return stx
}

// testRelayVoteRequest signs a vote tx, wraps it as a SignedTx, posts it to
// POST /vote, and returns the relayed vote nullifier.
func testRelayVoteRequest(t *testing.T, signer *ethereum.SignKeys, processID internal.HexBytes,
	proof *models.Proof, votePackage []byte,
) internal.HexBytes {
	t.Helper()
	c := qt.New(t)
	stx := testSignVoteTx(t, signer, processID, proof, votePackage)
	job := enqueueAndPollJob(t, http.MethodPost, "",
		&apicommon.RelayVoteRequest{TxPayload: stx}, "vote")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("error: %s", job.Errors))
	c.Assert(job.Result.VoteID, qt.Not(qt.HasLen), 0)
	// the nullifier is derived from the envelope before it is submitted, so it is on the
	// job regardless of what the chain replied, and it must match the chain's voteID.
	c.Assert(job.Result.Nullifier, qt.DeepEquals, job.Result.VoteID)
	c.Assert(job.Result.ProcessID, qt.DeepEquals, processID)
	return job.Result.VoteID
}

// relayVotingFixture is a voter authenticated against a CSP bundle covering one or more
// on-chain processes, i.e. everything the relay endpoints need to accept a real vote.
type relayVotingFixture struct {
	token      string
	client     *apiclient.HTTPclient
	orgAddress common.Address
	processIDs []internal.HexBytes
	bundleID   string
	authToken  internal.HexBytes
	voter      *ethereum.SignKeys
}

// proofFor CSP-signs the voter's address for one of the fixture's processes and builds the
// vote proof from it. The CSP consumes a process per user, not a bundle, so the same auth
// token signs once for each process — which is exactly what a multi-question vote does.
func (f *relayVotingFixture) proofFor(t *testing.T, processID internal.HexBytes) *models.Proof {
	t.Helper()
	voterAddr := f.voter.Address().Bytes()
	signature := testCSPSign(t, f.bundleID, f.authToken, processID, voterAddr)
	return testGenerateVoteProof(processID, voterAddr, signature, 1)
}

// setupRelayVoting builds the full CSP voting setup shared by the relay tests: an
// organization with an on-chain account and a plan, processes many on-chain elections
// whose census root is the CSP, a published group census bundled with them, and a voter
// authenticated against that bundle.
func setupRelayVoting(t *testing.T, processes int) *relayVotingFixture {
	t.Helper()
	c := qt.New(t)

	// create a user and organization
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
	orgName := fmt.Sprintf("relayorg-%d", internal.RandomInt(1000))
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

	// create the on-chain processes, whose census root is the CSP public key
	cspPubKey, err := testCSP.PubKey()
	c.Assert(err, qt.IsNil)
	processIDs := make([]internal.HexBytes, processes)
	rawProcessIDs := make([][]byte, processes)
	for i := range processIDs {
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
		rawProcessIDs[i] = signRemoteSignerAndSendVocdoniTx(t, processTx, token, vocdoniClient, orgAddress)
		processIDs[i] = internal.HexBytes(rawProcessIDs[i])
		t.Logf("Created process with ID: %x", rawProcessIDs[i])

		// the relay handler requires the process to be known by its on-chain address
		_, err = testDB.SetProcess(&db.Process{OrgAddress: orgAddress, Address: processIDs[i]})
		c.Assert(err, qt.IsNil)
	}

	// create a census, add members and publish a group-based census
	authFields := db.OrgMemberAuthFields{
		db.OrgMemberAuthFieldsName,
		db.OrgMemberAuthFieldsSurname,
		db.OrgMemberAuthFieldsMemberNumber,
	}
	twoFaFields := db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}

	suffix := internal.RandomInt(1000000)
	members := []apicommon.OrgMember{{
		Name:         "Relay",
		Surname:      "Voter",
		MemberNumber: fmt.Sprintf("R%06d", suffix),
		NationalID:   fmt.Sprintf("RELAY%05dA", suffix),
		BirthDate:    "1990-01-01",
		Email:        fmt.Sprintf("relay.voter.%d@example.com", suffix),
		Phone:        "+34699000001",
		Weight:       "1",
	}}
	postedOrgMembers := postOrgMembers(t, token, orgAddress, members...)
	idMap := make(map[string]string, len(postedOrgMembers))
	for _, m := range postedOrgMembers {
		idMap[m.NationalID] = m.ID
	}
	for i := range members {
		members[i].ID = idMap[members[i].NationalID]
	}

	group := postGroup(t, token, orgAddress, memberIDs(members)...)
	censusID := postCensus(t, token, orgAddress, authFields, twoFaFields)
	requestAndParse[apicommon.PublishedCensusResponse](
		t, http.MethodPost, token, &apicommon.PublishCensusGroupRequest{
			AuthFields:  authFields,
			TwoFaFields: twoFaFields,
		}, "census", censusID, "group", group.ID, "publish")

	// create a bundle linking the census and every process
	bundleID, _ := postProcessBundle(t, token, censusID, rawProcessIDs...)

	// authenticate the voter with the CSP
	authToken := testCSPAuthenticateWithFields(t, bundleID, &handlers.AuthRequest{
		Name:         members[0].Name,
		Surname:      members[0].Surname,
		MemberNumber: members[0].MemberNumber,
		Email:        members[0].Email,
	})

	voter := &ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)

	return &relayVotingFixture{
		token:      token,
		client:     vocdoniClient,
		orgAddress: orgAddress,
		processIDs: processIDs,
		bundleID:   bundleID,
		authToken:  authToken,
		voter:      voter,
	}
}

// TestRelayVote casts a vote via the public relay endpoint instead of submitting it
// directly to the chain, asserting the vote is counted and a nullifier is returned.
func TestRelayVote(t *testing.T) {
	c := qt.New(t)
	f := setupRelayVoting(t, 1)
	processID := f.processIDs[0]
	proof := f.proofFor(t, processID)

	// relay the vote and assert the chain counted it
	votesBefore, err := f.client.ElectionVoteCount(processID.Bytes())
	c.Assert(err, qt.IsNil)

	nullifier := testRelayVoteRequest(t, f.voter, processID, proof, []byte("[\"1\"]"))
	c.Assert(nullifier, qt.Not(qt.HasLen), 0)

	votesAfter, err := f.client.ElectionVoteCount(processID.Bytes())
	c.Assert(err, qt.IsNil)
	c.Assert(votesAfter, qt.Equals, votesBefore+1, qt.Commentf("expected 1 more vote, got %d", votesAfter))

	// a chain-accepted relay meters the owning organization's SentVotes counter
	orgAfter, err := testDB.Organization(f.orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(orgAfter.Counters.SentVotes, qt.Equals, 1)

	// the voter can now verify that nullifier against the chain: an unknown one is reported
	// as unverified rather than failing the call, a repeated one gets the same answer
	// (looked up only once), and a short one is accepted — anonymous (ZK) nullifiers are
	// minimal big-endian field elements, so they may be under 32 bytes
	unknown := internal.HexBytes(internal.RandomBytes(nullifierSize))
	short := internal.HexBytes{0xde, 0xad}
	verified := requestAndParse[apicommon.VerifyVotesResponse](t, http.MethodPost, "",
		&apicommon.VerifyVotesRequest{Nullifiers: []internal.HexBytes{nullifier, unknown, nullifier, short}},
		"votes", "verify")
	c.Assert(verified.Votes, qt.HasLen, 4)
	c.Assert(verified.Votes[0].Nullifier, qt.DeepEquals, nullifier)
	c.Assert(verified.Votes[0].Verified, qt.IsTrue)
	c.Assert(verified.Votes[0].ProcessID, qt.DeepEquals, processID)
	c.Assert(verified.Votes[0].TxHash, qt.Not(qt.HasLen), 0)
	c.Assert(verified.Votes[1].Nullifier, qt.DeepEquals, unknown)
	c.Assert(verified.Votes[1].Verified, qt.IsFalse)
	c.Assert(verified.Votes[2], qt.DeepEquals, verified.Votes[0])
	c.Assert(verified.Votes[3].Nullifier, qt.DeepEquals, short)
	c.Assert(verified.Votes[3].Verified, qt.IsFalse)

	// a nullifier that cannot name a vote — empty or over 32 bytes — is rejected outright,
	// not looked up
	requestAndAssertError(errors.ErrMalformedBody, t, http.MethodPost, "",
		&apicommon.VerifyVotesRequest{Nullifiers: []internal.HexBytes{{}}}, "votes", "verify")
	requestAndAssertError(errors.ErrMalformedBody, t, http.MethodPost, "",
		&apicommon.VerifyVotesRequest{
			Nullifiers: []internal.HexBytes{internal.RandomBytes(nullifierSize + 1)},
		}, "votes", "verify")

	// an empty batch and one over the cap are rejected before any chain read
	requestAndAssertError(errors.ErrVoteBatchEmpty, t, http.MethodPost, "",
		&apicommon.VerifyVotesRequest{}, "votes", "verify")
	tooMany := make([]internal.HexBytes, maxVotesPerBatch+1)
	for i := range tooMany {
		tooMany[i] = internal.RandomBytes(nullifierSize)
	}
	requestAndAssertError(errors.ErrVoteBatchTooLarge, t, http.MethodPost, "",
		&apicommon.VerifyVotesRequest{Nullifiers: tooMany}, "votes", "verify")
}

// TestRelayVotesBatch relays the votes of a multi-question process in a single call and
// asserts the batch lands as one job: every envelope's nullifier is readable from the job
// before the chain has replied, and each ends up with the voteID the chain assigned it.
func TestRelayVotesBatch(t *testing.T) {
	c := qt.New(t)
	const questions = 3
	f := setupRelayVoting(t, questions)

	req := &apicommon.RelayVotesRequest{Votes: make([]apicommon.RelayVoteRequest, questions)}
	votesBefore := make([]uint32, questions)
	for i, processID := range f.processIDs {
		var err error
		votesBefore[i], err = f.client.ElectionVoteCount(processID.Bytes())
		c.Assert(err, qt.IsNil)
		proof := f.proofFor(t, processID)
		req.Votes[i] = apicommon.RelayVoteRequest{
			TxPayload: testSignVoteTx(t, f.voter, processID, proof, []byte("[\"1\"]")),
		}
	}

	enq := requestAndParseWithAssertCode[apicommon.EnqueuedResponse](
		http.StatusAccepted, t, http.MethodPost, "", req, "votes")
	c.Assert(enq.JobID, qt.Not(qt.Equals), "")

	// the nullifiers are derived before submission, so they are on the job from the very
	// first read — whether or not the chain has accepted anything yet.
	early := requestAndParse[apicommon.JobResponse](t, http.MethodGet, "", nil, "jobs", enq.JobID)
	c.Assert(early.Type, qt.Equals, db.JobTypeRelayVotes)
	c.Assert(early.Result.Votes, qt.HasLen, questions)
	for i, vote := range early.Result.Votes {
		c.Assert(vote.Nullifier, qt.Not(qt.HasLen), 0, qt.Commentf("vote %d has no nullifier yet", i))
		c.Assert(vote.ProcessID, qt.DeepEquals, f.processIDs[i])
	}

	job := pollJob(t, enq.JobID)
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("errors: %s", job.Errors))
	c.Assert(job.Result.Total, qt.Equals, questions)
	c.Assert(job.Result.Added, qt.Equals, questions)
	c.Assert(job.Result.Votes, qt.HasLen, questions)
	for i, vote := range job.Result.Votes {
		comment := qt.Commentf("vote %d: %s", i, vote.Error)
		c.Assert(vote.Status, qt.Equals, db.JobStatusCompleted, comment)
		c.Assert(vote.ProcessID, qt.DeepEquals, f.processIDs[i], comment)
		c.Assert(vote.VoteID, qt.Not(qt.HasLen), 0, comment)
		// the chain assigns the very nullifier the handler derived from the envelope
		c.Assert(vote.VoteID, qt.DeepEquals, vote.Nullifier, comment)
	}

	// every question got its vote, and every relayed envelope was metered
	for i, processID := range f.processIDs {
		votesAfter, err := f.client.ElectionVoteCount(processID.Bytes())
		c.Assert(err, qt.IsNil)
		c.Assert(votesAfter, qt.Equals, votesBefore[i]+1, qt.Commentf("process %d was not voted", i))
	}
	orgAfter, err := testDB.Organization(f.orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(orgAfter.Counters.SentVotes, qt.Equals, questions)
}

// TestRelayVoteRejectsOversizedBody checks that the single-vote relay, public and
// unauthenticated like the batch one, also refuses a body it would otherwise buffer whole.
func TestRelayVoteRejectsOversizedBody(t *testing.T) {
	requestAndAssertError(errors.ErrRequestBodyTooLarge, t, http.MethodPost, "",
		&apicommon.RelayVoteRequest{TxPayload: internal.HexBytes(make([]byte, maxVoteBodyBytes))}, "vote")
}

// TestRelayVotesRejectsBatch checks that a batch is validated as a unit: every rejection
// happens before anything is enqueued, so a voter retries from a clean slate instead of
// discovering that a prefix of their questions was voted.
func TestRelayVotesRejectsBatch(t *testing.T) {
	c := qt.New(t)
	chainID := fetchVocdoniChainID(t, testNewVocdoniClient(t))

	// two organizations, each owning a process the backend knows about. No chain
	// interaction is needed: every case below is rejected by the synchronous checks.
	newOrgProcess := func() (common.Address, internal.HexBytes) {
		token := testCreateUser(t, "superpassword123")
		orgAddress := testCreateOrganization(t, token)
		processID := internal.HexBytes(randomProcessID())
		_, err := testDB.SetProcess(&db.Process{OrgAddress: orgAddress, Address: processID})
		c.Assert(err, qt.IsNil)
		return orgAddress, processID
	}
	_, processA := newOrgProcess()
	_, processB := newOrgProcess()

	voter := &ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)
	voteFor := func(processID internal.HexBytes) apicommon.RelayVoteRequest {
		return apicommon.RelayVoteRequest{
			TxPayload: testSignVoteTx(t, voter, processID, nil, []byte("[\"1\"]")),
		}
	}
	// a well-formed SignedTx that is not a vote
	notAVote, err := proto.Marshal(&models.Tx{Payload: &models.Tx_SetAccount{
		SetAccount: &models.SetAccountTx{Txtype: models.TxType_CREATE_ACCOUNT},
	}})
	c.Assert(err, qt.IsNil)
	signature, err := voter.SignVocdoniTx(notAVote, chainID)
	c.Assert(err, qt.IsNil)
	notAVoteTx, err := proto.Marshal(&models.SignedTx{Tx: notAVote, Signature: signature})
	c.Assert(err, qt.IsNil)

	for _, tc := range []struct {
		name     string
		votes    []apicommon.RelayVoteRequest
		expected errors.Error
	}{
		{"empty batch", nil, errors.ErrVoteBatchEmpty},
		{
			"over the cap",
			make([]apicommon.RelayVoteRequest, maxVotesPerBatch+1),
			errors.ErrVoteBatchTooLarge,
		},
		{
			"one payload missing",
			[]apicommon.RelayVoteRequest{voteFor(processA), {}},
			errors.ErrMalformedBody,
		},
		{
			"one payload not a vote",
			[]apicommon.RelayVoteRequest{voteFor(processA), {TxPayload: notAVoteTx}},
			errors.ErrInvalidTxFormat,
		},
		{
			"one process unknown",
			[]apicommon.RelayVoteRequest{voteFor(processA), voteFor(internal.HexBytes(randomProcessID()))},
			errors.ErrProcessNotFound,
		},
		{
			"votes of two organizations",
			[]apicommon.RelayVoteRequest{voteFor(processA), voteFor(processB)},
			errors.ErrVoteBatchMixedOrganizations,
		},
		{
			"the same vote twice",
			[]apicommon.RelayVoteRequest{voteFor(processA), voteFor(processA)},
			errors.ErrInvalidTxFormat,
		},
		{
			// the endpoint is public, so an oversized body is refused before it is buffered
			"body over the size cap",
			[]apicommon.RelayVoteRequest{{TxPayload: internal.HexBytes(make([]byte, maxVotesBodyBytes))}},
			errors.ErrRequestBodyTooLarge,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requestAndAssertError(tc.expected, t, http.MethodPost, "",
				&apicommon.RelayVotesRequest{Votes: tc.votes}, "votes")
		})
	}
}
