package api

import (
	"context"
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
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/pricing"
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
//
// The backoff is not the job's whole duration: each of the 6 attempts runs up to maxPublishRounds
// rounds, and a round waits up to 40 s per question (account.WaitTxMined) on a chain that does not
// mine. The worst case is 6 × 3 × 40 s × questions + the backoff: ~15 minutes for one question, over
// 3 hours for MaxQuestionsPerProcess. Not bounded on purpose: the claim is renewed throughout, and
// cutting it short would only turn a slow publish into a failed one.
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
	claims, err := a.db.StaleVotingProcesses()
	if err != nil {
		log.Warnw("could not scan for stale publishing processes", "error", err)
		return
	}
	for _, claim := range claims {
		a.reconcileStaleClaim(claim)
	}
}

// reconcileStaleClaim takes over one stale claim found by StaleVotingProcesses, resets the
// process's unmined questions, credits back what nothing mined and clears the claim.
func (a *API) reconcileStaleClaim(claim db.StalePublishClaim) {
	id := claim.ID
	// take over only the claim the scan found, and only while it is still stale: a sibling
	// replica or a publish may have reclaimed the process since, and its fresh claim must
	// survive. Holding the claim, no publish can start on the process while its questions are
	// reset and its payment reverted.
	owner, reclaimed, e := a.db.ReclaimStalePublishClaim(claim)
	if e != nil {
		log.Warnw("could not reclaim stale publishing marker", "processId", id.Hex(), "error", e)
		return
	}
	if !reclaimed {
		return
	}
	// mined questions keep their election ids: this only resets the unmined ones
	if e := a.db.ResetQuestionsPublish(id); e != nil {
		log.Warnw("could not reset questions of stale publishing process", "processId", id.Hex(), "error", e)
	}
	// the publish it held died with the worker: a final failure, so the wallet debit goes back
	if e := a.reversePublishDebit(id); e != nil {
		log.Errorw(e, "could not credit back the wallet debit of a stale publish; "+
			"it stays paid, or owes the credit to its next publish, checkout or delete")
	}
	if e := a.db.ClearVotingProcessPublishing(id, owner); e != nil {
		log.Warnw("could not release the stale publish reconcile claim", "processId", id.Hex(), "error", e)
	}
	log.Infow("reconciled stale publishing process", "processId", id.Hex())
}

// publishTarget is what a publish acts on: the process, its questions and census, and the
// user asking. The preflight and the publish that follows it take the same one.
type publishTarget struct {
	vp        *db.VotingProcess
	questions []db.VotingProcessQuestion
	census    *db.Census
	user      *db.User
	// ceilingCents, when positive, is the most the publish debit may charge (debitProcessWalletUpTo)
	ceilingCents pricing.Cents
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
//
// denied is the organization's typed refusal (OrgCanPublishProcess: the integrator's own org,
// 40174, or the 90-day duration cap), also listed in problems. Publish answers it as that error,
// as draft creation does, rather than folding it into a 400.
func (a *API) publishPreflightProblems(t publishTarget) (problems []string, questionsMismatch bool, denied error) {
	vp, questions, census, user := t.vp, t.questions, t.census, t.user
	problems = validateVotingProcessForPublish(vp, questions, census)
	if p := questionSetProblem(vp, questions); p != "" {
		problems = append(problems, p)
		questionsMismatch = true
	}
	if census == nil {
		return problems, questionsMismatch, nil // the checks below need the census
	}
	// a resume must still mint the remaining questions of a partially published anonymous process
	if a.anonymousCensusRefused(census.Anonymous) && !anyMined(questions) {
		problems = append(problems, anonymousVotingUnavailable)
	}
	orgDoc, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		return append(problems, "organization not found"), questionsMismatch, nil
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
		return problems, questionsMismatch, nil
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
		denied = err
	}
	return problems, questionsMismatch, denied
}

// preflightError is the refusal publish answers for what publishPreflightProblems found: 409
// (40172) when the stored question set does not match the process, which has to be repaired
// first; then the organization's typed refusal (403 40174, or the duration cap), as at draft
// creation; and a 400 for anything else. Nil when nothing was found.
func preflightError(problems []string, questionsMismatch bool, denied error) error {
	if len(problems) == 0 {
		return nil
	}
	switch {
	case questionsMismatch:
		return errors.ErrProcessQuestionsMismatch.Withf("process is not ready to publish: %s", strings.Join(problems, "; "))
	case denied != nil:
		return denied
	default:
		return errors.ErrMalformedBody.Withf("process is not ready to publish: %s", strings.Join(problems, "; "))
	}
}

// publishVotingProcessHandler godoc
//
//	@Summary		Publish a voting process
//	@Description	Publish a voting process: one on-chain election per question, submitted as a batch.
//	@Description	Requires Admin role (or a `voting:write` key). Returns 202 with a job id; poll
//	@Description	GET /jobs/{jobId}. Idempotent once published.
//	@Description	Transient failures (the chain not answering or confirming, a full tx queue) are retried
//	@Description	by the service up to 5 times, with about 2.5 minutes of backoff between attempts; the job
//	@Description	stays pending meanwhile. Each attempt runs up to 3 rounds, each waiting up to 40 s per
//	@Description	question for its election to be mined, so on a stalled chain the job can stay pending for
//	@Description	6 attempts × 3 rounds × 40 s × questions + 2.5 minutes: about 15 minutes for one question,
//	@Description	over 3 hours for 15. Poll GET /jobs/{jobId} until it is completed or failed.
//	@Description	If the publish finally fails before any election is mined, a failed publish credits the
//	@Description	payer's wallet back (a `creditBack` history row) and publishing again charges again.
//	@Description	409 (40172) means the stored questions do not match the process and the draft has to be
//	@Description	saved again before it can be published.
//	@Description	400 (40004) "process is not ready to publish: ..." lists every problem found, including
//	@Description	"anonymous voting is temporarily unavailable" for an anonymous process while the service has
//	@Description	anonymous voting off. A process some of whose questions are already on chain still resumes.
//	@Description	409 (40177) while a top-up checkout opened for the draft is still open: pay it, or, when
//	@Description	the draft's price changed since, call POST /processes/{processId}/checkout again to
//	@Description	replace it (or cancel it with DELETE /processes/{processId}/checkout) and publish again.
//	@Description	A priced process is paid from its payer's wallet (the organization's own, or its
//	@Description	integrator's when managed) at publish. 402 (40178) means the organization's wallet does
//	@Description	not cover it: the error data carries the quote, balanceCents and shortfallCents; top it
//	@Description	up via POST /processes/{processId}/checkout, which publishes once paid. A draft that already
//	@Description	paid part of its price (its census grew since) gets the data's paidCents and censusSize
//	@Description	too, and buys the difference with POST /processes/{processId}/census/checkout. For a managed
//	@Description	organization, 402 (40175) means its integrator's wallet does not cover the price (data:
//	@Description	requiredCents, availableCents), and 402 (40183) that the price would take it past the
//	@Description	spend cap its integrator set (data: capCents, spentCents, requiredCents).
//	@Description	409 (40177) also answers while another draft is paying for a once-per-organization add-on
//	@Description	(branding, custom URL) this one selects, and while it selects one its payment does not
//	@Description	include that the organization has not paid for either: deselect it, or publish once
//	@Description	another process pays for it.
//	@Description	422 (40176) means the census is above 50 000 voters: quote-only, no self-service price.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string	true	"Process ID"
//	@Success		202			{object}	apicommon.EnqueuedResponse
//	@Success		200			{object}	apicommon.CreateVotingProcessResponse	"Already published"
//	@Failure		400			{object}	errors.Error							"Not ready to publish (lists every problem)"
//	@Failure		403			{object}	errors.Error							"An integrator's own organization cannot own processes (40174)"
//	@Failure		401			{object}	errors.Error
//	@Failure		402			{object}	errors.Error{data=apicommon.ProcessPaymentRequired}	"Wallet short or spend cap reached"
//	@Failure		404			{object}	errors.Error
//	@Failure		409			{object}	errors.Error	"Publish in progress, out of sync, checkout open, add-on held, org being deleted (40901)"
//	@Failure		422			{object}	errors.Error	"Census size requires a custom quote"
//	@Failure		503			{object}	errors.Error	"Busy (50302 wallet or census growth, 50303 org tx), retry after Retry-After"
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
	if err := preflightError(a.publishPreflightProblems(target)); err != nil {
		writeSubscriptionError(w, err)
		return
	}

	// payment gate: a priced process publishes only when its payer's wallet covers what is due,
	// and the 402 carries what is missing so the client can go straight to a top-up checkout.
	// The debit itself happens inside startProcessPublish, after the publish claim.
	if err := a.paymentDueForPublish(vp); err != nil {
		writeSubscriptionError(w, err)
		return
	}

	jobID, err := a.startProcessPublish(r.Context(), target)
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

// errPublishFailedAfterDebit is a publish request that failed after the payer's wallet paid for
// it and before its job existed: the debit was credited back and the draft stays unpublished.
// Over HTTP it answers as the error it wraps. The top-up webhook records it as the payment's
// refusal and answers 200: Stripe redelivering would find the payment failed and publish nothing.
type errPublishFailedAfterDebit struct{ err error }

func (e errPublishFailedAfterDebit) Error() string {
	return "publish failed after the payment, which was credited back: " + e.err.Error()
}

func (e errPublishFailedAfterDebit) Unwrap() error { return e.err }

// startProcessPublish runs the post-preflight publish pipeline: atomic claim,
// wallet debit, organization signer, census publication, job creation and enqueue.
// The caller is responsible for preflight (publishPreflightProblems) and authorization.
// It exists apart from the HTTP handler so payment fulfillment can trigger publication
// server-side with the same guarantees. Errors are errors.Error values carrying their
// HTTP semantics (write with writeSubscriptionError), plain errors mapping to 500, or
// errProcessAlreadyPublished. ctx bounds only the lock waits below, never the enqueued work.
func (a *API) startProcessPublish(ctx context.Context, t publishTarget) (string, error) {
	vp, user := t.vp, t.user
	oid := vp.ID
	// atomically claim the process for publishing (duplicate-publish guard). The claim is
	// conditional on the process not having been updated since this snapshot was read
	// (vp.UpdatedAt), so a draft edit racing the publish cannot result in an old snapshot
	// going on chain.
	owner, claimed, err := a.db.ClaimVotingProcessForPublish(oid, vp.UpdatedAt)
	if err != nil {
		return "", fmt.Errorf("failed to claim voting process for publish: %w", err)
	}
	if !claimed {
		if cur, e := a.db.VotingProcess(oid); e == nil {
			switch {
			case cur.Published:
				return "", errProcessAlreadyPublished
			case cur.PublishInProgress():
				return "", errors.ErrPublishInProgress
			case !cur.UpdatedAt.Equal(vp.UpdatedAt):
				return "", errors.ErrStaleUpdate.Withf("process changed after it was read; reload it and publish again")
			}
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
				log.Errorw(e, "could not credit back the wallet debit of a failed publish; "+
					"it stays paid, or owes the credit to its next publish, checkout or delete")
			}
		}
		if e := a.db.ClearVotingProcessPublishing(oid, owner); e != nil {
			log.Warnw("could not clear voting process publishing state", "error", e)
		}
	}()
	// under the claim: a teardown that started before it refuses the publish here, and one that
	// starts after finds the draft claimed and stops (see deleteManagedOrganizationHandler)
	if err := a.refuseTearingDown(vp.OrgAddress); err != nil {
		return "", err
	}
	// again under the claim, which a delete takes too: one that refunded and failed between
	// the payment gate and here would otherwise go on chain unpaid
	if err := a.refuseRefundedDraft(vp); err != nil {
		return "", err
	}
	// the snapshot's questions were loaded before the claim; a draft edit in that window bumped
	// updatedAt and was caught above, but reload them under the claim anyway so the published
	// elections can only be built from what is stored now.
	questions, err := a.db.QuestionsByProcess(oid)
	if err != nil {
		return "", fmt.Errorf("failed to reload process questions: %w", err)
	}
	if p := questionSetProblem(vp, questions); p != "" {
		return "", errors.ErrProcessQuestionsMismatch.Withf("%s", p)
	}

	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		return "", fmt.Errorf("failed to get organization: %w", err)
	}

	// the census is read again under the organization's census growth lock, held until it is
	// written back below: that one read is what the debit prices, what is stored and what the job
	// publishes (maxCensusSize), so a member added concurrently is in all three or lands after
	// them. Bounded like the growth paths (503 50302), and taken before the tx lock, in their order:
	// they hold it while they may take the tx lock.
	growthLock, err := a.censusGrowthLocks.lockOrBusy(ctx, org.Address)
	if err != nil {
		return "", err
	}
	releaseGrowth := sync.OnceFunc(growthLock.Unlock)
	defer releaseGrowth()
	census, err := a.db.Census(vp.CensusID.Hex())
	if err != nil {
		return "", fmt.Errorf("failed to reload the census: %w", err)
	}
	nonTestSized := uint64(census.Size) > uint64(db.TestMaxCensusSize)

	// acquire the per-org tx lock with a bounded wait before anything is charged: a worker may hold
	// it through tx mining, and a caller is better served by a prompt 503 than a parked request. A
	// busy lock so refuses the publish with nothing debited, and nothing to give back.
	orgLock, err := a.orgTxLocks.lockCtx(ctx, org.Address)
	if err != nil {
		return "", err
	}
	lockHeld := true
	defer func() {
		if lockHeld {
			orgLock.Unlock()
		}
	}()

	// payment: the process is charged to its payer's wallet here — the only debit of a
	// publish — after the claim, so two concurrent publishes cannot both debit. A publish that
	// then fails for good before mining anything — here, or in the worker once its retries run
	// out — credits the payer's wallet back (reversePublishDebit), and publishing again charges
	// again.
	if err := a.debitProcessWalletUpTo(publishDebit{vp: vp, org: org, census: census, ceilingCents: t.ceilingCents}); err != nil {
		return "", err
	}
	debited = true
	// from here on a failure is credited back by the deferred reversal: reported apart, so the
	// top-up webhook records it as the payment's refusal instead of having Stripe redeliver
	afterDebit := func(err error) (string, error) { return "", errPublishFailedAfterDebit{err: err} }
	if a.publishPreJobFault != nil {
		if err := a.publishPreJobFault(); err != nil {
			return afterDebit(err)
		}
	}

	orgSigner, err := account.OrganizationSigner(a.secret, org.SignerSeedValue(), org.Nonce)
	if err != nil {
		return afterDebit(errors.ErrGenericInternalServerError.Withf("could not restore organization signer: %v", err))
	}
	// root = CSP public key; the on-chain census authorization is delegated to the CSP for every
	// question. A blind (anonymous) census publishes the CSP blind public key instead, so the
	// Vochain verifies each ballot's blind ECDSA proof against it under census origin OFF_CHAIN_CA_V2.
	cspPubKey, err := a.csp.PubKey()
	if census.Anonymous {
		cspPubKey, err = a.csp.BlindPubKey()
	}
	if err != nil {
		return afterDebit(errors.ErrGenericInternalServerError.Withf("could not get csp public key: %v", err))
	}
	census.Published = db.PublishedCensus{Root: cspPubKey, URI: a.serverURL, CreatedAt: time.Now()}
	if _, err := a.db.SetCensus(census); err != nil {
		return afterDebit(fmt.Errorf("failed to publish census: %w", err))
	}
	// stored and fixed in the worker's snapshot: a member added from here on is growth of this
	// process, which the growth guard prices once its elections take votes
	releaseGrowth()

	jobID, err := apicommon.NewJobID()
	if err != nil {
		return afterDebit(fmt.Errorf("failed to create job id: %w", err))
	}
	// the job names its process, so GET /jobs finds it and a failure (its errors) is traced to it
	if err := a.db.CreateTxJobWithResult(jobID, db.JobTypePublishVotingProcess, org.Address,
		&db.JobResult{ProcessID: internal.HexBytes(vp.ID[:])}); err != nil {
		return afterDebit(fmt.Errorf("failed to create tx job: %w", err))
	}
	// and the payment names the job: the top-up webhook's publish has no response to return it in
	if err := a.db.SetProcessPaymentPublishJob(vp.ID, jobID); err != nil {
		log.Warnw("could not record the publish job on the process payment", "processId", vp.ID.Hex(),
			"jobId", jobID, "error", err)
	}

	worker := &publishWorker{
		a: a, vp: vp, questions: questions, census: census, org: org, user: user,
		orgSigner: orgSigner, cspPubKey: cspPubKey, nonTestSized: nonTestSized, jobID: jobID, owner: owner,
		minedUnpersisted: make(map[bson.ObjectID]internal.HexBytes),
		staleAfter:       db.PublishStaleAfter,
	}
	// the worker now owns the lock and the publishing claim, including through a full queue:
	// that is retried like any transient failure, with the job pending meanwhile.
	committed = true
	lockHeld = false
	// renewed while the first attempt's task waits in the queue, until run takes over
	worker.holdClaim()
	worker.submit(orgLock)
	return jobID, nil
}

// submit hands one publish attempt to the tx queue. orgLock is held on entry and released by
// the task once it has run, or right away when the queue is full, which is a transient failure:
// finish schedules the next attempt. The lock is never held across the retry delay, so other
// publishes and status changes of the organization proceed meanwhile.
func (pw *publishWorker) submit(orgLock *orgTxLock) {
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
	// the hold finish took for the backoff ends once the attempt is handed over
	defer pw.a.heldTxJobs.release(pw.jobID)
	pw.submit(pw.a.orgTxLocks.lock(pw.org.Address))
}

// finish records one attempt's outcome (it is the attempt's txTask.record, so a panic the worker
// pool recovers lands here too). Success completes the job. A transient failure with attempts
// left schedules the next one, the job staying pending and the claim held. Anything else, or the
// last attempt failing, abandons the publish — crediting the payer's wallet back —
// and fails the job; a lost claim only fails it, since the process is someone else's now, or, when
// it went stale under this worker, the stale sweep's (standDown).
func (pw *publishWorker) finish(result *db.JobResult, err error) {
	a := pw.a
	var te transientPublishError
	if err != nil && errors.As(err, &te) && pw.attempt < publishRetryAttempts {
		pw.attempt++
		delay := publishRetryBackoff << (pw.attempt - 1)
		log.Warnw("voting process publish failed, retrying", "processId", pw.vp.ID.Hex(),
			"attempt", pw.attempt, "delay", delay.String(), "error", err)
		// the job stays held through the backoff, so its heartbeat keeps it from being swept
		// as interrupted (superviseTxJobs) while no queue holds it
		a.heldTxJobs.hold(pw.jobID)
		// and the claim stays fresh through the backoff and the retry's wait for the org tx lock,
		// or the growth paths would stop answering 409 while the next attempt still mines from
		// the census stored before them
		pw.holdClaim()
		time.AfterFunc(delay, pw.retry)
		return
	}
	// the worker ends here: nothing is left to keep the claim for
	pw.releaseClaimHold()
	if err == nil {
		if e := a.db.SetJobStatus(pw.jobID, db.JobStatusCompleted, result, ""); e != nil {
			log.Warnw("could not record completed job", "jobId", pw.jobID, "error", e)
		}
		return
	}
	if errors.Is(err, errPublishClaimLost) {
		pw.flushOrReportMined()
		pw.standDown()
	} else {
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
	// owner is the publish-claim token returned by ClaimVotingProcessForPublish: the worker
	// renews the claim with it while it runs and every state write it does on the process
	// (clear, publish) is conditional on still holding it.
	owner string
	// minedUnpersisted maps a question id to the election id the chain confirmed for it when
	// SetQuestionPublished failed afterwards. Those questions are never rebuilt or resubmitted
	// (the election already exists — resubmitting would mint a duplicate); only the DB write is
	// retried, until it succeeds or the attempt is abandoned.
	minedUnpersisted map[bson.ObjectID]internal.HexBytes
	// submitted is set right before any attempt (run) of this worker submits a batch, and stays set
	// across retries: those txs may mine whatever happens to the claim afterwards, even after a later
	// attempt failed, so the worker can no longer tell that nothing went on chain
	submitted bool
	// staleAfter is db.PublishStaleAfter as the worker was created with: its renewals use this
	// window, so changing the package variable never affects a worker already running
	staleAfter time.Duration
	// claimHold stops the renewal that keeps the claim fresh while no attempt runs (holdClaim)
	claimHoldMu sync.Mutex
	claimHold   chan struct{}
}

// holdClaim keeps the publish claim fresh while no attempt runs: while an attempt's task waits
// in the tx queue, through a retry's backoff and while the retry waits for the org tx lock. run
// stops it once its own renewal takes over, and the worker's end (finish) stops it for good.
// Starting it while it runs is a no-op.
func (pw *publishWorker) holdClaim() {
	pw.claimHoldMu.Lock()
	defer pw.claimHoldMu.Unlock()
	if pw.claimHold == nil {
		pw.claimHold = make(chan struct{})
		go pw.renewClaim(pw.claimHold)
	}
}

// releaseClaimHold stops the renewal holdClaim started, if it runs.
func (pw *publishWorker) releaseClaimHold() {
	pw.claimHoldMu.Lock()
	defer pw.claimHoldMu.Unlock()
	if pw.claimHold != nil {
		close(pw.claimHold)
		pw.claimHold = nil
	}
}

// standDown ends a worker whose claim is no longer its own. When the claim went stale under it and
// the worker knows nothing of its publish is on chain (none of its attempts submitted anything and
// every election it saw mine is recorded: call flushOrReportMined first), it reconciles the claim
// as the stale sweep would (reconcileOwnStaleClaim). Otherwise it credits nothing back: the txs it submitted may mine,
// so the stale claim is left to the startup sweep or the next publish, checkout or delete, which
// can only go by what is stored (the accepted crash limit, #822).
func (pw *publishWorker) standDown() {
	if len(pw.minedUnpersisted) == 0 && !pw.submitted {
		pw.reconcileOwnStaleClaim()
		return
	}
	log.Errorw(fmt.Errorf("publish claim lost with elections possibly on chain"),
		fmt.Sprintf("process %s stood down after its publish submitted or mined elections (submitted: %t, "+
			"unrecorded: %d); nothing is credited back and its stale claim is left to the stale sweep",
			pw.vp.ID.Hex(), pw.submitted, len(pw.minedUnpersisted)))
}

// flushOrReportMined retries the pending writes of elections that mined (flushMinedUnpersisted) and
// logs each one that still could not be recorded: losing the pair means a later resume cannot know
// the election exists and will mint a new one. It returns true when none remain.
func (pw *publishWorker) flushOrReportMined() bool {
	if pw.flushMinedUnpersisted() {
		return true
	}
	for qid, upstreamID := range pw.minedUnpersisted {
		log.Errorw(fmt.Errorf("mined election could not be persisted"),
			fmt.Sprintf("question %s has unrecorded on-chain election %s; a republish will create a duplicate",
				qid.Hex(), upstreamID.String()))
	}
	return false
}

// reconcileOwnStaleClaim hands a claim that went stale under this worker to the stale sweep: no
// renewal could refresh it, so the worker stood down without touching the process, and the sweep
// resets its unmined questions, credits back what nothing mined and clears it. A claim someone
// else holds now, or one already cleared, is left alone.
func (pw *publishWorker) reconcileOwnStaleClaim() {
	claims, err := pw.a.db.StaleVotingProcesses()
	if err != nil {
		log.Warnw("could not scan for the stale publish claim", "processId", pw.vp.ID.Hex(), "error", err)
		return
	}
	for _, claim := range claims {
		if claim.ID == pw.vp.ID && claim.Owner == pw.owner {
			pw.a.reconcileStaleClaim(claim)
		}
	}
}

// renewClaim refreshes the publish claim every PublishStaleAfter/3 until stop is closed, so a
// legitimately long publish (many questions, slow mining rounds) is not treated as stale and
// reclaimed from under the worker. A failed renewal means the claim is already lost; it is
// logged, and the owner-conditional final writes are what actually keep a dispossessed worker
// from overwriting the new claimant's state.
func (pw *publishWorker) renewClaim(stop <-chan struct{}) {
	interval := pw.staleAfter / 3
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			ok, err := pw.a.db.RenewVotingProcessPublishClaim(pw.vp.ID, pw.owner, pw.staleAfter)
			if err != nil {
				log.Warnw("could not renew publish claim", "processId", pw.vp.ID.Hex(), "error", err)
				continue
			}
			if !ok {
				log.Warnw("publish claim no longer owned, worker results will be discarded",
					"processId", pw.vp.ID.Hex())
				return
			}
		}
	}
}

// persistPublished records a chain-confirmed election id for a question, retrying the write a
// few times. While it stays unpersisted the pair is kept in minedUnpersisted so later rounds
// retry the write instead of resubmitting the (already existing) election.
func (pw *publishWorker) persistPublished(q *db.VotingProcessQuestion, upstreamID internal.HexBytes) bool {
	pw.minedUnpersisted[q.ID] = upstreamID
	initialStatus := db.QuestionStatusReady
	if pw.vp.InitialStatus == db.QuestionStatusPaused {
		initialStatus = db.QuestionStatusPaused
	}
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
		}
		// flag the process before recording the election id, so no draft edit can replace the
		// questions (and erase the id) once it is stored — whatever an edit in flight read
		// earlier. Without the flag the id is not stored yet: the pair stays pending and both
		// writes are retried.
		if err = pw.a.db.MarkVotingProcessMined(pw.vp.ID); err != nil {
			continue
		}
		if err = pw.a.db.SetQuestionPublished(db.QuestionPublishedParams{
			ID: q.ID, UpstreamID: upstreamID, MetadataURL: q.MetadataURL, Status: initialStatus,
			CensusOrigin: q.CensusOrigin, CensusRoot: q.CensusRoot,
		}); err == nil {
			q.UpstreamID = upstreamID
			delete(pw.minedUnpersisted, q.ID)
			return true
		}
	}
	log.Warnw("could not persist published question, will retry without resubmitting",
		"questionId", q.ID.Hex(), "error", err)
	return false
}

// flushMinedUnpersisted retries the pending SetQuestionPublished writes of questions whose
// election already mined. It returns true when none remain.
func (pw *publishWorker) flushMinedUnpersisted() bool {
	for i := range pw.questions {
		q := &pw.questions[i]
		if upstreamID, ok := pw.minedUnpersisted[q.ID]; ok {
			pw.persistPublished(q, upstreamID)
		}
	}
	return len(pw.minedUnpersisted) == 0
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
	// keep the publish claim fresh for as long as the attempt runs: rounds of building, mining
	// and confirming can legitimately outlast PublishStaleAfter, and losing the claim mid-run
	// would let a concurrent publish mint duplicate elections. This renewal takes over from the
	// one that kept it fresh while the attempt waited (holdClaim).
	pw.releaseClaimHold()
	stopRenew := make(chan struct{})
	go pw.renewClaim(stopRenew)
	defer close(stopRenew)
	// the questions were loaded before this worker held the org tx lock. A worker that ran
	// before it under the same lock may have persisted elections since, so read them again now
	// that nothing else can be submitting for this organization: building from the older read
	// would mint a second election for a question that already has one.
	if err := pw.reloadQuestions(); err != nil {
		return nil, err
	}
	confirmed := false
	for round := 0; round < maxPublishRounds && !confirmed; round++ {
		// a worker that no longer owns the publish claim must stop before submitting anything:
		// someone else has taken the process over, and both submitting would mint duplicate
		// elections. Renewing doubles as the check, and refreshes the claim on every round.
		if err := pw.refreshClaim(); err != nil {
			return nil, err
		}
		// first retry any chain-confirmed election whose DB write is still pending: it must
		// never be resubmitted, only re-persisted.
		pw.flushMinedUnpersisted()
		pending := make([]*db.VotingProcessQuestion, 0, len(pw.questions))
		for i := range pw.questions {
			q := &pw.questions[i]
			if _, mined := pw.minedUnpersisted[q.ID]; mined {
				continue // already on chain, only the DB write is pending
			}
			if len(q.UpstreamID) == 0 {
				pending = append(pending, q)
			}
		}
		if len(pending) == 0 {
			// everything is on chain; confirmed only once every row is persisted too
			confirmed = pw.flushMinedUnpersisted()
			if confirmed {
				break
			}
			continue
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
		pw.submitted = true
		results, err := a.account.SubmitSignedTxBatch(stxs)
		if err != nil {
			log.Warnw("voting process batch submit failed, will retry", "error", err)
			continue
		}
		if a.publishSubmittedHook != nil {
			a.publishSubmittedHook()
		}
		if err := pw.refreshClaim(); err != nil {
			return nil, err
		}
		confirmed = pw.confirmBatch(pending, results)
		if a.publishMinedFault != nil {
			if err := a.publishMinedFault(); err != nil {
				return nil, err
			}
		}
	}
	if !confirmed {
		return nil, transient(fmt.Errorf("publish did not confirm all questions after %d rounds", maxPublishRounds))
	}
	if e := a.db.SetVotingProcessPublished(pw.vp.ID, pw.owner, pw.resolveStartDate()); e != nil {
		// every election is already on-chain and its question persisted: the next attempt finds
		// nothing pending and only marks the process published again. The write is conditional
		// on still owning the claim, so a dispossessed worker never touches the new claimant's state;
		// a conflict means the claim is gone, which no retry can win back.
		if errors.Is(e, db.ErrConflict) {
			return nil, errPublishClaimLost
		}
		return nil, transient(e)
	}
	// what it paid beyond its ledger is nothing: the debit charged whatever its price was. The
	// €0 base records that it is published, so its census grows against what it paid (the first
	// growth check records it if this write fails)
	if _, e := a.db.SetProcessPaymentBase(pw.vp.ID, pw.vp.OrgAddress, 0); e != nil {
		log.Warnw("could not record the published process's payment base", "processId", pw.vp.ID.Hex(), "error", e)
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
	return &db.JobResult{Status: status, ProcessID: internal.HexBytes(pw.vp.ID[:])}, nil
}

// reloadQuestions replaces the worker's questions with what is stored now, refusing a stored
// set that no longer matches the process.
func (pw *publishWorker) reloadQuestions() error {
	questions, err := pw.a.db.QuestionsByProcess(pw.vp.ID)
	if err != nil {
		return fmt.Errorf("failed to reload process questions: %w", err)
	}
	if p := questionSetProblem(pw.vp, questions); p != "" {
		return fmt.Errorf("process questions changed before publishing: %s", p)
	}
	pw.questions = questions
	return nil
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

// refreshClaim renews the publish claim, reporting errPublishClaimLost when this worker no
// longer owns it. A failed write is transient: the claim is still ours, just not renewed.
func (pw *publishWorker) refreshClaim() error {
	owned, err := pw.confirmClaim()
	if err != nil {
		return transient(err)
	}
	if !owned {
		return errPublishClaimLost
	}
	return nil
}

// confirmClaim renews the publish claim and reports whether the worker still owns it, retrying
// a storage error a few times so a transient failure is not taken for a lost claim. An error
// that persists is returned.
func (pw *publishWorker) confirmClaim() (bool, error) {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}
		var owned bool
		if owned, err = pw.a.db.RenewVotingProcessPublishClaim(pw.vp.ID, pw.owner, pw.staleAfter); err == nil {
			return owned, nil
		}
	}
	return false, err
}

// abandon rolls back a publish that failed for good: it resets the not-yet-mined questions (so a
// later publish resumes them), credits the payer's wallet debit back when nothing was
// mined, and clears the publishing marker — all only while this worker still owns the claim. A
// credit-back that fails is logged and the marker cleared anyway: the draft then stays paid, the
// state a delete refunds and a publish reuses.
func (pw *publishWorker) abandon() {
	a := pw.a
	// last chance to persist elections that mined but whose DB write kept failing
	allRecorded := pw.flushOrReportMined()
	// only roll back if this worker still owns the publish claim: a stale claim that got
	// reclaimed means another publish is running and these resets would stomp its work.
	if ok, err := pw.confirmClaim(); err != nil || !ok {
		log.Warnw("publish claim not owned at abandon, skipping rollback",
			"processId", pw.vp.ID.Hex(), "error", err)
		if err == nil {
			pw.standDown()
		}
	} else {
		if e := a.db.ResetQuestionsPublish(pw.vp.ID); e != nil {
			log.Warnw("could not reset questions after failed publish", "error", e)
		}
		// an election that mined but could not be recorded is still an election: never credit it back
		if allRecorded {
			if e := a.reversePublishDebit(pw.vp.ID); e != nil {
				log.Errorw(e, "could not credit back the wallet debit of a failed publish; "+
					"it stays paid, or owes the credit to its next publish, checkout or delete")
			}
		}
		if e := a.db.ClearVotingProcessPublishing(pw.vp.ID, pw.owner); e != nil {
			log.Warnw("could not clear publishing state after failed publish", "error", e)
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
// assignCensus returns the census origin, root and URI of q's election. A census on the SaaS
// CSP key publishes OFF_CHAIN_CA (OFF_CHAIN_CA_V2 with its blind key when anonymous) and leaves
// the question's own origin and root empty.
//
//nolint:revive // function-result-limit: origin, root, uri and error are a natural group for census assignment
func (pw *publishWorker) assignCensus(_ *db.VotingProcessQuestion) (models.CensusOrigin, []byte, string, error) {
	origin := models.CensusOrigin_OFF_CHAIN_CA
	if pw.census.Anonymous {
		origin = models.CensusOrigin_OFF_CHAIN_CA_V2
	}
	return origin, pw.cspPubKey, pw.a.serverURL, nil
}

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
		origin, root, uri, err := pw.assignCensus(q)
		if err != nil {
			return nil, false, err
		}
		q.CensusOrigin = origin.String()
		q.CensusRoot = root
		nonce := startNonce + uint32(i)
		tx, err := a.account.BuildNewProcessTx(&account.NewProcessParams{
			OrgAddress:    pw.vp.OrgAddress,
			Params:        ep,
			CensusRoot:    root,
			CensusURI:     uri,
			CensusOrigin:  origin,
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
		// the election is confirmed on chain: record it in memory first (minedUnpersisted),
		// then persist. If the DB write keeps failing the question is NOT retried as a new
		// submission — the next rounds only retry the write — or every DB hiccup after a
		// mined tx would mint a duplicate election.
		if !pw.persistPublished(pending[i], res.UpstreamID) {
			allConfirmed = false
			continue
		}
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
//	@Failure	400			{object}	errors.Error	"Invalid status, or a question in a terminal status cannot change"
//	@Failure	503			{object}	errors.Error	"Organization transaction in progress (50303), or tx queue full"
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
	a.enqueueStatusChange(w, r, vp, targets, status)
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
//	@Failure	400			{object}	errors.Error	"Invalid status, or a question in a terminal status cannot change"
//	@Failure	503			{object}	errors.Error	"Organization transaction in progress (50303), or tx queue full"
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
	a.enqueueStatusChange(w, r, vp, targets, status)
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
//
// Each question's outcome is recorded individually in the job result (result.questions): a
// chain rejection of one question no longer abandons the rest of the batch. A question that
// is already in the requested status is a successful no-op — the chain would reject the
// redundant transition, so submitting it would make a retry of a half-applied batch fail
// forever. A question in a terminal status (ENDED/CANCELED/RESULTS) that is asked for a
// different one is an invalid transition and rejects the whole batch with 400 before
// anything is submitted.
func (a *API) enqueueStatusChange(
	w http.ResponseWriter, r *http.Request, vp *db.VotingProcess,
	targets []db.VotingProcessQuestion, status models.ProcessStatus,
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
	// stored uppercase to match the vochain (status.String() is already uppercase).
	statusStr := status.String()
	// validate every transition before submitting anything, and set aside the no-ops.
	entries := make([]db.QuestionJobResult, len(published))
	toSubmit := make([]int, 0, len(published))
	for i := range published {
		q := &published[i]
		entries[i] = db.QuestionJobResult{QuestionID: q.ID.Hex()}
		switch q.Status {
		case statusStr:
			// already there: the chain rejects a transition to the current status, so a
			// retry of a partially applied batch must treat it as already done.
			entries[i].Status = db.JobStatusCompleted
			entries[i].NoOp = true
		case db.QuestionStatusEnded, db.QuestionStatusCanceled, db.QuestionStatusResults:
			errors.ErrMalformedBody.Withf(
				"question %s is %s and cannot change to %s", q.ID.Hex(), q.Status, statusStr,
			).Write(w)
			return
		default:
			entries[i].Status = db.JobStatusPending
			toSubmit = append(toSubmit, i)
		}
	}
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if len(toSubmit) == 0 {
		// everything is already in the requested status: record a completed job with the
		// per-question outcomes without touching the lock, the queue or the chain.
		jobID, err := apicommon.NewJobID()
		if err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		result := &db.JobResult{Status: statusStr, Questions: entries}
		if err := a.db.CreateTxJobWithResult(jobID, db.JobTypeSetProcessStatus, org.Address, result); err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		if err := a.db.SetJobStatus(jobID, db.JobStatusCompleted, result, ""); err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		apicommon.HTTPWriteJSONStatus(w, http.StatusAccepted, &apicommon.EnqueuedResponse{JobID: jobID})
		return
	}
	orgSigner, err := account.OrganizationSigner(a.secret, org.SignerSeedValue(), org.Nonce)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not restore organization signer: %v", err).Write(w)
		return
	}
	// the lock comes before the job: a caller refused for a busy organization gets a clear
	// 503 and no job row is left behind.
	orgLock, err := a.orgTxLocks.lockCtx(r.Context(), org.Address)
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	jobID, err := apicommon.NewJobID()
	if err != nil {
		orgLock.Unlock()
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if err := a.db.CreateTxJob(jobID, db.JobTypeSetProcessStatus, org.Address); err != nil {
		orgLock.Unlock()
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	run := func() (*db.JobResult, error) {
		defer orgLock.Unlock()
		failed := 0
		for _, i := range toSubmit {
			if err := a.submitStatusChange(orgSigner, &published[i], status, statusStr); err != nil {
				entries[i].Status = db.JobStatusFailed
				entries[i].Error = err.Error()
				failed++
				continue
			}
			entries[i].Status = db.JobStatusCompleted
		}
		result := &db.JobResult{Status: statusStr, Questions: entries}
		if failed > 0 {
			return result, fmt.Errorf("%d of %d questions failed to change status", failed, len(toSubmit))
		}
		return result, nil
	}
	// record keeps the per-question entries on the job even when the task fails as a
	// whole; the default recorder would drop the result of a failed task.
	record := func(result *db.JobResult, runErr error) {
		jobStatus, errMsg := db.JobStatusCompleted, ""
		if runErr != nil {
			jobStatus, errMsg = db.JobStatusFailed, runErr.Error()
		}
		if e := a.db.SetJobStatus(jobID, jobStatus, result, errMsg); e != nil {
			log.Warnw("could not record status change job", "jobId", jobID, "error", e)
		}
	}
	if !a.enqueueTx(txTask{jobID: jobID, run: run, record: record}) {
		orgLock.Unlock()
		if e := a.db.SetJobStatus(jobID, db.JobStatusFailed, nil, "tx queue full"); e != nil {
			log.Warnw("could not mark job failed after full queue", "error", e)
		}
		errors.ErrTxQueueFull.Write(w)
		return
	}
	apicommon.HTTPWriteJSONStatus(w, http.StatusAccepted, &apicommon.EnqueuedResponse{JobID: jobID})
}

// submitStatusChange builds, funds, signs and submits one SET_PROCESS_STATUS tx and records
// the question's new stored status on success (confirmed in the background by the status
// syncer, which corrects the optimistic write if the tx never reaches the requested status).
func (a *API) submitStatusChange(
	orgSigner *ethereum.SignKeys, q *db.VotingProcessQuestion, status models.ProcessStatus, statusStr string,
) error {
	tx, err := a.account.BuildSetProcessStatusTx(orgSigner.Address(), q.UpstreamID, status)
	if err != nil {
		return err
	}
	fundedTx, _, err := a.account.FundTransaction(tx, orgSigner.Address())
	if err != nil {
		return err
	}
	stx, err := a.account.SignTransaction(fundedTx, orgSigner)
	if err != nil {
		return err
	}
	if _, err := a.account.SubmitSignedTx(stx); err != nil {
		return err
	}
	if err := a.db.SetQuestionStatus(q.ID, statusStr); err != nil {
		log.Warnw("could not persist question status", "error", err)
	}
	a.enqueueConfirm(q.UpstreamID, statusStr)
	return nil
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
