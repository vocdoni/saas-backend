package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/proto/build/go/models"
)

// maxPublishRounds bounds how many times one publish attempt re-batches the not-yet-confirmed
// questions. Running out is a transient failure: the attempt is retried (publishRetryAttempts),
// keeping the questions already mined.
const maxPublishRounds = 3

// publishRetryAttempts bounds how many times the service retries a publish that failed for a
// transient reason (the chain or the tx queue, never the draft) before it gives up: abandons the
// attempt and credits the payer's wallet back.
const publishRetryAttempts = 5

// publishRetryBackoff is the delay before the first retry; each further retry doubles it (5s, 10s,
// 20s, 40s, 80s: about 2.5 minutes in all). The job stays pending meanwhile. A var so tests can
// shorten it.
var publishRetryBackoff = 5 * time.Second

// transientPublishError marks a publish failure that a later attempt may get past: the chain did
// not answer or confirm, or the tx queue was full. Every other failure comes from the draft or the
// server's own state and fails the publish at once.
type transientPublishError struct{ err error }

func (e transientPublishError) Error() string { return e.err.Error() }

func (e transientPublishError) Unwrap() error { return e.err }

// transient marks err as a transientPublishError.
func transient(err error) error { return transientPublishError{err: err} }

// errPublishClaimLost reports that the publishing marker this worker held is gone: released or
// reclaimed as stale. The process is no longer the worker's, so it stops without touching it.
var errPublishClaimLost = fmt.Errorf("publish claim lost")

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
// (processes whose marker is older than db.PublishStaleAfter), resets their questions so they
// can be published again, and credits back the payer's wallet debit for a publish
// that died before mining anything. Run at startup and once more when the markers a restart found
// still fresh have gone stale (see New); the claim path also reclaims stale markers as a second
// line of defense.
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
		// the publish it held died with the worker: a final failure, so the wallet debit
		// goes back (the marker is still held here, like a worker's abandon)
		if e := a.reversePublishDebit(id); e != nil {
			log.Errorw(e, "could not credit back the wallet debit of a stale publish; it stays a paid draft")
		}
		if e := a.db.ClearVotingProcessPublishing(id); e != nil {
			log.Warnw("could not clear stale publishing marker", "processId", id.Hex(), "error", e)
			continue
		}
		log.Infow("reconciled stale publishing process", "processId", id.Hex())
	}
}

// publishTarget is what a publish acts on: the process, its questions and census, and the
// user asking. The preflight and the publish that follows it take the same one.
type publishTarget struct {
	vp        *db.VotingProcess
	questions []db.VotingProcessQuestion
	census    *db.Census
	user      *db.User
}

// publishPreflightProblems returns every reason a process would fail to publish that can be
// determined synchronously (without building/funding a tx or touching the chain): the structural
// checks, plus the role/duration/integrator denials that would otherwise only surface asynchronously
// as an opaque job failure. It is shared by GET .../check (reported as {valid,errors}) and by
// publish (enforced before enqueueing). Funding and chain submission stay async.
//
// questionsMismatch singles out the one problem that is not the caller's request but the server's
// own data state — a stored question set that does not match the process. Publish answers that with
// a 409 rather than a 400; the dry-run ignores the flag and just reports the problem.
func (a *API) publishPreflightProblems(t publishTarget) (problems []string, questionsMismatch bool) {
	vp, questions, census, user := t.vp, t.questions, t.census, t.user
	problems = validateVotingProcessForPublish(vp, questions, census)
	if p := questionSetProblem(vp, questions); p != "" {
		problems = append(problems, p)
		questionsMismatch = true
	}
	if census == nil {
		return problems, questionsMismatch // the checks below need the census
	}
	orgDoc, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		return append(problems, "organization not found"), questionsMismatch
	}
	if !user.HasRoleFor(vp.OrgAddress, db.AdminRole) {
		problems = append(problems, "publishing requires the admin role")
	}
	// the largest per-question census: a process whose every question has no voter cannot publish
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
		return problems, questionsMismatch
	}
	start := vp.StartDate
	if start.IsZero() || start.Before(time.Now()) {
		start = time.Now()
	}
	var durationSeconds uint32
	if vp.EndDate.After(start) {
		durationSeconds = uint32(vp.EndDate.Sub(start).Seconds())
	}
	if err := a.subscriptions.OrgCanPublishProcess(orgDoc, durationSeconds); err != nil {
		problems = append(problems, err.Error())
	}
	return problems, questionsMismatch
}

// publishVotingProcessHandler godoc
//
//	@Summary		Publish a voting process
//	@Description	Publish a voting process: one on-chain election per question, submitted as a batch.
//	@Description	Requires Admin role (or a `voting:write` key). Returns 202 with a job id; poll
//	@Description	GET /jobs/{jobId}. Idempotent once published.
//	@Description	Transient failures (the chain not answering or confirming, a full tx queue) are retried
//	@Description	by the service up to 5 times over about 2.5 minutes; the job stays pending meanwhile,
//	@Description	so poll GET /jobs/{jobId} for at least 3 minutes before treating a publish as stuck.
//	@Description	If the publish finally fails before any election is mined, a failed publish credits the
//	@Description	payer's wallet back (a `creditBack` history row) and publishing again charges again.
//	@Description	409 (40172) means the stored questions do not match the process and the draft has to be
//	@Description	saved again before it can be published.
//	@Description	A priced process is paid from its payer's wallet (the organization's own, or its
//	@Description	integrator's when managed) at publish. 402 (40178) means the organization's wallet does
//	@Description	not cover it: the error data carries the quote, balanceCents and shortfallCents; top it
//	@Description	up via POST /processes/{processId}/checkout, which publishes once paid. For a managed
//	@Description	organization, 402 (40175) means its integrator's wallet does not cover the price.
//	@Description	422 (40176) means the census is above 50 000 voters: quote-only, no self-service price.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string	true	"Process ID"
//	@Success		202			{object}	apicommon.EnqueuedResponse
//	@Success		200			{object}	apicommon.CreateVotingProcessResponse	"Already published"
//	@Failure		400			{object}	errors.Error							"Not ready to publish"
//	@Failure		401			{object}	errors.Error
//	@Failure		402			{object}	errors.Error	"Wallet short (quote, balance, shortfall in data) or integrator wallet short"
//	@Failure		404			{object}	errors.Error
//	@Failure		409			{object}	errors.Error	"Publish in progress, questions out of sync, or a top-up checkout is open"
//	@Failure		422			{object}	errors.Error	"Census size requires a custom quote"
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
	// admin role, integrator top-level guard and the 90-day duration cap. Anything
	// predictable is rejected here rather than as an opaque async job failure;
	// only funding and chain submission are left to the worker.
	target := publishTarget{vp: vp, questions: questions, census: census, user: user}
	if problems, questionsMismatch := a.publishPreflightProblems(target); len(problems) > 0 {
		// a stored question set that does not match the process is server-side state, not a bad
		// request, so it answers 409. It wins over any other problem present: it has to be repaired
		// (by saving the draft again) before the rest of the draft is even worth looking at.
		apiErr := errors.ErrMalformedBody
		if questionsMismatch {
			apiErr = errors.ErrProcessQuestionsMismatch
		}
		apiErr.Withf("process is not ready to publish: %s", strings.Join(problems, "; ")).Write(w)
		return
	}

	// payment gate: a priced process publishes only when its payer's wallet covers what is due,
	// and the 402 carries what is missing so the client can go straight to a top-up checkout.
	// The debit itself happens inside startProcessPublish, after the publish claim.
	if err := a.paymentDueForPublish(vp); err != nil {
		writeSubscriptionError(w, err)
		return
	}

	jobID, err := a.startProcessPublish(target)
	if err != nil {
		if errors.Is(err, errProcessAlreadyPublished) {
			apicommon.HTTPWriteJSON(w, apicommon.CreateVotingProcessResponse{ProcessID: oid.Hex()})
			return
		}
		writeSubscriptionError(w, err)
		return
	}
	apicommon.HTTPWriteJSONStatus(w, http.StatusAccepted, &apicommon.EnqueuedResponse{JobID: jobID})
}

// errProcessAlreadyPublished reports that a publish request found the process already on
// chain — success for the caller, just nothing to enqueue.
var errProcessAlreadyPublished = fmt.Errorf("process already published")

// startProcessPublish runs the post-preflight publish pipeline: atomic claim,
// wallet debit, organization signer, census publication, job creation and enqueue.
// The caller is responsible for preflight (publishPreflightProblems) and authorization.
// It exists apart from the HTTP handler so payment fulfillment can trigger publication
// server-side with the same guarantees. Errors are errors.Error values carrying their
// HTTP semantics (write with writeSubscriptionError), plain errors mapping to 500, or
// errProcessAlreadyPublished.
func (a *API) startProcessPublish(t publishTarget) (string, error) {
	vp, questions, census, user := t.vp, t.questions, t.census, t.user
	oid := vp.ID
	// atomically claim the process for publishing (duplicate-publish guard)
	claimed, err := a.db.ClaimVotingProcessForPublish(oid)
	if err != nil {
		return "", fmt.Errorf("failed to claim voting process for publish: %w", err)
	}
	if !claimed {
		if cur, e := a.db.VotingProcess(oid); e == nil && cur.Published {
			return "", errProcessAlreadyPublished
		}
		return "", errors.ErrPublishInProgress
	}
	committed, debited := false, false
	defer func() {
		if committed {
			return
		}
		// a failure after the wallet paid is final for this request: the money goes back, and a
		// publish that is tried again charges again
		if debited {
			if e := a.reversePublishDebit(oid); e != nil {
				log.Errorw(e, "could not credit back the wallet debit of a failed publish; it stays a paid draft")
			}
		}
		if e := a.db.ClearVotingProcessPublishing(oid); e != nil {
			log.Warnw("could not clear voting process publishing state", "error", e)
		}
	}()
	// again under the claim, which a delete takes too: one that refunded and failed between
	// the payment gate and here would otherwise go on chain unpaid
	if err := a.refuseRefundedDraft(oid); err != nil {
		return "", err
	}

	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		return "", fmt.Errorf("failed to get organization: %w", err)
	}
	nonTestSized := uint64(census.Size) > uint64(db.TestMaxCensusSize)

	// payment: the process is charged to its payer's wallet here — the only debit of a
	// publish — after the claim, so two concurrent publishes cannot both debit. A publish that
	// then fails for good before mining anything — here, or in the worker once its retries run
	// out — credits the payer's wallet back (reversePublishDebit), and publishing again charges
	// again.
	if err := a.debitProcessWallet(vp, org); err != nil {
		return "", err
	}
	debited = true

	orgSigner, err := account.OrganizationSigner(a.secret, org.Creator, org.Nonce)
	if err != nil {
		return "", errors.ErrGenericInternalServerError.Withf("could not restore organization signer: %v", err)
	}
	// root = CSP public key; the on-chain census authorization is delegated to the CSP for every
	// question. A blind (anonymous) census publishes the CSP blind public key instead, so the
	// Vochain verifies each ballot's blind ECDSA proof against it under census origin OFF_CHAIN_CA_V2.
	cspPubKey, err := a.csp.PubKey()
	if census.Anonymous {
		cspPubKey, err = a.csp.BlindPubKey()
	}
	if err != nil {
		return "", errors.ErrGenericInternalServerError.Withf("could not get csp public key: %v", err)
	}
	census.Published = db.PublishedCensus{Root: cspPubKey, URI: a.serverURL, CreatedAt: time.Now()}
	if _, err := a.db.SetCensus(census); err != nil {
		return "", fmt.Errorf("failed to publish census: %w", err)
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
		return "", fmt.Errorf("failed to create job id: %w", err)
	}
	if err := a.db.CreateTxJob(jobID, db.JobTypePublishVotingProcess, org.Address); err != nil {
		return "", fmt.Errorf("failed to create tx job: %w", err)
	}

	worker := &publishWorker{
		a: a, vp: vp, questions: questions, census: census, org: org, user: user,
		orgSigner: orgSigner, cspPubKey: cspPubKey, nonTestSized: nonTestSized, jobID: jobID,
	}
	// the worker now owns the lock and the publishing claim, including through a full queue:
	// that is retried like any transient failure, with the job pending meanwhile.
	committed = true
	lockHeld = false
	worker.submit(orgLock)
	return jobID, nil
}

// submit hands one publish attempt to the tx queue. orgLock is held on entry and released by
// the task once it has run, or right away when the queue is full, which is a transient failure:
// finish schedules the next attempt. The lock is never held across the retry delay, so other
// publishes and status changes of the organization proceed meanwhile.
func (pw *publishWorker) submit(orgLock *sync.Mutex) {
	if pw.a.enqueueTx(txTask{jobID: pw.jobID, run: func() (*db.JobResult, error) {
		defer orgLock.Unlock()
		return pw.run()
	}, record: pw.finish}) {
		return
	}
	orgLock.Unlock()
	pw.finish(nil, transient(errors.ErrTxQueueFull))
}

// retry runs the next attempt after its backoff. It takes the organization's tx lock first, like
// any publish, on its own goroutine — never a tx worker's, which must not wait on a lock that a
// task still queued behind it may hold.
func (pw *publishWorker) retry() {
	pw.submit(pw.a.orgTxLocks.lock(pw.org.Address))
}

// finish records one attempt's outcome (it is the attempt's txTask.record, so a panic the worker
// pool recovers lands here too). Success completes the job. A transient failure with attempts
// left schedules the next one, the job staying pending and the claim held. Anything else, or the
// last attempt failing, abandons the publish — crediting the payer's wallet back —
// and fails the job; a lost claim only fails it, since the process is someone else's now.
func (pw *publishWorker) finish(result *db.JobResult, err error) {
	a := pw.a
	if err == nil {
		if e := a.db.SetJobStatus(pw.jobID, db.JobStatusCompleted, result, ""); e != nil {
			log.Warnw("could not record completed job", "jobId", pw.jobID, "error", e)
		}
		return
	}
	var te transientPublishError
	if errors.As(err, &te) && pw.attempt < publishRetryAttempts {
		pw.attempt++
		delay := publishRetryBackoff << (pw.attempt - 1)
		log.Warnw("voting process publish failed, retrying", "processId", pw.vp.ID.Hex(),
			"attempt", pw.attempt, "delay", delay.String(), "error", err)
		time.AfterFunc(delay, pw.retry)
		return
	}
	if !errors.Is(err, errPublishClaimLost) {
		pw.abandon()
	}
	if e := a.db.SetJobStatus(pw.jobID, db.JobStatusFailed, nil, err.Error()); e != nil {
		log.Warnw("could not record failed job", "jobId", pw.jobID, "error", e)
	}
}

// publishWorker carries the state of an async voting-process publish across the batch +
// retry rounds executed on the tx worker pool.
type publishWorker struct {
	a            *API
	vp           *db.VotingProcess
	questions    []db.VotingProcessQuestion
	census       *db.Census
	org          *db.Organization
	user         *db.User
	orgSigner    *ethereum.SignKeys
	cspPubKey    []byte
	nonTestSized bool
	jobID        string
	// attempt counts the retries already scheduled (0 on the first attempt)
	attempt int
}

// run is one publish attempt: it builds and submits one election per question in a single
// batch, confirms them on chain, and resubmits the not-yet-confirmed ones with fresh nonces up to
// maxPublishRounds. On success it marks the process published (one process counter unit). A
// failure is returned, marked transient when a later attempt may get past it, and finish decides
// between retrying and abandoning; run itself never abandons.
func (pw *publishWorker) run() (result *db.JobResult, err error) {
	a := pw.a
	// a panic mid-publish must not crash the process: surface it as a permanent failure, which
	// finish abandons like any other.
	defer func() {
		if r := recover(); r != nil {
			result, err = nil, fmt.Errorf("publish worker panicked: %v", r)
		}
	}()
	if a.publishFault != nil {
		if err := a.publishFault(); err != nil {
			return nil, err
		}
	}
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
		// keep the claim fresh for as long as this publish works on it: a round, plus the
		// retries in between, would otherwise outlive PublishStaleAfter and be reclaimed
		if err := pw.refreshClaim(); err != nil {
			return nil, err
		}
		startNonce, err := a.account.AccountNonce(pw.vp.OrgAddress)
		if err != nil {
			return nil, transient(fmt.Errorf("could not read account nonce: %w", err))
		}
		stxs, ok, err := pw.buildBatch(pending, startNonce)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("voting process publish not permitted")
		}
		results, err := a.account.SubmitSignedTxBatch(stxs)
		if err != nil {
			log.Warnw("voting process batch submit failed, will retry", "error", err)
			continue
		}
		if err := pw.refreshClaim(); err != nil {
			return nil, err
		}
		confirmed = pw.confirmBatch(pending, results)
	}
	if !confirmed {
		return nil, transient(fmt.Errorf("publish did not confirm all questions after %d rounds", maxPublishRounds))
	}
	if e := a.db.SetVotingProcessPublished(pw.vp.ID, pw.resolveStartDate()); e != nil {
		// every election is already on-chain and its question persisted: the next attempt finds
		// nothing pending and only marks the process published again
		return nil, transient(e)
	}
	// the debit lets a process through without a paid payment only when it priced at zero,
	// so one that has none was published free: record that as its envelope, or its census
	// could later grow into a priced size without anything to charge against
	if _, e := a.db.SetProcessPaymentEnvelope(pw.vp.ID, pw.vp.OrgAddress, 0); e != nil {
		log.Warnw("could not record free process payment", "processId", pw.vp.ID.Hex(), "error", e)
	}
	if pw.nonTestSized {
		if e := a.db.IncrementOrganizationProcessesCounter(pw.vp.OrgAddress); e != nil {
			log.Warnw("could not update organization process counter", "error", e)
		}
	}
	status := db.QuestionStatusReady
	if pw.vp.InitialStatus == db.QuestionStatusPaused {
		status = db.QuestionStatusPaused
	}
	return &db.JobResult{Status: status}, nil
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

// refreshClaim moves the publishing marker forward, reporting errPublishClaimLost when it is no
// longer held. A failed write is transient: the marker is still ours, just not refreshed.
func (pw *publishWorker) refreshClaim() error {
	held, err := pw.a.db.RefreshVotingProcessPublishing(pw.vp.ID)
	if err != nil {
		return transient(err)
	}
	if !held {
		return errPublishClaimLost
	}
	return nil
}

// abandon rolls back a publish that failed for good: it resets the not-yet-mined questions (so a
// later publish resumes them), credits the payer's wallet debit back when nothing was
// mined, and clears the publishing marker. A credit-back that fails is logged and the marker
// cleared anyway: the draft then stays paid, the state a delete refunds and a publish reuses.
func (pw *publishWorker) abandon() {
	a := pw.a
	if e := a.db.ResetQuestionsPublish(pw.vp.ID); e != nil {
		log.Warnw("could not reset questions after failed publish", "error", e)
	}
	if e := a.reversePublishDebit(pw.vp.ID); e != nil {
		log.Errorw(e, "could not credit back the wallet debit of a failed publish; it stays a paid draft")
	}
	if e := a.db.ClearVotingProcessPublishing(pw.vp.ID); e != nil {
		log.Warnw("could not clear publishing state after failed publish", "error", e)
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
	initialStatus, err := account.ParseInitialStatus(pw.vp.InitialStatus)
	if err != nil {
		return nil, false, err
	}
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
			OrgAddress:    pw.vp.OrgAddress,
			Params:        ep,
			CensusRoot:    pw.cspPubKey,
			CensusURI:     a.serverURL,
			Anonymous:     pw.census.Anonymous,
			MetadataURL:   q.MetadataURL,
			Nonce:         &nonce,
			InitialStatus: initialStatus,
		})
		if err != nil {
			return nil, false, err
		}
		fundedTx, txType, err := a.account.FundTransaction(tx, pw.orgSigner.Address())
		if err != nil {
			return nil, false, transient(err) // funding reads the chain and the faucet
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
		initialStatus := db.QuestionStatusReady
		if pw.vp.InitialStatus == db.QuestionStatusPaused {
			initialStatus = db.QuestionStatusPaused
		}
		if err := a.db.SetQuestionPublished(
			pending[i].ID, res.UpstreamID, pending[i].MetadataURL, initialStatus,
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
	qid, err := bson.ObjectIDFromHex(chi.URLParam(r, "questionId"))
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
	w http.ResponseWriter, r *http.Request, oid bson.ObjectID,
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
	// stored uppercase to match the vochain (status.String() is already uppercase).
	statusStr := status.String()
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
			// confirm the change landed on-chain in the background, correcting the optimistic
			// write above if the tx never reaches the requested status.
			a.enqueueConfirm(published[i].UpstreamID, statusStr)
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

// parseProcessStatus maps a status string to the on-chain enum. Input is accepted case-insensitively
// (upper-cased to match the uppercase QuestionStatus* constants).
func parseProcessStatus(s string) (models.ProcessStatus, bool) {
	switch strings.ToUpper(s) {
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
