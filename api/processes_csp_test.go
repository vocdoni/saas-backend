package api

import (
	"bytes"
	"encoding/hex"
	"math/big"
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
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/crypto/ethereum"
)

// verifyProcessCSP completes the OTP challenge for a mid-challenge process auth token and
// returns the verified token.
func verifyProcessCSP(t *testing.T, pid, email string, authToken internal.HexBytes) internal.HexBytes {
	t.Helper()
	c := qt.New(t)
	otp := extractOTPFromBody(waitForEmail(t, email))
	c.Assert(otp, qt.Not(qt.Equals), "")
	verified := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "",
		&handlers.AuthChallengeRequest{AuthToken: authToken, AuthData: []string{otp}},
		"processes", pid, "auth", "1")
	c.Assert(verified.AuthToken, qt.Not(qt.HasLen), 0)
	return verified.AuthToken
}

// authProcessCSP runs the full process-scoped CSP auth flow (auth/0 -> OTP -> auth/1) and
// returns the verified auth token.
func authProcessCSP(t *testing.T, pid string, authReq *handlers.AuthRequest) internal.HexBytes {
	t.Helper()
	step0 := requestAndParse[handlers.AuthResponse](
		t, http.MethodPost, "", authReq, "processes", pid, "auth", "0",
	)
	qt.Assert(t, step0.AuthToken, qt.Not(qt.HasLen), 0)
	return verifyProcessCSP(t, pid, authReq.Email, step0.AuthToken)
}

// publishCensusProcess creates and publishes a /processes election over the given census with n
// yes/no questions, returning the process id and the on-chain election id of each question. The
// organization must be provisioned on chain and subscribed to a plan.
func publishCensusProcess(t *testing.T, token string, orgAddress common.Address, census apicommon.CensusSpec, n int,
) (string, []internal.HexBytes) {
	t.Helper()
	req := minimalVotingProcessRequest(orgAddress)
	req.StartDate = ""
	req.Census = census
	for len(req.Questions) < n {
		req.Questions = append(req.Questions, req.Questions[0])
	}
	return publishProcessRequest(t, token, req)
}

// payIfPriced pays a priced draft of a standard organization as its checkout would. Add-ons are
// charged at any census size (SMS 2FA on a few voters is priced), so tests about something other
// than payment pay first. A managed organization's draft is left to the integrator wallet.
func payIfPriced(t *testing.T, token, pid string) {
	t.Helper()
	vp, err := testDB.VotingProcess(objectID(qt.New(t), pid))
	qt.Assert(t, err, qt.IsNil)
	org, err := testDB.Organization(vp.OrgAddress)
	qt.Assert(t, err, qt.IsNil)
	if org.ManagedBy != (common.Address{}) {
		return
	}
	price := requestAndParse[apicommon.ProcessPriceResponse](t, http.MethodGet, token, nil, "processes", pid, "price")
	if price.TotalCents > 0 {
		markProcessPaid(t, pid)
	}
}

// publishProcessRequest creates and publishes req, returning the process id and the on-chain
// election id of each of its questions.
func publishProcessRequest(t *testing.T, token string, req *apicommon.CreateVotingProcessRequest,
) (string, []internal.HexBytes) {
	t.Helper()
	c := qt.New(t)
	n := len(req.Questions)
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint,
	).ProcessID
	payIfPriced(t, token, pid)
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Questions, qt.HasLen, n)
	elections := make([]internal.HexBytes, n)
	for i, q := range got.Questions {
		c.Assert(q.UpstreamID, qt.Not(qt.HasLen), 0)
		elections[i] = q.UpstreamID
	}
	return pid, elections
}

// TestProcessCSP exercises the process-scoped CSP handlers (auth/resend/sign/weight/check)
// end to end against a published multi-question process. Question 2 has an eligibility
// subset restricted to the first member, so the second member can authenticate but is not
// eligible for it.
func TestProcessCSP(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	// a process whose census authenticates by name+surname with an email OTP; question 2 is
	// restricted to the first member (ids[:1]) via its eligibility subset.
	req := newVotingProcessRequest(orgAddress, ids)
	req.StartDate = ""
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname}
	req.Census.Weighted = true
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint,
	)
	pid := created.ProcessID

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Questions, qt.HasLen, 2)
	// question index 1 is the eligibility-restricted one
	c.Assert(got.Questions[1].EligibleMemberIDs, qt.HasLen, 1)
	openElection := got.Questions[0].UpstreamID       // all census members eligible
	restrictedElection := got.Questions[1].UpstreamID // only the first member eligible
	c.Assert(len(openElection) > 0, qt.IsTrue)
	c.Assert(len(restrictedElection) > 0, qt.IsTrue)

	authReq := func(i int) *handlers.AuthRequest {
		return &handlers.AuthRequest{
			Name:    members[i].Name,
			Surname: members[i].Surname,
			Email:   members[i].Email,
		}
	}
	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)

	// --- first member: authenticate, but keep the mid-challenge token to exercise the
	// unverified-sign gate and the OTP resend before completing verification ---
	step0 := requestAndParse[handlers.AuthResponse](
		t, http.MethodPost, "", authReq(0), "processes", pid, "auth", "0",
	)
	c.Assert(step0.AuthToken, qt.Not(qt.HasLen), 0)

	// an unverified token cannot sign
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "",
		&handlers.SignRequest{
			AuthToken: step0.AuthToken,
			ProcessID: openElection,
			Payload:   hex.EncodeToString(voter.Address().Bytes()),
		}, "processes", pid, "sign")
	// nor read the member's weight and voted status
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "",
		&handlers.CheckMembershipRequest{AuthToken: step0.AuthToken}, "processes", pid, "check")

	// a resend with a contact that does not match the member fails with the same
	// indistinguishable error that an unknown participant gets at step 0
	requestAndAssertError(errors.ErrCSPAuthFailed, t, http.MethodPost, "",
		&handlers.AuthResendRequest{AuthToken: step0.AuthToken, Email: "wrong-contact@example.com"},
		"processes", pid, "auth", "resend")

	// resend the OTP challenge for the mid-challenge token
	resend := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "",
		&handlers.AuthResendRequest{AuthToken: step0.AuthToken, Email: members[0].Email},
		"processes", pid, "auth", "resend")
	c.Assert(resend.AuthToken, qt.Not(qt.HasLen), 0)

	// wrong contacts spend the token's resends like real ones (two spent so far, the cap is 3), so a
	// token holder cannot probe a member's contact endlessly: once spent, even the right one is refused
	requestAndAssertError(errors.ErrCSPAuthFailed, t, http.MethodPost, "",
		&handlers.AuthResendRequest{AuthToken: step0.AuthToken, Email: "another-wrong@example.com"},
		"processes", pid, "auth", "resend")
	requestAndAssertError(errors.ErrVerificationMaxAttempts, t, http.MethodPost, "",
		&handlers.AuthResendRequest{AuthToken: step0.AuthToken, Email: members[0].Email},
		"processes", pid, "auth", "resend")

	// complete verification
	tok0 := verifyProcessCSP(t, pid, members[0].Email, step0.AuthToken)

	// check: belongs to the process, both questions votable, nothing voted yet
	check0 := requestAndParse[handlers.ProcessCheckResponse](t, http.MethodPost, "",
		&handlers.CheckMembershipRequest{AuthToken: tok0}, "processes", pid, "check")
	c.Assert(check0.BelongsToProcess, qt.IsTrue)
	c.Assert(check0.Questions, qt.HasLen, 2)
	for _, q := range check0.Questions {
		c.Assert(q.CanVote, qt.IsTrue, qt.Commentf("member 0 should vote every question"))
		c.Assert(q.HasVoted, qt.IsFalse)
	}

	// weight: member 0 weight is 1 on a weighted census
	weight0 := requestAndParse[handlers.UserWeightResponse](t, http.MethodPost, "",
		&handlers.UserWeightRequest{AuthToken: tok0}, "processes", pid, "weight")
	c.Assert(bytes.Equal(weight0.Weight, big.NewInt(1).Bytes()), qt.IsTrue,
		qt.Commentf("unexpected weight %x", weight0.Weight))

	// sign a ballot for the open election
	sign0 := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "",
		&handlers.SignRequest{
			AuthToken: tok0,
			ProcessID: openElection,
			Payload:   hex.EncodeToString(voter.Address().Bytes()),
		}, "processes", pid, "sign")
	c.Assert(sign0.Signature, qt.Not(qt.HasLen), 0)

	// a sign request naming no election is a client error, not an internal one
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, "",
		&handlers.SignRequest{AuthToken: tok0, Payload: hex.EncodeToString(voter.Address().Bytes())},
		"processes", pid, "sign")

	// the blind endpoints reject a non-anonymous census (mirror of the anonymous-census guard on
	// the plain sign endpoints, exercised in TestProcessBlindCSP)
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, "",
		&handlers.BlindPointRequest{AuthToken: tok0, Elections: []internal.HexBytes{openElection}},
		"processes", pid, "blind-point")

	// sign-info: member 0's consumed address + nullifier for the open election are now available
	signInfo := requestAndParse[handlers.ProcessSignInfoResponse](t, http.MethodPost, "",
		&handlers.ConsumedAddressRequest{AuthToken: tok0}, "processes", pid, "sign-info")
	c.Assert(signInfo.Consumed, qt.HasLen, 1) // only the open election was signed
	c.Assert(bytes.Equal(signInfo.Consumed[0].UpstreamID, openElection), qt.IsTrue)
	c.Assert(bytes.Equal(signInfo.Consumed[0].Address, voter.Address().Bytes()), qt.IsTrue)
	c.Assert(signInfo.Consumed[0].Nullifier, qt.Not(qt.HasLen), 0)

	// participants (manager): member 0 matched by email. Per question: (hasSigned, hasVoted).
	participantsPath := "participants?field=" + string(db.OrgMemberLookupFieldEmail) + "&value=" + members[0].Email
	assertParticipant := func(openSigned, openVoted bool) {
		t.Helper()
		pc := requestAndParse[apicommon.ProcessParticipantsResponse](t, http.MethodGet, token, nil,
			"processes", pid, participantsPath)
		c.Assert(pc.Participants, qt.HasLen, 1)
		c.Assert(pc.Participants[0].MemberID, qt.Equals, members[0].ID)
		c.Assert(pc.Participants[0].Questions, qt.HasLen, 2)
		for _, qv := range pc.Participants[0].Questions {
			if bytes.Equal(qv.UpstreamID, openElection) {
				c.Assert(qv.HasSigned, qt.Equals, openSigned)
				c.Assert(qv.HasVoted, qt.Equals, openVoted)
			} else {
				c.Assert(qv.HasSigned, qt.IsFalse)
				c.Assert(qv.HasVoted, qt.IsFalse)
			}
		}
	}
	// signed but not relayed: the CSP signature is not a vote
	assertParticipant(true, false)

	// AC5b: a signed row the chain cannot be asked about is reported as unknown, never as not
	// voted, while the rest of the list still renders
	failVoteLookup(signInfo.Consumed[0].Nullifier)
	pc := requestAndParse[apicommon.ProcessParticipantsResponse](t, http.MethodGet, token, nil,
		"processes", pid, participantsPath)
	c.Assert(pc.Participants, qt.HasLen, 1)
	for _, qv := range pc.Participants[0].Questions {
		open := bytes.Equal(qv.UpstreamID, openElection)
		c.Assert(qv.HasSigned, qt.Equals, open)
		c.Assert(qv.HasVoted, qt.IsFalse)
		c.Assert(qv.HasVotedUnknown, qt.Equals, open)
	}
	unfailVoteLookup(signInfo.Consumed[0].Nullifier)

	// once the vote is mined, hasVoted flips to true (and stays, now from the cached flag)
	testCastVote(t, testNewVocdoniClient(t), &voter, openElection,
		testGenerateVoteProof(openElection, voter.Address().Bytes(), sign0.Signature, 1), []byte("[\"1\"]"))
	assertParticipant(true, true)
	// the found vote is cached on the row: a chain that cannot be asked no longer matters
	failVoteLookup(signInfo.Consumed[0].Nullifier)
	assertParticipant(true, true)
	unfailVoteLookup(signInfo.Consumed[0].Nullifier)
	check0 = requestAndParse[handlers.ProcessCheckResponse](t, http.MethodPost, "",
		&handlers.CheckMembershipRequest{AuthToken: tok0}, "processes", pid, "check")
	for _, q := range check0.Questions {
		voted := bytes.Equal(q.UpstreamID, openElection)
		c.Assert(q.HasSigned, qt.Equals, voted)
		c.Assert(q.HasVoted, qt.Equals, voted)
	}
	// the participant lookup is Manager/Admin only
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodGet, "", nil, "processes", pid, participantsPath)

	// --- second member: authenticates but is not eligible for the restricted question ---
	tok1 := authProcessCSP(t, pid, authReq(1))
	check1 := requestAndParse[handlers.ProcessCheckResponse](t, http.MethodPost, "",
		&handlers.CheckMembershipRequest{AuthToken: tok1}, "processes", pid, "check")
	c.Assert(check1.BelongsToProcess, qt.IsTrue)
	for _, q := range check1.Questions {
		if bytes.Equal(q.UpstreamID, restrictedElection) {
			c.Assert(q.CanVote, qt.IsFalse, qt.Commentf("member 1 must not vote the restricted question"))
		} else {
			c.Assert(q.CanVote, qt.IsTrue)
		}
	}

	// signing the restricted election with an ineligible member is rejected
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "",
		&handlers.SignRequest{
			AuthToken: tok1,
			ProcessID: restrictedElection,
			Payload:   hex.EncodeToString(voter.Address().Bytes()),
		}, "processes", pid, "sign")

	// a verified token anchored to a different process is rejected on this one
	otherTok := internal.HexBytes(internal.RandomBytes(16))
	otherOID := bson.NewObjectID()
	otherAnchor := internal.HexBytes(otherOID[:])
	c.Assert(testDB.SetCSPAuth(otherTok, internal.HexBytesFromString(members[0].ID), otherAnchor, ""), qt.IsNil)
	c.Assert(testDB.VerifyCSPAuth(otherTok), qt.IsNil)
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "",
		&handlers.CheckMembershipRequest{AuthToken: otherTok}, "processes", pid, "check")

	// a malformed process id is a client error
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, "",
		&handlers.CheckMembershipRequest{AuthToken: tok0}, "processes", "not-a-valid-id", "check")
}

// TestProcessCSPSignBatch exercises POST /processes/{processId}/sign-batch: a batch is
// authorized as a unit and signs nothing on failure, and once authorized every ballot is
// signed with a per-ballot outcome.
func TestProcessCSPSignBatch(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	// same fixture as TestProcessCSP: question 2 is restricted to the first member.
	req := newVotingProcessRequest(orgAddress, ids)
	req.StartDate = ""
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname}
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint,
	)
	pid := created.ProcessID

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Questions, qt.HasLen, 2)
	openElection := got.Questions[0].UpstreamID
	restrictedElection := got.Questions[1].UpstreamID

	authReq := func(i int) *handlers.AuthRequest {
		return &handlers.AuthRequest{Name: members[i].Name, Surname: members[i].Surname, Email: members[i].Email}
	}
	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)
	address := internal.HexBytes(voter.Address().Bytes())
	batch := func(authToken internal.HexBytes, elections ...internal.HexBytes) *handlers.SignBatchRequest {
		ballots := make([]handlers.SignBatchBallot, len(elections))
		for i, e := range elections {
			ballots[i] = handlers.SignBatchBallot{UpstreamID: e, Address: address}
		}
		return &handlers.SignBatchRequest{AuthToken: authToken, Ballots: ballots}
	}
	consumed := func(authToken internal.HexBytes) []handlers.QuestionConsumedAddress {
		return requestAndParse[handlers.ProcessSignInfoResponse](t, http.MethodPost, "",
			&handlers.ConsumedAddressRequest{AuthToken: authToken}, "processes", pid, "sign-info").Consumed
	}

	// --- second member: eligible for the open question only, so a batch naming both is
	// rejected as a unit and its eligible sibling is NOT signed. Keep the mid-challenge token so
	// the unverified case below can reuse it without a second auth/0 (which the OTP cooldown
	// would refuse) ---
	step0tok1 := requestAndParse[handlers.AuthResponse](
		t, http.MethodPost, "", authReq(1), "processes", pid, "auth", "0",
	)
	// an unverified (mid-challenge) token cannot sign. sign-info needs a verified token too, so
	// the "nothing was signed" half is asserted below, once the same token is verified.
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "",
		batch(step0tok1.AuthToken, openElection), "processes", pid, "sign-batch")
	tok1 := verifyProcessCSP(t, pid, members[1].Email, step0tok1.AuthToken)
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "",
		batch(tok1, openElection, restrictedElection), "processes", pid, "sign-batch")
	c.Assert(consumed(tok1), qt.HasLen, 0)

	// a repeated election, an empty batch and more ballots than the process has questions are
	// all client errors, and none of them signs anything
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, "",
		batch(tok1, openElection, openElection), "processes", pid, "sign-batch")
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, "",
		batch(tok1), "processes", pid, "sign-batch")
	requestAndAssertError(errors.ErrVoteBatchTooLarge, t, http.MethodPost, "",
		&handlers.SignBatchRequest{
			AuthToken: tok1,
			Ballots:   make([]handlers.SignBatchBallot, db.MaxQuestionsPerProcess+1),
		},
		"processes", pid, "sign-batch")
	// a ballot without an address is rejected, and takes the batch with it
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, "",
		&handlers.SignBatchRequest{
			AuthToken: tok1,
			Ballots:   []handlers.SignBatchBallot{{UpstreamID: openElection}},
		}, "processes", pid, "sign-batch")
	// ...and so is one without an election: an empty upstreamId is a client error, not the 500
	// that db.QuestionByUpstreamID's ErrInvalidData would otherwise become
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, "",
		&handlers.SignBatchRequest{
			AuthToken: tok1,
			Ballots:   []handlers.SignBatchBallot{{Address: address}},
		}, "processes", pid, "sign-batch")
	// a wrong-length address is rejected before it can be pinned as the election's consumer
	shortAddr := internal.HexBytes(bytes.Repeat([]byte{1}, common.AddressLength-1))
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, "",
		&handlers.SignBatchRequest{
			AuthToken: tok1,
			Ballots:   []handlers.SignBatchBallot{{UpstreamID: openElection, Address: shortAddr}},
		},
		"processes", pid, "sign-batch")
	// an oversized body is refused before it is buffered (413, not 400)
	huge := internal.HexBytes(bytes.Repeat([]byte{0}, 30000)) // hex ~60KB > the ~54KB cap
	requestAndAssertError(errors.ErrRequestBodyTooLarge, t, http.MethodPost, "",
		&handlers.SignBatchRequest{
			AuthToken: tok1,
			Ballots:   []handlers.SignBatchBallot{{UpstreamID: openElection, Address: huge}},
		},
		"processes", pid, "sign-batch")
	c.Assert(consumed(tok1), qt.HasLen, 0)

	// --- first member: both questions signed in one call ---
	tok0 := authProcessCSP(t, pid, authReq(0))
	signed := requestAndParse[handlers.SignBatchResponse](t, http.MethodPost, "",
		batch(tok0, openElection, restrictedElection), "processes", pid, "sign-batch")
	c.Assert(signed.Signatures, qt.HasLen, 2)
	weightOne := internal.HexBytes(big.NewInt(1).Bytes())
	for i, s := range signed.Signatures {
		c.Assert(s.Error, qt.Equals, "", qt.Commentf("ballot %d", i))
		c.Assert(s.Code, qt.Equals, "", qt.Commentf("ballot %d", i))
		c.Assert(s.Signature, qt.Not(qt.HasLen), 0)
		c.Assert(s.Weight, qt.DeepEquals, weightOne)
	}
	c.Assert(signed.Signatures[0].UpstreamID, qt.DeepEquals, openElection)
	c.Assert(signed.Signatures[1].UpstreamID, qt.DeepEquals, restrictedElection)
	c.Assert(consumed(tok0), qt.HasLen, 2)

	// re-signing a consumed election with a DIFFERENT address is its own recoverable outcome
	// (address_mismatch, fix: re-send with the pinned address), not already_consumed
	otherVoter := ethereum.SignKeys{}
	c.Assert(otherVoter.Generate(), qt.IsNil)
	mismatch := requestAndParse[handlers.SignBatchResponse](t, http.MethodPost, "",
		&handlers.SignBatchRequest{
			AuthToken: tok0,
			Ballots: []handlers.SignBatchBallot{
				{UpstreamID: openElection, Address: internal.HexBytes(otherVoter.Address().Bytes())},
			},
		}, "processes", pid, "sign-batch")
	c.Assert(mismatch.Signatures, qt.HasLen, 1)
	c.Assert(mismatch.Signatures[0].Code, qt.Equals, "address_mismatch")
	c.Assert(mismatch.Signatures[0].Signature, qt.HasLen, 0)

	// --- a spent signing slot is a per-ballot error, not a failed batch: exhaust the open
	// election's signatures (it is at 1 after the batch above) and re-run the same batch ---
	for i := 0; i < db.MaxCSPSignaturesPerElection-1; i++ {
		requestAndParse[handlers.AuthResponse](t, http.MethodPost, "",
			&handlers.SignRequest{AuthToken: tok0, ProcessID: openElection, Payload: address.String()},
			"processes", pid, "sign")
	}
	again := requestAndParse[handlers.SignBatchResponse](t, http.MethodPost, "",
		batch(tok0, openElection, restrictedElection), "processes", pid, "sign-batch")
	c.Assert(again.Signatures, qt.HasLen, 2)
	c.Assert(again.Signatures[0].Code, qt.Equals, "already_consumed")
	c.Assert(again.Signatures[0].Error, qt.Not(qt.Equals), "")
	c.Assert(again.Signatures[0].Signature, qt.HasLen, 0)
	c.Assert(again.Signatures[1].Code, qt.Equals, "")
	c.Assert(again.Signatures[1].Error, qt.Equals, "")
	c.Assert(again.Signatures[1].Signature, qt.Not(qt.HasLen), 0)

	// an all-failed batch is still 200: openElection is spent, so a single-ballot batch over it
	// returns one entry carrying a code and no signature, not an error status
	allFailed := requestAndParse[handlers.SignBatchResponse](t, http.MethodPost, "",
		batch(tok0, openElection), "processes", pid, "sign-batch")
	c.Assert(allFailed.Signatures, qt.HasLen, 1)
	c.Assert(allFailed.Signatures[0].Code, qt.Equals, "already_consumed")
	c.Assert(allFailed.Signatures[0].Signature, qt.HasLen, 0)
	c.Assert(allFailed.Signatures[0].Error, qt.Not(qt.Equals), "")
}

// TestProcessCSPSignRefusedAfterEnd checks that the CSP refuses to sign once the process end date
// has passed (AC7). Publish refuses a past end date, so the stored one is moved afterwards.
func TestProcessCSPSignRefusedAfterEnd(t *testing.T) {
	c := qt.New(t)
	f := setupRelayVoting(t, 1)

	oid, err := bson.ObjectIDFromHex(f.pid)
	c.Assert(err, qt.IsNil)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	vp.EndDate = time.Now().Add(-2 * time.Minute) // past the CSP's end grace
	_, err = testDB.SetVotingProcess(vp)
	c.Assert(err, qt.IsNil)

	apiErr := requestAndExpectError(t, http.MethodPost, "", &handlers.SignRequest{
		AuthToken: f.authToken,
		ProcessID: f.processIDs[0],
		Payload:   hex.EncodeToString(f.voter.Address().Bytes()),
	}, "processes", f.pid, "sign")
	c.Assert(apiErr.Code, qt.Equals, errors.ErrUnauthorized.Code)
	c.Assert(apiErr.Error(), qt.Contains, "process has ended")
}
