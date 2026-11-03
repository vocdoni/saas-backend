package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	dvoteapi "go.vocdoni.io/dvote/api"
)

// endDateBody is the PUT /processes/{id}/endDate body for t.
func endDateBody(t time.Time) *apicommon.SetProcessEndDateRequest {
	return &apicommon.SetProcessEndDateRequest{EndDate: t.UTC().Format(time.RFC3339)}
}

// TestProcessEndDateExtendsOnChain extends a really published process on the in-process chain:
// every question's election ends at the new date and the stored end date follows (AC2); the same
// call again is a completed job of no-ops (AC3); nothing is charged (AC7); a request validated
// against an end that changed before its job runs fails (AC-R4), while a duplicate of the change is
// a repeat; when one question's election cannot be read the job sends nothing and the stored end
// date does not move (AC4); a retry then extends both; and a closed question is skipped while the
// open one is extended.
func TestProcessEndDateExtendsOnChain(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "enddatechain1234")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	ids := memberIDs(postOrgMembers(t, token, orgAddress, newOrgMembers(2)...))
	pid, got := publishedProcess(t, token, orgAddress, ids)
	oid := objectID(c, pid)
	c.Assert(got.Questions, qt.HasLen, 2)

	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	orgBefore, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	walletBefore, err := testDB.Wallet(orgAddress)
	c.Assert(err, qt.IsNil)
	ledgerBefore, _, err := testDB.WalletLedger(orgAddress, 1, 10)
	c.Assert(err, qt.IsNil)
	paymentBefore, paymentErrBefore := testDB.ProcessPayment(oid)

	client := testNewVocdoniClient(t)
	balance := func() uint64 {
		acc, err := client.Account(orgAddress.String())
		c.Assert(err, qt.IsNil)
		return acc.Balance
	}

	// a repeat of the original end, before any extension, completes as all no-ops
	job := enqueueAndPollJob(t, http.MethodPut, token, endDateBody(vp.EndDate), "processes", pid, "endDate")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job errors: %v", job.Errors))
	c.Assert(job.Result.Questions, qt.HasLen, 2)
	for _, entry := range job.Result.Questions {
		c.Assert(entry.Status, qt.Equals, db.JobStatusCompleted)
		c.Assert(entry.NoOp, qt.IsTrue)
	}

	// AC2: the job moves every election and then the stored end date; the funder's surcharge is
	// exact, so the organization's on-chain balance does not move
	origEnd := vp.EndDate
	newEnd := vp.EndDate.Add(30 * 24 * time.Hour).Truncate(time.Second).UTC()
	balanceBefore := balance()
	job = enqueueAndPollJob(t, http.MethodPut, token, endDateBody(newEnd), "processes", pid, "endDate")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job errors: %v", job.Errors))
	c.Assert(job.Result, qt.Not(qt.IsNil))
	c.Assert(job.Result.Status, qt.Equals, newEnd.Format(time.RFC3339))
	c.Assert(job.Result.Questions, qt.HasLen, 2)
	for i, entry := range job.Result.Questions {
		c.Assert(entry.QuestionID, qt.Equals, got.Questions[i].ID.Hex())
		c.Assert(entry.Status, qt.Equals, db.JobStatusCompleted)
		c.Assert(entry.NoOp, qt.IsFalse)
		election, err := testAPI.account.Election(got.Questions[i].UpstreamID)
		c.Assert(err, qt.IsNil)
		c.Assert(election.EndDate.Unix(), qt.Equals, newEnd.Unix())
	}
	vp, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.EndDate.Equal(newEnd), qt.IsTrue, qt.Commentf("stored %s, want %s", vp.EndDate, newEnd))
	c.Assert(balance(), qt.Equals, balanceBefore)

	// AC3: the same call again completes with every question a no-op
	job = enqueueAndPollJob(t, http.MethodPut, token, endDateBody(newEnd), "processes", pid, "endDate")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job errors: %v", job.Errors))
	c.Assert(job.Result.Questions, qt.HasLen, 2)
	for _, entry := range job.Result.Questions {
		c.Assert(entry.Status, qt.Equals, db.JobStatusCompleted)
		c.Assert(entry.NoOp, qt.IsTrue)
	}

	// AC-R4: a request validated against the old end whose job runs after the extension fails
	// before sending anything and names the current end; one that asked for that same end is a repeat
	stale := *vp
	stale.EndDate = origEnd
	staleJob := func(end time.Time) apicommon.JobResponse {
		rec := httptest.NewRecorder()
		testAPI.enqueueEndDateChange(rec, httptest.NewRequest(http.MethodPut, "/", nil), &stale, end)
		c.Assert(rec.Code, qt.Equals, http.StatusAccepted, qt.Commentf("resp: %s", rec.Body))
		var enq apicommon.EnqueuedResponse
		c.Assert(json.Unmarshal(rec.Body.Bytes(), &enq), qt.IsNil)
		return pollJob(t, enq.JobID)
	}
	job = staleJob(origEnd)
	c.Assert(job.Status, qt.Equals, db.JobStatusFailed)
	c.Assert(job.Errors, qt.DeepEquals, []string{"the process end date changed to " + newEnd.Format(time.RFC3339) +
		" since this request was validated; send a new request"})
	job = staleJob(newEnd)
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job errors: %v", job.Errors))
	for _, entry := range job.Result.Questions {
		c.Assert(entry.NoOp, qt.IsTrue)
	}
	for i := range got.Questions {
		election, err := testAPI.account.Election(got.Questions[i].UpstreamID)
		c.Assert(err, qt.IsNil)
		c.Assert(election.EndDate.Unix(), qt.Equals, newEnd.Unix())
	}
	vp, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.EndDate.Equal(newEnd), qt.IsTrue, qt.Commentf("stored %s, want %s", vp.EndDate, newEnd))
	// AC7: free — the organization, its wallet and the process payment are untouched
	orgAfter, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(orgAfter, qt.DeepEquals, orgBefore)
	walletAfter, err := testDB.Wallet(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(walletAfter, qt.DeepEquals, walletBefore)
	ledgerAfter, _, err := testDB.WalletLedger(orgAddress, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(ledgerAfter, qt.Equals, ledgerBefore)
	paymentAfter, paymentErrAfter := testDB.ProcessPayment(oid)
	c.Assert(paymentErrAfter, qt.Equals, paymentErrBefore)
	c.Assert(paymentAfter, qt.DeepEquals, paymentBefore)

	// AC4: the second question points at an election the chain does not have; the job reads every
	// election before sending, so it fails with nothing sent and the stored end date keeps the last
	// full success
	c.Assert(testDB.SetQuestionPublished(got.Questions[1].ID, internal.HexBytes(randomProcessID()),
		"url", db.QuestionStatusReady), qt.IsNil)
	later := newEnd.Add(24 * time.Hour)
	job = enqueueAndPollJob(t, http.MethodPut, token, endDateBody(later), "processes", pid, "endDate")
	c.Assert(job.Status, qt.Equals, db.JobStatusFailed)
	c.Assert(job.Result.Questions, qt.HasLen, 2)
	for _, entry := range job.Result.Questions {
		c.Assert(entry.Status, qt.Equals, db.JobStatusFailed)
	}
	for i := range got.Questions {
		election, err := testAPI.account.Election(got.Questions[i].UpstreamID)
		c.Assert(err, qt.IsNil)
		c.Assert(election.EndDate.Unix(), qt.Equals, newEnd.Unix())
	}
	vp, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.EndDate.Equal(newEnd), qt.IsTrue, qt.Commentf("stored %s, want %s", vp.EndDate, newEnd))

	// calling again once the election is back extends both
	c.Assert(testDB.SetQuestionPublished(got.Questions[1].ID, got.Questions[1].UpstreamID,
		"url", db.QuestionStatusReady), qt.IsNil)
	job = enqueueAndPollJob(t, http.MethodPut, token, endDateBody(later), "processes", pid, "endDate")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job errors: %v", job.Errors))
	c.Assert(job.Result.Questions[0].NoOp, qt.IsFalse)
	c.Assert(job.Result.Questions[1].NoOp, qt.IsFalse)
	vp, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.EndDate.Equal(later), qt.IsTrue, qt.Commentf("stored %s, want %s", vp.EndDate, later))
	// a closed question is skipped: only the open one is extended, and the stored end follows
	c.Assert(testDB.SetQuestionStatus(got.Questions[0].ID, db.QuestionStatusEnded), qt.IsNil)
	latest := later.Add(24 * time.Hour)
	job = enqueueAndPollJob(t, http.MethodPut, token, endDateBody(latest), "processes", pid, "endDate")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job errors: %v", job.Errors))
	c.Assert(job.Result.Questions, qt.HasLen, 1)
	c.Assert(job.Result.Questions[0].QuestionID, qt.Equals, got.Questions[1].ID.Hex())
	c.Assert(job.Result.Questions[0].NoOp, qt.IsFalse)
	for i, want := range []time.Time{later, latest} {
		election, err := testAPI.account.Election(got.Questions[i].UpstreamID)
		c.Assert(err, qt.IsNil)
		c.Assert(election.EndDate.Unix(), qt.Equals, want.Unix())
	}
	vp, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.EndDate.Equal(latest), qt.IsTrue, qt.Commentf("stored %s, want %s", vp.EndDate, latest))
}

// TestProcessEndDateRepair pins the repair of a split process on the in-process chain: the same
// endDate is accepted after the stored end passed while an open election already ends at it (AC-R3),
// a question closed on chain but stored open is skipped and its stored status corrected (AC-R2), and
// a job left with nothing open fails without storing anything (AC-store).
func TestProcessEndDateRepair(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "enddaterepair123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	ids := memberIDs(postOrgMembers(t, token, orgAddress, newOrgMembers(2)...))
	pid, got := publishedProcess(t, token, orgAddress, ids)
	oid := objectID(c, pid)
	c.Assert(got.Questions, qt.HasLen, 2)
	q0, q1 := got.Questions[0], got.Questions[1]
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)

	put := func(end time.Time) apicommon.JobResponse {
		return enqueueAndPollJob(t, http.MethodPut, token, endDateBody(end), "processes", pid, "endDate")
	}
	assertStored := func(want time.Time) {
		vp, err := testDB.VotingProcess(oid)
		c.Assert(err, qt.IsNil)
		c.Assert(vp.EndDate.Equal(want), qt.IsTrue, qt.Commentf("stored %s, want %s", vp.EndDate, want))
	}
	assertChainEnd := func(q db.VotingProcessQuestion, want time.Time) {
		election, err := testAPI.account.Election(q.UpstreamID)
		c.Assert(err, qt.IsNil)
		c.Assert(election.EndDate.Unix(), qt.Equals, want.Unix())
	}
	// split extends q0 alone to end (q1 is hidden as closed in storage), then puts the stored end in
	// the past: q0 open at end on chain, q1 open at its previous end, the CSP stopped
	split := func(end time.Time) {
		c.Assert(testDB.SetQuestionStatus(q1.ID, db.QuestionStatusEnded), qt.IsNil)
		job := put(end)
		c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job errors: %v", job.Errors))
		c.Assert(testDB.SetQuestionStatus(q1.ID, db.QuestionStatusReady), qt.IsNil)
		c.Assert(testDB.SetVotingProcessEndDate(oid, time.Now().Add(-time.Hour)), qt.IsNil)
	}
	// endOnChain ends q's election through the status endpoint, then stores it as open again
	endOnChain := func(q db.VotingProcessQuestion) {
		job := enqueueAndPollJob(t, http.MethodPut, token, &apicommon.SetProcessStatusRequest{Status: "ended"},
			"processes", pid, "questions", q.ID.Hex(), "status")
		c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job errors: %v", job.Errors))
		c.Assert(testDB.SetQuestionStatus(q.ID, db.QuestionStatusReady), qt.IsNil)
	}
	// assertSkipped checks a job entry skipped a closed question and that its stored status now
	// follows the chain (vocone moves ENDED to RESULTS within seconds)
	assertSkipped := func(entry db.QuestionJobResult, q db.VotingProcessQuestion) {
		c.Assert(entry.QuestionID, qt.Equals, q.ID.Hex())
		c.Assert(entry.Status, qt.Equals, db.JobStatusCompleted)
		c.Assert(entry.NoOp, qt.IsTrue)
		c.Assert(entry.Error, qt.Matches, "skipped: election is (ENDED|RESULTS) on chain")
		_, questions, err := testDB.ProcessWithQuestions(oid)
		c.Assert(err, qt.IsNil)
		for i := range questions {
			if questions[i].ID == q.ID {
				c.Assert(questions[i].Status, qt.Matches, "ENDED|RESULTS")
				c.Assert(questions[i].SyncedAt.IsZero(), qt.IsFalse)
			}
		}
	}

	// AC-R3: after the stored end passed, the same endDate repairs the split and extends q1; a
	// different date is refused, and an unreadable election is a server error
	end1 := vp.EndDate.Add(24 * time.Hour).Truncate(time.Second).UTC()
	split(end1)
	refused := requestAndExpectError(t, http.MethodPut, token, endDateBody(end1.Add(time.Hour)),
		"processes", pid, "endDate")
	c.Assert(refused.Code, qt.Equals, errors.ErrInvalidData.Code)
	c.Assert(refused.Err.Error(), qt.Contains, "a previous extension is unfinished: send endDate "+
		end1.Format(time.RFC3339)+" to finish it")
	c.Assert(testDB.SetQuestionPublished(q1.ID, internal.HexBytes(randomProcessID()), "url",
		db.QuestionStatusReady), qt.IsNil)
	requestAndAssertError(errors.ErrVochainRequestFailed, t, http.MethodPut, token, endDateBody(end1),
		"processes", pid, "endDate")
	c.Assert(testDB.SetQuestionPublished(q1.ID, q1.UpstreamID, "url", db.QuestionStatusReady), qt.IsNil)
	job := put(end1)
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job errors: %v", job.Errors))
	c.Assert(job.Result.Questions[0].NoOp, qt.IsTrue)
	c.Assert(job.Result.Questions[1].NoOp, qt.IsFalse)
	assertChainEnd(q1, end1)
	assertStored(end1)

	// AC-R3 variant: q1 closed on chain (still stored open) is skipped, q0 is a no-op, and the
	// repair stores the end
	end2 := end1.Add(24 * time.Hour)
	split(end2)
	endOnChain(q1)
	requestAndAssertError(errors.ErrInvalidData, t, http.MethodPut, token, endDateBody(end2.Add(time.Hour)),
		"processes", pid, "endDate")
	c.Assert(testDB.SetQuestionPublished(q1.ID, internal.HexBytes(randomProcessID()), "url",
		db.QuestionStatusReady), qt.IsNil)
	requestAndAssertError(errors.ErrVochainRequestFailed, t, http.MethodPut, token, endDateBody(end2),
		"processes", pid, "endDate")
	c.Assert(testDB.SetQuestionPublished(q1.ID, q1.UpstreamID, "url", db.QuestionStatusReady), qt.IsNil)
	// inside the budget but with nothing open at the date, the real chain still gives the 400
	c.Assert(testDB.SetVotingProcessEndDate(oid, time.Now().Add(6*time.Minute)), qt.IsNil)
	requestAndAssertError(errors.ErrInvalidData, t, http.MethodPut, token, endDateBody(end2.Add(time.Hour)),
		"processes", pid, "endDate")
	c.Assert(testDB.SetVotingProcessEndDate(oid, time.Now().Add(-time.Hour)), qt.IsNil)
	job = put(end2)
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job errors: %v", job.Errors))
	c.Assert(job.Result.Questions, qt.HasLen, 2)
	c.Assert(job.Result.Questions[0].NoOp, qt.IsTrue)
	c.Assert(job.Result.Questions[0].Error, qt.Equals, "")
	assertSkipped(job.Result.Questions[1], q1)
	assertStored(end2)

	// AC-R2: q1 stored open again but closed on chain is skipped while q0 is extended
	c.Assert(testDB.SetQuestionStatus(q1.ID, db.QuestionStatusReady), qt.IsNil)
	end3 := end2.Add(24 * time.Hour)
	job = put(end3)
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job errors: %v", job.Errors))
	c.Assert(job.Result.Questions[0].NoOp, qt.IsFalse)
	assertSkipped(job.Result.Questions[1], q1)
	assertChainEnd(q0, end3)
	assertStored(end3)

	// AC-store: with every target closed on chain the job fails and stores nothing
	endOnChain(q0)
	c.Assert(testDB.SetQuestionStatus(q1.ID, db.QuestionStatusReady), qt.IsNil)
	job = put(end3.Add(24 * time.Hour))
	c.Assert(job.Status, qt.Equals, db.JobStatusFailed)
	c.Assert(job.Errors, qt.DeepEquals, []string{"no open question left to extend; nothing was changed"})
	assertSkipped(job.Result.Questions[0], q0)
	assertSkipped(job.Result.Questions[1], q1)
	assertStored(end3)
}

// TestProcessEndDateRefusals pins the synchronous refusals (AC1) and API-key access (AC6). The
// questions carry fake election ids: every 400 here happens before the chain is read, and the two
// timing refusals that check for a repair surface the fake ids as a 500.
func TestProcessEndDateRefusals(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "enddaterefuse123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	ids := memberIDs(postOrgMembers(t, token, orgAddress, newOrgMembers(2)...))
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint,
	)
	pid := created.ProcessID
	oid := objectID(c, pid)
	vp, questions, err := testDB.ProcessWithQuestions(oid)
	c.Assert(err, qt.IsNil)
	valid := vp.EndDate.Add(time.Hour)

	// a draft, and an unknown process
	requestAndAssertError(errors.ErrInvalidData, t, http.MethodPut, token, endDateBody(valid), "processes", pid, "endDate")
	requestAndAssertError(errors.ErrProcessNotFound, t, http.MethodPut, token, endDateBody(valid),
		"processes", bson.NewObjectID().Hex(), "endDate")

	// publish it with fake election ids
	for i := range questions {
		c.Assert(testDB.SetQuestionPublished(questions[i].ID, internal.HexBytes(randomProcessID()),
			"url", db.QuestionStatusReady), qt.IsNil)
	}
	c.Assert(testDB.SetVotingProcessPublished(oid, "", vp.StartDate), qt.IsNil)

	// malformed body and date
	requestAndAssertError(errors.ErrMalformedBody, t, http.MethodPut, token, "nope", "processes", pid, "endDate")
	requestAndAssertError(errors.ErrMalformedBody, t, http.MethodPut, token,
		&apicommon.SetProcessEndDateRequest{EndDate: "tomorrow"}, "processes", pid, "endDate")
	// not at least 5 minutes after the current end (an earlier end is refused the same way)
	requestAndAssertError(errors.ErrInvalidData, t, http.MethodPut, token,
		endDateBody(vp.EndDate.Add(time.Minute)), "processes", pid, "endDate")
	requestAndAssertError(errors.ErrInvalidData, t, http.MethodPut, token,
		endDateBody(vp.EndDate.Add(-time.Hour)), "processes", pid, "endDate")
	// over the 90-day cap counted from the stored start
	requestAndAssertError(errors.ErrInvalidData, t, http.MethodPut, token,
		endDateBody(vp.StartDate.Add(91*24*time.Hour)), "processes", pid, "endDate")
	// an already ended process, or one too close to its end (5 minutes plus 1 per open question, 7
	// here), is checked for a repair against the chain: the fake election ids are a read error (the
	// 400 for a real chain is pinned in TestProcessEndDateRepair)
	c.Assert(testDB.SetVotingProcessEndDate(oid, time.Now().Add(-time.Hour)), qt.IsNil)
	requestAndAssertError(errors.ErrVochainRequestFailed, t, http.MethodPut, token,
		endDateBody(time.Now().Add(time.Hour)), "processes", pid, "endDate")
	c.Assert(testDB.SetVotingProcessEndDate(oid, time.Now().Add(6*time.Minute)), qt.IsNil)
	requestAndAssertError(errors.ErrVochainRequestFailed, t, http.MethodPut, token,
		endDateBody(time.Now().Add(time.Hour)), "processes", pid, "endDate")
	// a repeat of the ended stored end is refused as before, without reading the chain
	ended := time.Now().Add(-time.Hour).Truncate(time.Second)
	c.Assert(testDB.SetVotingProcessEndDate(oid, ended), qt.IsNil)
	requestAndAssertError(errors.ErrInvalidData, t, http.MethodPut, token, endDateBody(ended), "processes", pid, "endDate")
	c.Assert(testDB.SetVotingProcessEndDate(oid, vp.EndDate), qt.IsNil)
	// no question is still READY or PAUSED
	for i := range questions {
		c.Assert(testDB.SetQuestionStatus(questions[i].ID, db.QuestionStatusEnded), qt.IsNil)
	}
	requestAndAssertError(errors.ErrInvalidData, t, http.MethodPut, token, endDateBody(valid), "processes", pid, "endDate")
	for i := range questions {
		c.Assert(testDB.SetQuestionStatus(questions[i].ID, db.QuestionStatusReady), qt.IsNil)
	}

	// a manager and a non-member are refused
	managerToken := testCreateUser(t, "enddatemanager12")
	manager := requestAndParse[apicommon.UserInfo](t, http.MethodGet, managerToken, nil, usersMeEndpoint)
	c.Assert(testDB.AddUserToOrganization(manager.Email, orgAddress, db.ManagerRole), qt.IsNil)
	requestAndAssertError(errors.ErrUnauthorized, t, http.MethodPut, managerToken, endDateBody(valid),
		"processes", pid, "endDate")
	strangerToken := testCreateUser(t, "enddatestranger1")
	requestAndAssertError(errors.ErrUnauthorized, t, http.MethodPut, strangerToken, endDateBody(valid),
		"processes", pid, "endDate")

	// 503 while the organization lock is held; no job is created
	restore := orgTxLockTimeout
	t.Cleanup(func() { orgTxLockTimeout = restore })
	orgTxLockTimeout = 50 * time.Millisecond
	held := testAPI.orgTxLocks.lock(orgAddress)
	requestAndAssertError(errors.ErrOrgTxBusy, t, http.MethodPut, token, endDateBody(valid), "processes", pid, "endDate")
	held.Unlock()
	orgTxLockTimeout = restore
	jobType := db.JobTypeSetProcessEndDate
	n, _, err := testDB.Jobs(orgAddress, 1, 10, &jobType)
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(0))

	// AC6: a voting:write key whose user is admin is accepted (the job then fails on the fake
	// election ids and leaves the stored end date alone)...
	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	org.Integrator = true
	c.Assert(testDB.SetOrganization(org), qt.IsNil)
	keyBody := &apicommon.CreateAPIKeyRequest{Label: "enddate", Scopes: []string{ScopeVotingWrite}}
	data, code := testRequest(t, http.MethodPost, token, keyBody, "integrator", "organizations", orgAddress.String(), "apikeys")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("resp: %s", data))
	var key apicommon.CreateAPIKeyResponse
	c.Assert(json.Unmarshal(data, &key), qt.IsNil)
	job := enqueueAndPollJob(t, http.MethodPut, key.Secret, endDateBody(valid), "processes", pid, "endDate")
	c.Assert(job.Status, qt.Equals, db.JobStatusFailed)
	c.Assert(job.Result.Questions, qt.HasLen, 2)
	for _, entry := range job.Result.Questions {
		c.Assert(entry.Status, qt.Equals, db.JobStatusFailed)
	}
	stored, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.EndDate.Equal(vp.EndDate), qt.IsTrue)

	// ...and refused once its user is no longer admin
	admin := requestAndParse[apicommon.UserInfo](t, http.MethodGet, token, nil, usersMeEndpoint)
	adminUser, err := testDB.UserByEmail(admin.Email)
	c.Assert(err, qt.IsNil)
	c.Assert(testDB.UpdateOrganizationUserRole(orgAddress, adminUser.ID, db.ManagerRole), qt.IsNil)
	requestAndAssertError(errors.ErrUnauthorized, t, http.MethodPut, key.Secret, endDateBody(valid),
		"processes", pid, "endDate")
}

// TestPlanEndDateChange pins the job's plan (AC-R1, AC-R5): what each question gets, and every
// refusal that must stop the job before any tx is sent.
func TestPlanEndDateChange(t *testing.T) {
	c := qt.New(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	start := now.Add(-time.Hour)
	newEnd := now.Add(24 * time.Hour)
	election := func(status string, end time.Time) *dvoteapi.Election {
		return &dvoteapi.Election{ElectionSummary: dvoteapi.ElectionSummary{Status: status, StartDate: start, EndDate: end}}
	}
	targets := make([]db.VotingProcessQuestion, 4)
	for i := range targets {
		targets[i].ID = bson.NewObjectID()
	}
	plan := func(elections ...*dvoteapi.Election) (endDatePlan, error) {
		return planEndDateChange(now, newEnd, targets[:len(elections)], elections)
	}

	// closed and done questions are skipped and use no budget; the i-th extend needs (i+1) minutes
	got, err := plan(
		election(db.QuestionStatusEnded, now.Add(-time.Minute)),
		election(db.QuestionStatusReady, newEnd),
		election(db.QuestionStatusPaused, now.Add(61*time.Second)),
		election(db.QuestionStatusReady, now.Add(121*time.Second)),
	)
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.HasLen, 4)
	c.Assert(got[0].action, qt.Equals, endDateSkipClosed)
	c.Assert(got[1].action, qt.Equals, endDateNoOp)
	for _, step := range got[2:] {
		c.Assert(step.action, qt.Equals, endDateExtend)
		c.Assert(step.secs, qt.Equals, uint32(newEnd.Sub(start).Seconds()))
	}

	// the same ends in the other order: the second extend no longer has its two minutes
	_, err = plan(
		election(db.QuestionStatusReady, now.Add(121*time.Second)),
		election(db.QuestionStatusReady, now.Add(61*time.Second)),
	)
	c.Assert(err, qt.ErrorMatches, "question "+targets[1].ID.Hex()+" ends at .*, too soon to extend safely; nothing was changed")

	// a no-op target means a repair: the error says when to send the same endDate again, and an
	// end already past is not printed as "ends at"
	_, err = plan(
		election(db.QuestionStatusReady, newEnd),
		election(db.QuestionStatusReady, now.Add(30*time.Second)),
	)
	c.Assert(err, qt.ErrorMatches, "question "+targets[1].ID.Hex()+" ends at "+now.Add(30*time.Second).Format(time.RFC3339)+
		", too soon to extend safely; nothing was changed; send the same endDate again after "+
		now.Add(30*time.Second).Format(time.RFC3339)+" to finish \\(questions that have ended by then will be skipped as ended\\)")
	// the hint waits for the latest too-soon end, not the earliest
	_, err = plan(
		election(db.QuestionStatusReady, newEnd),
		election(db.QuestionStatusReady, now.Add(30*time.Second)),
		election(db.QuestionStatusReady, now.Add(50*time.Second)),
	)
	c.Assert(err, qt.ErrorMatches, "question "+targets[1].ID.Hex()+" ends at "+now.Add(30*time.Second).Format(time.RFC3339)+
		", too soon.*; send the same endDate again after "+now.Add(50*time.Second).Format(time.RFC3339)+" to finish.*")
	_, err = plan(
		election(db.QuestionStatusReady, newEnd),
		election(db.QuestionStatusReady, now.Add(-30*time.Second)),
	)
	c.Assert(err, qt.ErrorMatches, "question "+targets[1].ID.Hex()+" ended at "+now.Add(-30*time.Second).Format(time.RFC3339)+
		", too soon to extend safely; nothing was changed; send the same endDate again in a minute, once the chain "+
		"marks the too-close questions ended, to finish \\(they will be skipped\\)")
	// a past end earlier than a future one: the future one still decides the wait
	_, err = plan(
		election(db.QuestionStatusReady, newEnd),
		election(db.QuestionStatusReady, now.Add(-30*time.Second)),
		election(db.QuestionStatusReady, now.Add(40*time.Second)),
	)
	c.Assert(err, qt.ErrorMatches, "question "+targets[1].ID.Hex()+" ended at .*; send the same endDate again after "+
		now.Add(40*time.Second).Format(time.RFC3339)+" to finish.*")
	// without a no-op target there is no repair hint
	_, err = plan(election(db.QuestionStatusReady, now.Add(30*time.Second)))
	c.Assert(err.Error(), qt.Not(qt.Contains), "send the same endDate")

	// with several offenders the earliest end is named
	_, err = plan(
		election(db.QuestionStatusReady, now.Add(50*time.Second)),
		election(db.QuestionStatusReady, now.Add(30*time.Second)),
	)
	c.Assert(err, qt.ErrorMatches, "question "+targets[1].ID.Hex()+" ends at "+
		now.Add(30*time.Second).Format(time.RFC3339)+", too soon.*")

	// newEnd itself must leave the submit lead plus one budget per extend
	_, err = planEndDateChange(now, now.Add(90*time.Second), targets[:1],
		[]*dvoteapi.Election{election(db.QuestionStatusReady, now.Add(70*time.Second))})
	c.Assert(err, qt.ErrorMatches, "endDate .* is too soon to extend 1 questions safely; nothing was changed")
	_, err = planEndDateChange(now, now.Add(91*time.Second), targets[:1],
		[]*dvoteapi.Election{election(db.QuestionStatusReady, now.Add(70*time.Second))})
	c.Assert(err, qt.IsNil)

	// a shortening is refused
	_, err = plan(election(db.QuestionStatusReady, newEnd.Add(time.Hour)))
	c.Assert(err, qt.ErrorMatches, "question .* already ends at .*, after the new end date; nothing was changed")

	// AC-R5: over 90 days from the election's on-chain start is refused, whatever the stored start
	late := &dvoteapi.Election{ElectionSummary: dvoteapi.ElectionSummary{
		Status: db.QuestionStatusReady, StartDate: newEnd.Add(-90*24*time.Hour - time.Second), EndDate: now.Add(time.Hour),
	}}
	_, err = plan(late)
	c.Assert(err, qt.ErrorMatches, "question .*: the new end is out of range .*at most 90 days.*")
	late.StartDate = late.StartDate.Add(time.Second)
	_, err = plan(late)
	c.Assert(err, qt.IsNil)
}
