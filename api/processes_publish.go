package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/proto/build/go/models"
)

// maxPublishRounds bounds how many times the publish worker re-batches the not-yet-confirmed
// questions before giving up and abandoning the attempt (questions are then regenerated from
// scratch on the next publish).
const maxPublishRounds = 3

// electionParamsForQuestion builds the single-election params for one question by combining
// the process's shared params with the question's ballot config (translated) and the
// server-computed maxCensusSize.
func electionParamsForQuestion(
	vp *db.VotingProcess, q *db.VotingProcessQuestion, census *db.Census,
) (*db.ElectionParams, error) {
	voteType, err := account.VoteTypeFromQuestion(q)
	if err != nil {
		return nil, err
	}
	maxCensusSize := account.ComputeMaxCensusSize(q.EligibleMemberIDs, census.Size)
	if maxCensusSize == 0 {
		return nil, fmt.Errorf("cannot determine census size for question")
	}
	return &db.ElectionParams{
		Title:       q.Title,
		Description: q.Description,
		Header:      vp.Header,
		StreamURI:   vp.StreamURI,
		StartDate:   vp.StartDate,
		EndDate:     vp.EndDate,
		Questions: []db.Question{{
			Title:       q.Title,
			Description: q.Description,
			Choices:     q.Choices,
		}},
		VoteType:      voteType,
		ElectionType:  account.ElectionTypeFromQuestion(q),
		MaxCensusSize: maxCensusSize,
	}, nil
}

// reconcileStalePublishing clears publishing markers left behind by a crash/restart/deploy
// (processes whose marker is older than db.PublishStaleAfter) and resets their questions so they
// can be published again. Run once at startup; the claim path also reclaims stale markers as a
// second line of defense.
func (a *API) reconcileStalePublishing() {
	ids, err := a.db.StaleVotingProcesses()
	if err != nil {
		log.Warnw("could not scan for stale publishing processes", "error", err)
		return
	}
	for _, id := range ids {
		if e := a.db.ResetQuestionsPublish(id); e != nil {
			log.Warnw("could not reset questions of stale publishing process", "processId", id.Hex(), "error", e)
		}
		if e := a.db.ClearVotingProcessPublishing(id); e != nil {
			log.Warnw("could not clear stale publishing marker", "processId", id.Hex(), "error", e)
			continue
		}
		log.Infow("reconciled stale publishing process", "processId", id.Hex())
	}
}

// publishPreflightProblems returns every reason a process would fail to publish that can be
// determined synchronously (without building/funding a tx or touching the chain): the structural
// checks, plus the plan/quota/permission denials that would otherwise only surface asynchronously
// as an opaque job failure. It is shared by GET .../check (reported as {valid,errors}) and by
// publish (enforced as a 400 before enqueueing). Funding and chain submission stay async.
func (a *API) publishPreflightProblems(
	vp *db.VotingProcess, questions []db.VotingProcessQuestion, census *db.Census, user *db.User,
) []string {
	problems := validateVotingProcessForPublish(vp, questions, census)
	if census == nil {
		return problems // plan checks below need the census
	}
	orgDoc, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		return append(problems, "organization not found")
	}
	if !user.HasRoleFor(vp.OrgAddress, db.AdminRole) {
		problems = append(problems, "publishing requires the admin role")
	}
	// per-question plan voting-type gate (skipped for raw ballot-protocol overrides)
	for i := range questions {
		if questions[i].BallotProtocol != nil {
			continue
		}
		if err := a.subscriptions.OrgAllowsVotingType(vp.OrgAddress, questions[i].Type); err != nil {
			problems = append(problems, err.Error())
		}
	}
	// the largest per-question census is the binding constraint for the plan MaxCensus cap;
	// process count/weighted/duration are process-level, so one call covers them all.
	var maxSize uint64
	for i := range questions {
		if s := account.ComputeMaxCensusSize(questions[i].EligibleMemberIDs, census.Size); s > maxSize {
			maxSize = s
		}
	}
	// an auth-only census with no members and no eligibility subsets has no voters; it would pass
	// the checks below but fail in the worker (electionParamsForQuestion). Reject it here.
	if maxSize == 0 {
		problems = append(problems, "census has no members")
		return problems
	}
	start := vp.StartDate
	if start.IsZero() || start.Before(time.Now()) {
		start = time.Now()
	}
	var durationSeconds uint32
	if vp.EndDate.After(start) {
		durationSeconds = uint32(vp.EndDate.Sub(start).Seconds())
	}
	if err := a.subscriptions.OrgCanPublishProcess(orgDoc, maxSize, durationSeconds, census.Weighted); err != nil {
		problems = append(problems, err.Error())
	}
	// managed orgs draw a process slot from the integrator's shared quota (read-only here). Key
	// this on census.Size — the same basis reserveManagedProcessSlot uses at publish — so the
	// dry-run and the real reservation agree.
	if orgDoc.ManagedBy != (common.Address{}) && uint64(census.Size) > uint64(db.TestMaxCensusSize) {
		if integrator, err := a.db.Organization(orgDoc.ManagedBy); err != nil {
			problems = append(problems, "integrator organization not found")
		} else if err := a.subscriptions.CanReserveManagedPublish(integrator); err != nil {
			problems = append(problems, err.Error())
		}
	}
	// email/SMS/vote allowance for the inline census. One auth (2FA challenge) per voter, but the
	// N questions are N elections so each voter can cast N ballots → votes scale with the question
	// count while notifications do not.
	notifyCount := int(census.Size)
	voteCount := int(census.Size) * len(questions)
	if err := a.subscriptions.OrgCanPublishCensus(census, notifyCount, voteCount); err != nil {
		problems = append(problems, err.Error())
	}
	return problems
}

// publishVotingProcessHandler godoc
//
//	@Summary		Publish a voting process
//	@Description	Publish a voting process: one on-chain election per question, submitted as a batch.
//	@Description	Requires Admin role (or a `voting:write` key). Returns 202 with a job id; poll
//	@Description	GET /jobs/{jobId}. Idempotent once published.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string	true	"Process ID"
//	@Success		202			{object}	apicommon.EnqueuedResponse
//	@Success		200			{object}	apicommon.CreateVotingProcessResponse	"Already published"
//	@Failure		400			{object}	errors.Error
//	@Failure		401			{object}	errors.Error
//	@Failure		404			{object}	errors.Error
//	@Failure		409			{object}	errors.Error
//	@Failure		503			{object}	errors.Error
//	@Router			/processes/{processId}/publish [post]
func (a *API) publishVotingProcessHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	vp, questions, err := a.db.ProcessWithQuestions(oid)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrProcessNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if !user.HasRoleFor(vp.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of the organization").Write(w)
		return
	}
	if vp.Published {
		apicommon.HTTPWriteJSON(w, apicommon.CreateVotingProcessResponse{ProcessID: oid.Hex()})
		return
	}
	census, err := a.db.Census(vp.CensusID.Hex())
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	// full synchronous publish-readiness gate (same as GET .../check): structural checks plus
	// plan voting-type, census-size, process-count, duration, managed-quota and email/SMS/vote
	// allowance. Anything predictable is a 400 here rather than an opaque async job failure;
	// only funding and chain submission are left to the worker.
	if problems := a.publishPreflightProblems(vp, questions, census, user); len(problems) > 0 {
		errors.ErrMalformedBody.Withf("process is not ready to publish: %s", strings.Join(problems, "; ")).Write(w)
		return
	}

	// atomically claim the process for publishing (duplicate-publish guard)
	claimed, err := a.db.ClaimVotingProcessForPublish(oid)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if !claimed {
		if cur, e := a.db.VotingProcess(oid); e == nil && cur.Published {
			apicommon.HTTPWriteJSON(w, apicommon.CreateVotingProcessResponse{ProcessID: oid.Hex()})
			return
		}
		errors.ErrPublishInProgress.Write(w)
		return
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if e := a.db.ClearVotingProcessPublishing(oid); e != nil {
			log.Warnw("could not clear voting process publishing state", "error", e)
		}
	}()

	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	// a process is one billed unit; reserve a single managed slot when applicable. A resume (a
	// re-publish of a process that already mined some elections) must NOT reserve again — the
	// original reservation from the first attempt still stands.
	nonTestSized := uint64(census.Size) > uint64(db.TestMaxCensusSize)
	var integratorAddr common.Address
	var managedReserved bool
	if !anyMined(questions) {
		integratorAddr, managedReserved, err = a.reserveManagedProcessSlot(org, uint64(census.Size))
		if err != nil {
			writeSubscriptionError(w, err)
			return
		}
	}
	if managedReserved {
		defer func() {
			if !managedReserved {
				return
			}
			if e := a.db.AddOrganizationManagedProcesses(integratorAddr, -1); e != nil {
				log.Warnw("could not roll back managed processes counter", "error", e)
			}
		}()
	}

	orgSigner, err := account.OrganizationSigner(a.secret, org.Creator, org.Nonce)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not restore organization signer: %v", err).Write(w)
		return
	}
	cspPubKey, err := a.csp.PubKey()
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not get csp public key: %v", err).Write(w)
		return
	}
	// publish the census (root = CSP public key); the on-chain census authorization is
	// delegated to the CSP for every question.
	census.Published = db.PublishedCensus{Root: cspPubKey, URI: a.serverURL, CreatedAt: time.Now()}
	if _, err := a.db.SetCensus(census); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	orgLock := a.orgTxLocks.lock(org.Address)
	lockHeld := true
	defer func() {
		if lockHeld {
			orgLock.Unlock()
		}
	}()

	jobID, err := apicommon.NewJobID()
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if err := a.db.CreateTxJob(jobID, db.JobTypePublishVotingProcess, org.Address); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	reserved := managedReserved
	worker := &publishWorker{
		a: a, vp: vp, questions: questions, census: census, org: org, user: user,
		orgSigner: orgSigner, cspPubKey: cspPubKey, integratorAddr: integratorAddr,
		reserved: reserved, nonTestSized: nonTestSized,
	}
	if !a.enqueueTx(txTask{jobID: jobID, run: func() (*db.JobResult, error) {
		defer orgLock.Unlock()
		return worker.run()
	}}) {
		if e := a.db.SetJobStatus(jobID, db.JobStatusFailed, nil, "tx queue full"); e != nil {
			log.Warnw("could not mark job failed after full queue", "error", e)
		}
		errors.ErrTxQueueFull.Write(w)
		return
	}
	// the worker now owns the lock, the publishing claim and the managed reservation.
	committed = true
	managedReserved = false
	lockHeld = false
	apicommon.HTTPWriteJSONStatus(w, http.StatusAccepted, &apicommon.EnqueuedResponse{JobID: jobID})
}

// publishWorker carries the state of an async voting-process publish across the batch +
// retry rounds executed on the tx worker pool.
type publishWorker struct {
	a              *API
	vp             *db.VotingProcess
	questions      []db.VotingProcessQuestion
	census         *db.Census
	org            *db.Organization
	user           *db.User
	orgSigner      *ethereum.SignKeys
	cspPubKey      []byte
	integratorAddr common.Address
	reserved       bool
	nonTestSized   bool
}

// run builds and submits one election per question in a single batch, confirms them on
// chain, and retries the not-yet-confirmed ones with fresh nonces up to maxPublishRounds.
// On success it marks the process published (one process counter unit); on failure it
// abandons the attempt (questions reset so a later publish regenerates them).
func (pw *publishWorker) run() (result *db.JobResult, err error) {
	a := pw.a
	// a panic mid-publish must not strand the publishing marker (or crash the process): recover,
	// abandon the attempt, and surface it as a normal job failure.
	defer func() {
		if r := recover(); r != nil {
			pw.abandon()
			result, err = nil, fmt.Errorf("publish worker panicked: %v", r)
		}
	}()
	confirmed := false
	for round := 0; round < maxPublishRounds && !confirmed; round++ {
		pending := make([]*db.VotingProcessQuestion, 0, len(pw.questions))
		for i := range pw.questions {
			if len(pw.questions[i].UpstreamID) == 0 {
				pending = append(pending, &pw.questions[i])
			}
		}
		if len(pending) == 0 {
			confirmed = true
			break
		}
		startNonce, err := a.account.AccountNonce(pw.vp.OrgAddress)
		if err != nil {
			pw.abandon()
			return nil, fmt.Errorf("could not read account nonce: %w", err)
		}
		stxs, ok, err := pw.buildBatch(pending, startNonce)
		if err != nil {
			pw.abandon()
			return nil, err
		}
		if !ok { // a permission/build error on a question: abandon (will regenerate)
			break
		}
		results, err := a.account.SubmitSignedTxBatch(stxs)
		if err != nil {
			log.Warnw("voting process batch submit failed, will retry", "error", err)
			continue
		}
		confirmed = pw.confirmBatch(pending, results)
	}
	if !confirmed {
		pw.abandon()
		return nil, fmt.Errorf("publish did not confirm all questions after %d rounds", maxPublishRounds)
	}
	if e := a.db.SetVotingProcessPublished(pw.vp.ID, pw.resolveStartDate()); e != nil {
		// every election is already on-chain and its question persisted; clear the marker so a
		// retry can re-run and simply re-mark the process published (pending is empty → no new
		// elections). Leaving it set would make the process permanently unclaimable.
		if ce := a.db.ClearVotingProcessPublishing(pw.vp.ID); ce != nil {
			log.Warnw("could not clear publishing marker after late publish failure", "error", ce)
		}
		return nil, e
	}
	if pw.nonTestSized {
		if e := a.db.IncrementOrganizationProcessesCounter(pw.vp.OrgAddress); e != nil {
			log.Warnw("could not update organization process counter", "error", e)
		}
	}
	return &db.JobResult{Status: "READY"}, nil //nolint:goconst
}

// resolveStartDate returns the start date to persist on the process once its elections are
// confirmed. An empty startDate becomes "start at the mined block" (StartTime=0) and a past
// startDate is moved to "now" at build time (see electionStartDuration), so in both cases the
// elections started at the block just mined and "now" is within seconds of the real start; a
// still-future requested date is kept as-is. The N per-question elections may even mine in
// different blocks (retry rounds), so a single exact chain date does not exist anyway.
func (pw *publishWorker) resolveStartDate() time.Time {
	if pw.vp.StartDate.After(time.Now()) {
		return pw.vp.StartDate
	}
	return time.Now()
}

// abandon rolls back a failed/aborted publish: it resets the not-yet-mined questions (so a later
// publish resumes them), clears the publishing marker, and releases the managed-process
// reservation only when nothing was mined this run. A partial-mine failure keeps the slot so the
// resume (which skips a new reservation) consumes it, avoiding a leak or a double-reserve.
func (pw *publishWorker) abandon() {
	a := pw.a
	if e := a.db.ResetQuestionsPublish(pw.vp.ID); e != nil {
		log.Warnw("could not reset questions after failed publish", "error", e)
	}
	if e := a.db.ClearVotingProcessPublishing(pw.vp.ID); e != nil {
		log.Warnw("could not clear publishing state after failed publish", "error", e)
	}
	if pw.reserved && !anyMined(pw.questions) {
		if e := a.db.AddOrganizationManagedProcesses(pw.integratorAddr, -1); e != nil {
			log.Warnw("could not roll back managed processes counter", "error", e)
		}
	}
}

// anyMined reports whether any question already has an on-chain election (upstreamId) — i.e. a
// prior publish attempt mined at least one, so this publish is a resume.
func anyMined(questions []db.VotingProcessQuestion) bool {
	for i := range questions {
		if len(questions[i].UpstreamID) > 0 {
			return true
		}
	}
	return false
}

// buildBatch builds, funds, permission-checks and signs a NEW_PROCESS tx per pending
// question with contiguous nonces starting at startNonce. ok is false when a question is
// not permitted (the attempt is abandoned rather than partially submitted).
func (pw *publishWorker) buildBatch(
	pending []*db.VotingProcessQuestion, startNonce uint32,
) (stxs [][]byte, ok bool, err error) {
	a := pw.a
	stxs = make([][]byte, 0, len(pending))
	for i, q := range pending {
		ep, err := electionParamsForQuestion(pw.vp, q, pw.census)
		if err != nil {
			return nil, false, err
		}
		metaBytes, err := account.BuildElectionMetadata(ep)
		if err != nil {
			return nil, false, err
		}
		objectName, err := a.objectStorage.PutJSON(metaBytes, pw.user.Email)
		if err != nil {
			return nil, false, err
		}
		q.MetadataURL = a.objectStorage.LocalURL(objectName)
		nonce := startNonce + uint32(i)
		tx, err := a.account.BuildNewProcessTx(&account.NewProcessParams{
			OrgAddress:  pw.vp.OrgAddress,
			Params:      ep,
			CensusRoot:  pw.cspPubKey,
			CensusURI:   a.serverURL,
			MetadataURL: q.MetadataURL,
			Nonce:       &nonce,
		})
		if err != nil {
			return nil, false, err
		}
		fundedTx, txType, err := a.account.FundTransaction(tx, pw.orgSigner.Address())
		if err != nil {
			return nil, false, err
		}
		if txType == nil || *txType != models.TxType_NEW_PROCESS {
			return nil, false, fmt.Errorf("unexpected tx type for publish")
		}
		if hasPerm, err := a.subscriptions.HasTxPermission(fundedTx, *txType, pw.org, pw.user); err != nil || !hasPerm {
			log.Warnw("voting process publish not permitted", "error", err)
			return nil, false, nil
		}
		stx, err := a.account.SignTransaction(fundedTx, pw.orgSigner)
		if err != nil {
			return nil, false, err
		}
		stxs = append(stxs, stx)
	}
	return stxs, true, nil
}

// confirmBatch waits for each submitted item to mine and persists the question's on-chain
// id. It returns true only when every pending question is confirmed.
func (pw *publishWorker) confirmBatch(pending []*db.VotingProcessQuestion, results []account.BatchItemResult) bool {
	a := pw.a
	// results are aligned to pending by position (SubmitSignedTxBatch's fail-fast ordering). If the
	// node ever returns a differently-sized batch, that positional binding is unsafe (it could
	// persist question A's id onto B), so treat the whole round as unconfirmed rather than trust it.
	if len(results) != len(pending) {
		log.Warnw("unexpected batch result count, will retry",
			"pending", len(pending), "results", len(results))
		return false
	}
	allConfirmed := true
	for i := range pending {
		if i >= len(results) {
			allConfirmed = false
			continue
		}
		res := results[i]
		if res.Status != account.BatchSubmitted {
			allConfirmed = false
			continue
		}
		if err := a.account.WaitTxMined(res.Hash); err != nil {
			log.Warnw("voting process election not confirmed, will retry", "error", err)
			allConfirmed = false
			continue
		}
		if err := a.db.SetQuestionPublished(
			pending[i].ID, res.UpstreamID, pending[i].MetadataURL, db.QuestionStatusReady,
		); err != nil {
			// leave UpstreamID unset so the question stays pending and is retried, rather
			// than letting the process be marked published with an unpersisted row.
			log.Warnw("could not persist published question", "error", err)
			allConfirmed = false
			continue
		}
		pending[i].UpstreamID = res.UpstreamID
	}
	return allConfirmed
}

// setVotingProcessQuestionsStatusHandler changes the on-chain status of many questions.
//
//	@Summary	Change status of many questions
//	@Tags		processes
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		processId	path		string								true	"Process ID"
//	@Param		request		body		apicommon.SetQuestionsStatusRequest	true	"Target status + questions"
//	@Success	202			{object}	apicommon.EnqueuedResponse
//	@Router		/processes/{processId}/questions/status [put]
func (a *API) setVotingProcessQuestionsStatusHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	var req apicommon.SetQuestionsStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	status, valid := parseProcessStatus(req.Status)
	if !valid {
		errors.ErrMalformedBody.Withf("invalid status").Write(w)
		return
	}
	vp, questions, ok := a.authorizeStatusChange(w, r, oid)
	if !ok {
		return
	}
	targets := selectStatusTargets(questions, req.Questions)
	a.enqueueStatusChange(w, vp, targets, status)
}

// setVotingProcessQuestionStatusHandler changes the on-chain status of one question.
//
//	@Summary	Change status of one question
//	@Tags		processes
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		processId	path		string								true	"Process ID"
//	@Param		questionId	path		string								true	"Question ID"
//	@Param		request		body		apicommon.SetProcessStatusRequest	true	"Target status"
//	@Success	202			{object}	apicommon.EnqueuedResponse
//	@Router		/processes/{processId}/questions/{questionId}/status [put]
func (a *API) setVotingProcessQuestionStatusHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	qid, err := primitive.ObjectIDFromHex(chi.URLParam(r, "questionId"))
	if err != nil {
		errors.ErrMalformedURLParam.Withf("invalid question ID").Write(w)
		return
	}
	var req apicommon.SetProcessStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	status, valid := parseProcessStatus(req.Status)
	if !valid {
		errors.ErrMalformedBody.Withf("invalid status").Write(w)
		return
	}
	vp, questions, ok := a.authorizeStatusChange(w, r, oid)
	if !ok {
		return
	}
	var targets []db.VotingProcessQuestion
	for i := range questions {
		if questions[i].ID == qid {
			targets = append(targets, questions[i])
		}
	}
	if len(targets) == 0 {
		errors.ErrProcessNotFound.Withf("question not found").Write(w)
		return
	}
	a.enqueueStatusChange(w, vp, targets, status)
}

// authorizeStatusChange loads the process + questions and checks the caller's role.
func (a *API) authorizeStatusChange(
	w http.ResponseWriter, r *http.Request, oid primitive.ObjectID,
) (*db.VotingProcess, []db.VotingProcessQuestion, bool) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return nil, nil, false
	}
	vp, questions, err := a.db.ProcessWithQuestions(oid)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrProcessNotFound.Write(w)
			return nil, nil, false
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return nil, nil, false
	}
	if !user.HasRoleFor(vp.OrgAddress, db.ManagerRole) && !user.HasRoleFor(vp.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Write(w)
		return nil, nil, false
	}
	return vp, questions, true
}

// selectStatusTargets returns the published questions matching the requested ids, or every
// published question when no ids are given.
func selectStatusTargets(
	questions []db.VotingProcessQuestion, ids []apicommon.QuestionStatusID,
) []db.VotingProcessQuestion {
	if len(ids) == 0 {
		return questions
	}
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id.ID] = true
	}
	var targets []db.VotingProcessQuestion
	for i := range questions {
		if wanted[questions[i].ID.Hex()] {
			targets = append(targets, questions[i])
		}
	}
	return targets
}

// enqueueStatusChange builds+submits a SET_PROCESS_STATUS tx per published target question
// on the tx worker pool, serialized under the org lock, and updates the stored status.
func (a *API) enqueueStatusChange(
	w http.ResponseWriter, vp *db.VotingProcess, targets []db.VotingProcessQuestion, status models.ProcessStatus,
) {
	published := make([]db.VotingProcessQuestion, 0, len(targets))
	for i := range targets {
		if len(targets[i].UpstreamID) > 0 {
			published = append(published, targets[i])
		}
	}
	if len(published) == 0 {
		errors.ErrMalformedBody.Withf("no published questions to update").Write(w)
		return
	}
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	orgSigner, err := account.OrganizationSigner(a.secret, org.Creator, org.Nonce)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not restore organization signer: %v", err).Write(w)
		return
	}
	jobID, err := apicommon.NewJobID()
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if err := a.db.CreateTxJob(jobID, db.JobTypeSetProcessStatus, org.Address); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	statusStr := strings.ToLower(status.String())
	orgLock := a.orgTxLocks.lock(org.Address)
	if !a.enqueueTx(txTask{jobID: jobID, run: func() (*db.JobResult, error) {
		defer orgLock.Unlock()
		for i := range published {
			tx, err := a.account.BuildSetProcessStatusTx(orgSigner.Address(), published[i].UpstreamID, status)
			if err != nil {
				return nil, err
			}
			fundedTx, _, err := a.account.FundTransaction(tx, orgSigner.Address())
			if err != nil {
				return nil, err
			}
			stx, err := a.account.SignTransaction(fundedTx, orgSigner)
			if err != nil {
				return nil, err
			}
			if _, err := a.account.SubmitSignedTx(stx); err != nil {
				return nil, err
			}
			if err := a.db.SetQuestionStatus(published[i].ID, statusStr); err != nil {
				log.Warnw("could not persist question status", "error", err)
			}
		}
		return &db.JobResult{Status: statusStr}, nil
	}}) {
		orgLock.Unlock()
		if e := a.db.SetJobStatus(jobID, db.JobStatusFailed, nil, "tx queue full"); e != nil {
			log.Warnw("could not mark job failed after full queue", "error", e)
		}
		errors.ErrTxQueueFull.Write(w)
		return
	}
	apicommon.HTTPWriteJSONStatus(w, http.StatusAccepted, &apicommon.EnqueuedResponse{JobID: jobID})
}

// parseProcessStatus maps a status string to the on-chain enum.
func parseProcessStatus(s string) (models.ProcessStatus, bool) {
	switch strings.ToLower(s) {
	case db.QuestionStatusReady:
		return models.ProcessStatus_READY, true
	case db.QuestionStatusPaused:
		return models.ProcessStatus_PAUSED, true
	case db.QuestionStatusEnded:
		return models.ProcessStatus_ENDED, true
	case db.QuestionStatusCanceled:
		return models.ProcessStatus_CANCELED, true
	default:
		return 0, false
	}
}
