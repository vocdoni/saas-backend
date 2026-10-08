package api

import (
	"context"
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
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
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
		if e := a.db.ClearVotingProcessPublishing(id, ""); e != nil {
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
// checks, plus the plan/quota/permission denials that would otherwise only surface asynchronously
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
		return problems, questionsMismatch // plan checks below need the census
	}
	orgDoc, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		return append(problems, "organization not found"), questionsMismatch
	}
	if !user.HasRoleFor(vp.OrgAddress, db.AdminRole) {
		problems = append(problems, "publishing requires the admin role")
	}
	// Per-question plan voting-type gate, on the ballot each question actually encodes rather
	// than the type it is labelled with: a stored type is only a label, and a question written
	// before the two halves were reconciled may carry one its protocol contradicts.
	//
	// EffectiveQuestionType recognises the four named types (singlechoice, multichoice, ranked,
	// cumulative) from their canonical protocol and gates each on its plan flag, so a raw protocol
	// that encodes one of them is gated too — the label cannot be dropped to evade the plan.
	//
	// What still slips through is a protocol that is *almost* canonical: {maxCount:2, maxValue:1,
	// maxTotalCost:2} is the multichoice ballot field-for-field bar costExponent, so it is not
	// recognised and stays ungated. EffectiveQuestionType recognises a shape only when it is
	// exactly canonical, which is what storage needs and what authorization does not. Actually
	// holding the gate needs a deliberately loose classifier (maxValue == 1 && maxCount > 1 ⇒
	// effectively multiple-choice, whatever else is set), not this one.
	for i := range questions {
		// a question that already mined (UpstreamID set) is immutable on chain: its type cannot be
		// changed, and gating it would only block a resume that mints the remaining questions. So the
		// voting-type gate covers only questions still pending their first publish.
		if len(questions[i].UpstreamID) > 0 {
			continue
		}
		if err := a.subscriptions.OrgAllowsVotingType(vp.OrgAddress, account.EffectiveQuestionType(&questions[i])); err != nil {
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
	return problems, questionsMismatch
}

// publishVotingProcessHandler godoc
//
//	@Summary		Publish a voting process
//	@Description	Publish a voting process: one on-chain election per question, submitted as a batch.
//	@Description	Requires Admin role (or a `voting:write` key). Returns 202 with a job id; poll
//	@Description	GET /jobs/{jobId}. Idempotent once published.
//	@Description	409 (40172) means the stored questions do not match the process and the draft has to be
//	@Description	saved again before it can be published.
//	@Description	402 (40178) means the process is priced and unpaid — the current quote travels in the
//	@Description	error data; start checkout via POST /processes/{processId}/checkout. For a managed
//	@Description	organization, 402 (40175) means its integrator's wallet does not cover the price.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string	true	"Process ID"
//	@Success		202			{object}	apicommon.EnqueuedResponse
//	@Success		200			{object}	apicommon.CreateVotingProcessResponse	"Already published"
//	@Failure		400			{object}	errors.Error							"Not ready to publish"
//	@Failure		401			{object}	errors.Error
//	@Failure		402			{object}	errors.Error	"Payment required (quote in data), or insufficient integrator wallet balance"
//	@Failure		404			{object}	errors.Error
//	@Failure		409			{object}	errors.Error	"Publish in progress, questions out of sync, or payment still processing"
//	@Failure		422			{object}	errors.Error	"Managed organization: census size requires a custom quote"
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
	// allowance. Anything predictable is rejected here rather than as an opaque async job failure;
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

	// payment gate: a priced process publishes only after its payment is verified. The
	// quote owed travels in the error payload so the client can go straight to checkout.
	// Free processes pass with no checkout; managed organizations are debited from
	// their integrator's wallet inside startProcessPublish instead.
	due, err := a.paymentDueForPublish(vp)
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	if due != nil {
		errors.ErrPaymentRequired.WithData(due).Write(w)
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

// startProcessPublish runs the post-preflight publish pipeline: atomic claim, managed
// slot reservation, organization signer, census publication, job creation and enqueue.
// The caller is responsible for preflight (publishPreflightProblems) and authorization.
// It exists apart from the HTTP handler so payment fulfillment can trigger publication
// server-side with the same guarantees. Errors are errors.Error values carrying their
// HTTP semantics (write with writeSubscriptionError), plain errors mapping to 500, or
// errProcessAlreadyPublished. ctx bounds only the lock wait below, never the enqueued work.
func (a *API) startProcessPublish(ctx context.Context, t publishTarget) (string, error) {
	vp, questions, census, user := t.vp, t.questions, t.census, t.user
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
	committed := false
	defer func() {
		if committed {
			return
		}
		if e := a.db.ClearVotingProcessPublishing(oid, owner); e != nil {
			log.Warnw("could not clear voting process publishing state", "error", e)
		}
	}()
	// the snapshot's questions were loaded before the claim; a draft edit in that window bumped
	// updatedAt and was caught above, but reload them under the claim anyway so the published
	// elections can only be built from what is stored now.
	questions, err = a.db.QuestionsByProcess(oid)
	if err != nil {
		return "", fmt.Errorf("failed to reload process questions: %w", err)
	}
	if p := questionSetProblem(vp, questions); p != "" {
		return "", errors.ErrProcessQuestionsMismatch.Withf("%s", p)
	}
	// again under the claim, which a delete takes too: one that refunded and failed between
	// the payment gate and here would otherwise go on chain unpaid
	if err := a.refuseRefundedDraft(oid); err != nil {
		return "", err
	}

	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		return "", fmt.Errorf("failed to get organization: %w", err)
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
			return "", err
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

	// payment: a managed organization's process is charged to its integrator's wallet
	// here, after the claim, so two concurrent publishes cannot both debit — and the
	// debit's per-process idempotency means a retry after a later failure passes
	// without a second charge. Such a failure (a full queue included) leaves a paid
	// draft, like a card-paid one: publishing again is free and deleting it refunds the
	// wallet. Standard organizations were gated on a verified payment before this function.
	if org.ManagedBy != (common.Address{}) {
		if err := a.debitManagedProcessWallet(vp, org); err != nil {
			return "", err
		}
	}

	orgSigner, err := account.OrganizationSigner(a.secret, org.SignerSeedValue(), org.Nonce)
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

	// acquire the per-org tx lock with a bounded wait before the job exists: a worker may hold
	// it through tx mining, and a caller is better served by a prompt 503 than a parked request.
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

	jobID, err := apicommon.NewJobID()
	if err != nil {
		return "", fmt.Errorf("failed to create job id: %w", err)
	}
	if err := a.db.CreateTxJob(jobID, db.JobTypePublishVotingProcess, org.Address); err != nil {
		return "", fmt.Errorf("failed to create tx job: %w", err)
	}

	reserved := managedReserved
	worker := &publishWorker{
		a: a, vp: vp, questions: questions, census: census, org: org, user: user,
		orgSigner: orgSigner, cspPubKey: cspPubKey, integratorAddr: integratorAddr,
		reserved: reserved, nonTestSized: nonTestSized, owner: owner,
		minedUnpersisted: make(map[bson.ObjectID]internal.HexBytes),
	}
	if !a.enqueueTx(txTask{jobID: jobID, run: func() (*db.JobResult, error) {
		defer orgLock.Unlock()
		return worker.run()
	}}) {
		if e := a.db.SetJobStatus(jobID, db.JobStatusFailed, nil, "tx queue full"); e != nil {
			log.Warnw("could not mark job failed after full queue", "error", e)
		}
		if org.ManagedBy != (common.Address{}) {
			return "", errors.ErrTxQueueFull.Withf("what the wallet paid is kept; publishing again does not charge it again")
		}
		return "", errors.ErrTxQueueFull
	}
	// the worker now owns the lock, the publishing claim and the managed reservation.
	committed = true
	managedReserved = false
	lockHeld = false
	return jobID, nil
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
	// owner is the publish-claim token returned by ClaimVotingProcessForPublish: the worker
	// renews the claim with it while it runs and every state write it does on the process
	// (clear, publish) is conditional on still holding it.
	owner string
	// minedUnpersisted maps a question id to the election id the chain confirmed for it when
	// SetQuestionPublished failed afterwards. Those questions are never rebuilt or resubmitted
	// (the election already exists — resubmitting would mint a duplicate); only the DB write is
	// retried, until it succeeds or the attempt is abandoned.
	minedUnpersisted map[bson.ObjectID]internal.HexBytes
}

// renewClaim refreshes the publish claim every PublishStaleAfter/3 until stop is closed, so a
// legitimately long publish (many questions, slow mining rounds) is not treated as stale and
// reclaimed from under the worker. A failed renewal means the claim is already lost; it is
// logged, and the owner-conditional final writes are what actually keep a dispossessed worker
// from overwriting the new claimant's state.
func (pw *publishWorker) renewClaim(stop <-chan struct{}) {
	interval := db.PublishStaleAfter / 3
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
			ok, err := pw.a.db.RenewVotingProcessPublishClaim(pw.vp.ID, pw.owner)
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
		if err = pw.a.db.SetQuestionPublished(q.ID, upstreamID, q.MetadataURL, initialStatus); err == nil {
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
	// keep the publish claim fresh for as long as the worker runs: rounds of building, mining
	// and confirming can legitimately outlast PublishStaleAfter, and losing the claim mid-run
	// would let a concurrent publish mint duplicate elections.
	stopRenew := make(chan struct{})
	go pw.renewClaim(stopRenew)
	defer close(stopRenew)
	confirmed := false
	for round := 0; round < maxPublishRounds && !confirmed; round++ {
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
	if e := a.db.SetVotingProcessPublished(pw.vp.ID, pw.owner, pw.resolveStartDate()); e != nil {
		// every election is already on-chain and its question persisted; clear the marker so a
		// retry can re-run and simply re-mark the process published (pending is empty → no new
		// elections). Leaving it set would make the process permanently unclaimable. Both writes
		// are conditional on still owning the claim: a dispossessed worker must not touch the
		// new claimant's state.
		if ce := a.db.ClearVotingProcessPublishing(pw.vp.ID, pw.owner); ce != nil {
			log.Warnw("could not clear publishing marker after late publish failure", "error", ce)
		}
		return nil, e
	}
	// both payment gates let a process through without a payment only when it priced at zero,
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

// abandon rolls back a failed/aborted publish: it resets the not-yet-mined questions (so a later
// publish resumes them), clears the publishing marker, and releases the managed-process
// reservation only when nothing was mined this run. A partial-mine failure keeps the slot so the
// resume (which skips a new reservation) consumes it, avoiding a leak or a double-reserve.
func (pw *publishWorker) abandon() {
	a := pw.a
	// last chance to persist elections that mined but whose DB write kept failing: losing the
	// pair here means a later resume cannot know the election exists and will mint a new one.
	if !pw.flushMinedUnpersisted() {
		for qid, upstreamID := range pw.minedUnpersisted {
			log.Errorw(fmt.Errorf("mined election could not be persisted"),
				fmt.Sprintf("question %s has unrecorded on-chain election %s; a republish will create a duplicate",
					qid.Hex(), upstreamID.String()))
		}
	}
	// only roll back if this worker still owns the publish claim: a stale claim that got
	// reclaimed means another publish is running and these resets would stomp its work.
	if ok, err := a.db.RenewVotingProcessPublishClaim(pw.vp.ID, pw.owner); err != nil || !ok {
		log.Warnw("publish claim not owned at abandon, skipping rollback",
			"processId", pw.vp.ID.Hex(), "error", err)
		return
	}
	if e := a.db.ResetQuestionsPublish(pw.vp.ID); e != nil {
		log.Warnw("could not reset questions after failed publish", "error", e)
	}
	if e := a.db.ClearVotingProcessPublishing(pw.vp.ID, pw.owner); e != nil {
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

// reserveManagedProcessSlot reserves one process slot against the integrator's shared
// ManagedProcesses quota when org is a managed organization publishing a non-test-sized
// election. It returns the integrator address and reserved=true when a slot was taken — the
// caller MUST roll it back with AddOrganizationManagedProcesses(integratorAddr, -1) if the
// publish/sign later fails. For a standalone org or a test-sized election it is a no-op
// (reserved=false, nil error).
//
// HasTxPermission skips the per-org process-count check for managed orgs precisely because
// this integrator-level reservation enforces it instead.
func (a *API) reserveManagedProcessSlot(org *db.Organization, maxCensusSize uint64) (common.Address, bool, error) {
	if org.ManagedBy == (common.Address{}) || maxCensusSize <= uint64(db.TestMaxCensusSize) {
		return common.Address{}, false, nil
	}
	integrator, err := a.db.Organization(org.ManagedBy)
	if err != nil {
		// a managed org whose integrator no longer exists is a not-found condition, not a
		// server fault — map it like subscriptions.limitsOwner does rather than 500.
		if err == db.ErrNotFound {
			return common.Address{}, false, errors.ErrOrganizationNotFound.WithErr(err)
		}
		return common.Address{}, false, errors.ErrGenericInternalServerError.Withf("could not get integrator organization: %v", err)
	}
	maxProcesses, err := a.subscriptions.ManagedPublishLimits(integrator)
	if err != nil {
		return common.Address{}, false, err
	}
	if err := a.db.ReserveManagedPublish(integrator.Address, maxProcesses); err != nil {
		if err == db.ErrManagedQuotaReached {
			return common.Address{}, false, errors.ErrIntegratorQuotaExceeded
		}
		return common.Address{}, false, errors.ErrGenericInternalServerError.WithErr(err)
	}
	return integrator.Address, true, nil
}
