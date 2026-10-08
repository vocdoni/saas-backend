package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	dvoteapi "go.vocdoni.io/dvote/api"
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

// testVoteHashes are the metadata hashes a vote envelope attests: its election's and, as
// parentMetadataHash, its parent election's.
type testVoteHashes struct {
	metadata, parent []byte
}

// testStoredVoteHashes returns the hashes stored for the question published as processID, as a
// voting client attests the ones it read: the question's and its process's parent election's.
// Processes that are not questions attest none.
func testStoredVoteHashes(t *testing.T, processID internal.HexBytes) testVoteHashes {
	t.Helper()
	question, err := testDB.QuestionByUpstreamID(processID)
	if err != nil {
		return testVoteHashes{}
	}
	hashes := testVoteHashes{metadata: question.MetadataHash}
	if len(question.ParentUpstreamID) > 0 {
		vp, err := testDB.VotingProcess(question.ProcessID)
		qt.Assert(t, err, qt.IsNil)
		hashes.parent = vp.MetadataHash
	}
	return hashes
}

// testSignVoteTx builds a vote envelope for processID and signs it as the voter would,
// returning the marshaled models.SignedTx the relay endpoints take as their payload. The
// envelope attests the stored hashes (testStoredVoteHashes).
func testSignVoteTx(t *testing.T, signer *ethereum.SignKeys, processID internal.HexBytes,
	proof *models.Proof, votePackage, memo []byte,
) internal.HexBytes {
	t.Helper()
	return testSignVoteTxWithHashes(t, signer, processID, proof, votePackage, memo, testStoredVoteHashes(t, processID))
}

// testSignVoteTxWithMetadataHash is testSignVoteTx attesting the given election metadata hash
// (and the stored parent hash).
func testSignVoteTxWithMetadataHash(t *testing.T, signer *ethereum.SignKeys, processID internal.HexBytes,
	proof *models.Proof, votePackage, memo, metadataHash []byte,
) internal.HexBytes {
	t.Helper()
	hashes := testStoredVoteHashes(t, processID)
	hashes.metadata = metadataHash
	return testSignVoteTxWithHashes(t, signer, processID, proof, votePackage, memo, hashes)
}

// testSignVoteTxWithHashes is testSignVoteTx attesting the given hashes.
func testSignVoteTxWithHashes(t *testing.T, signer *ethereum.SignKeys, processID internal.HexBytes,
	proof *models.Proof, votePackage, memo []byte, hashes testVoteHashes,
) internal.HexBytes {
	t.Helper()
	c := qt.New(t)
	tx := &models.Tx{Payload: &models.Tx_Vote{Vote: &models.VoteEnvelope{
		ProcessId: processID.Bytes(), Nonce: internal.RandomBytes(16), Proof: proof, VotePackage: votePackage,
		Memo: memo, MetadataHash: hashes.metadata, ParentMetadataHash: hashes.parent,
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
	proof *models.Proof, votePackage, memo []byte,
) internal.HexBytes {
	t.Helper()
	c := qt.New(t)
	stx := testSignVoteTx(t, signer, processID, proof, votePackage, memo)
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

// relayVotingFixture is a voter authenticated against a published process covering one or
// more on-chain elections, i.e. everything the relay endpoints need to accept a real vote.
type relayVotingFixture struct {
	token      string
	client     *apiclient.HTTPclient
	orgAddress common.Address
	processIDs []internal.HexBytes
	pid        string
	authToken  internal.HexBytes
	voter      *ethereum.SignKeys
}

// proofFor CSP-signs the voter's address for one of the fixture's processes and builds the
// vote proof from it. The CSP consumes an election per user, not a process, so the same auth
// token signs once for each election — which is exactly what a multi-question vote does.
func (f *relayVotingFixture) proofFor(t *testing.T, processID internal.HexBytes) *models.Proof {
	t.Helper()
	voterAddr := f.voter.Address().Bytes()
	signature := testCSPSign(t, f.pid, f.authToken, processID, voterAddr)
	return testGenerateVoteProof(processID, voterAddr, signature, 1)
}

// setupRelayVoting builds the full CSP voting setup shared by the relay tests: a
// provisioned organization with a plan, a published process with as many questions (each
// its own on-chain election) and a voter authenticated against it.
func setupRelayVoting(t *testing.T, processes int) *relayVotingFixture {
	t.Helper()
	c := qt.New(t)

	token := testCreateUser(t, "superpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	authFields := db.OrgMemberAuthFields{
		db.OrgMemberAuthFieldsName,
		db.OrgMemberAuthFieldsSurname,
		db.OrgMemberAuthFieldsMemberNumber,
	}
	twoFaFields := db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}

	suffix := internal.RandomInt(1000000)
	members := postOrgMembers(t, token, orgAddress, apicommon.OrgMember{
		Name:         "Relay",
		Surname:      "Voter",
		MemberNumber: fmt.Sprintf("R%06d", suffix),
		NationalID:   fmt.Sprintf("RELAY%05dA", suffix),
		BirthDate:    "1990-01-01",
		Email:        fmt.Sprintf("relay.voter.%d@example.com", suffix),
		Phone:        "+34699000001",
		Weight:       "1",
	})
	pid, processIDs := publishCensusProcess(t, token, orgAddress, apicommon.CensusSpec{
		AuthFields:  authFields,
		TwoFaFields: twoFaFields,
		MemberIDs:   memberIDs(members),
	}, processes)

	// authenticate the voter with the CSP
	authToken := testCSPAuthenticateWithFields(t, pid, &handlers.AuthRequest{
		Name:         members[0].Name,
		Surname:      members[0].Surname,
		MemberNumber: members[0].MemberNumber,
		Email:        members[0].Email,
	})

	voter := &ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)

	return &relayVotingFixture{
		token:      token,
		client:     testNewVocdoniClient(t),
		orgAddress: orgAddress,
		processIDs: processIDs,
		pid:        pid,
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

	nullifier := testRelayVoteRequest(t, f.voter, processID, proof, []byte("[\"1\"]"), nil)
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
	tooMany := make([]internal.HexBytes, db.MaxQuestionsPerProcess+1)
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
			TxPayload: testSignVoteTx(t, f.voter, processID, proof, []byte("[\"1\"]"), nil),
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
	// interaction is needed: every case below is rejected by the synchronous checks. The
	// processes are legacy rows, which keeps parseRelayVote's legacy lookup covered.
	newOrgProcess := func() (common.Address, internal.HexBytes) {
		token := testCreateUser(t, "superpassword123")
		orgAddress := testCreateOrganization(t, token)
		processID := internal.HexBytes(randomProcessID())
		insertLegacyDoc(t, "processes", db.Process{ID: bson.NewObjectID(), OrgAddress: orgAddress, Address: processID})
		return orgAddress, processID
	}
	_, processA := newOrgProcess()
	_, processB := newOrgProcess()

	voter := &ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)
	voteFor := func(processID internal.HexBytes) apicommon.RelayVoteRequest {
		return apicommon.RelayVoteRequest{
			TxPayload: testSignVoteTx(t, voter, processID, nil, []byte("[\"1\"]"), nil),
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
			make([]apicommon.RelayVoteRequest, db.MaxQuestionsPerProcess+1),
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

// TestProcessMetadataHash checks the metadata hash contract of a published process: each question's
// election commits on chain the SHA-256 of the exact bytes its metadataURL serves, the question reads
// expose that hash, and the relay rejects up front a vote that attests any other hash.
func TestProcessMetadataHash(t *testing.T) {
	c := qt.New(t)
	f := setupRelayVoting(t, 2)

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, f.token, nil, "processes", f.pid)
	c.Assert(got.Questions, qt.HasLen, 2)
	for i, q := range got.Questions {
		comment := qt.Commentf("question %d", i)
		c.Assert(q.MetadataURL, qt.Not(qt.Equals), "", comment)
		served, code := testRequest(t, http.MethodGet, "", nil, "storage", path.Base(q.MetadataURL))
		c.Assert(code, qt.Equals, http.StatusOK, comment)
		want := sha256.Sum256(served)
		c.Assert([]byte(q.MetadataHash), qt.DeepEquals, want[:], comment)
		// a question's document carries only the question: the process text and media are the
		// parent election's
		var doc dvoteapi.ElectionMetadata
		c.Assert(json.Unmarshal(served, &doc), qt.IsNil, comment)
		c.Assert(doc.Meta, qt.IsNil, comment)
		c.Assert(doc.Media, qt.Equals, dvoteapi.ProcessMedia{}, comment)
		c.Assert(doc.Questions, qt.HasLen, 1, comment)

		election, err := f.client.Election(q.UpstreamID.Bytes())
		c.Assert(err, qt.IsNil, comment)
		c.Assert(election.MetadataURL, qt.Equals, q.MetadataURL, comment)
		c.Assert([]byte(election.MetadataHash), qt.DeepEquals, want[:], comment)

		public := requestAndParse[apicommon.PublicQuestionResponse](t, http.MethodGet, "", nil,
			"processes", f.pid, "questions", q.ID.Hex())
		c.Assert(public.MetadataURL, qt.Equals, q.MetadataURL, comment)
		c.Assert(public.MetadataHash, qt.DeepEquals, q.MetadataHash, comment)
	}

	stale := internal.RandomBytes(sha256.Size)
	current := testSignVoteTx(t, f.voter, f.processIDs[0], nil, []byte("[\"1\"]"), nil)
	t.Run("single vote with a stale hash", func(t *testing.T) {
		requestAndAssertError(errors.ErrVoteMetadataChanged, t, http.MethodPost, "",
			&apicommon.RelayVoteRequest{
				TxPayload: testSignVoteTxWithMetadataHash(t, f.voter, f.processIDs[1], nil, []byte("[\"1\"]"), nil, stale),
			}, "vote")
	})
	t.Run("single vote without a hash", func(t *testing.T) {
		requestAndAssertError(errors.ErrVoteMetadataChanged, t, http.MethodPost, "",
			&apicommon.RelayVoteRequest{
				TxPayload: testSignVoteTxWithMetadataHash(t, f.voter, f.processIDs[1], nil, []byte("[\"1\"]"), nil, nil),
			}, "vote")
	})
	t.Run("batch with one stale hash", func(t *testing.T) {
		requestAndAssertError(errors.ErrVoteMetadataChanged, t, http.MethodPost, "",
			&apicommon.RelayVotesRequest{Votes: []apicommon.RelayVoteRequest{
				{TxPayload: current},
				{TxPayload: testSignVoteTxWithMetadataHash(t, f.voter, f.processIDs[1], nil, []byte("[\"1\"]"), nil, stale)},
			}}, "votes")
	})
	// the rejected batch enqueued nothing, so no vote reached the chain
	for i, processID := range f.processIDs {
		count, err := f.client.ElectionVoteCount(processID.Bytes())
		c.Assert(err, qt.IsNil)
		c.Assert(count, qt.Equals, uint32(0), qt.Commentf("process %d", i))
	}
}

func TestParentMetadataCurrent(t *testing.T) {
	c := qt.New(t)
	stored, pending, other := internal.HexBytes{1}, internal.HexBytes{2}, []byte{3}

	// a process published without a parent hash accepts any
	c.Assert(parentMetadataCurrent(&db.VotingProcess{}, other), qt.IsTrue)

	vp := &db.VotingProcess{MetadataHash: stored}
	c.Assert(parentMetadataCurrent(vp, stored), qt.IsTrue)
	c.Assert(parentMetadataCurrent(vp, pending), qt.IsFalse)

	// a pending parent edit may already be what the chain commits to
	vp.PendingMetadata = &db.PendingProcessMetadata{MetadataHash: pending}
	c.Assert(parentMetadataCurrent(vp, stored), qt.IsTrue)
	c.Assert(parentMetadataCurrent(vp, pending), qt.IsTrue)
	c.Assert(parentMetadataCurrent(vp, other), qt.IsFalse)
}
