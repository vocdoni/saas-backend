package api

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/subscriptions"
	dvoteapi "go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/log"
)

const (
	// minEndDateLead is how far a new end date must be after the current end date (which must itself
	// still be ahead of now, so the new end also leads now by at least this much): the job may
	// wait for a tx worker, then mines each question in turn (up to 40 s each), and the chain end can
	// trail the stored end by a mining delay.
	minEndDateLead = 5 * time.Minute
	// endDateSubmitLead is how far the new end date must still be ahead when a question's tx is
	// submitted; the chain refuses a duration that has already passed.
	endDateSubmitLead = 30 * time.Second
	// perQuestionBudget is the time one question's tx needs to be mined (the 40 s SubmitSignedTx wait
	// plus a block): the job only sends a tx whose election still ends that much after it is sent.
	perQuestionBudget = time.Minute
)

// setVotingProcessEndDateHandler moves the end date of a published process later.
//
//	@Summary		Extend the end date of a published process
//	@Description	Moves the end date of every open (READY or PAUSED) published question's election to `endDate`
//	@Description	(RFC 3339, any offset; stored in UTC, truncated to seconds), as an async job; closed questions
//	@Description	keep their outcome and are skipped. Admin only (JWT, or an API key with `voting:write` whose
//	@Description	user is Admin); free. Extend only: the new end must be at least 5 minutes after the current end
//	@Description	(the current end itself is accepted as a no-op job) and at most 90 days after the process's start date. Request
//	@Description	the extension at least 5 minutes plus 1 minute per open question before the current end; the one
//	@Description	exception is a repair: an endDate that an open question's election already ends at on chain (left
//	@Description	by a partially failed job) is accepted even after the current end, and finishes the rest:
//	@Description	questions already ended on chain are skipped, and if one is still open but too close to its end
//	@Description	the job fails with nothing sent until it has ended.
//	@Description	Poll GET /jobs/{jobId}: `result.status` is the requested end date (applied
//	@Description	only if the job completed) and `result.questions[]` gives each open question's
//	@Description	`status`/`error`/`noOp`. The stored end date (used by voter
//	@Description	authentication) changes only when every question succeeded; on a partial failure the job
//	@Description	fails with "n of m questions failed; send the same endDate again to finish the rest" and calling
//	@Description	again with the same endDate finishes the rest
//	@Description	(done questions are no-ops). A question found closed on chain is skipped (`noOp`, with the reason
//	@Description	in `error`) and its stored status corrected. If the stored end date changed after the request was
//	@Description	validated, the job fails before sending anything and names the current end date. The job can also
//	@Description	fail before sending anything ("...; nothing was changed"), including "no open question left to
//	@Description	extend" when every question is closed on chain.
//	@Description	A 500 is an internal error or a failed blockchain check of a repair; no job was created, so retrying is safe.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string								true	"Process ID"
//	@Param			request		body		apicommon.SetProcessEndDateRequest	true	"New end date"
//	@Success		202			{object}	apicommon.EnqueuedResponse
//	@Failure		400			{object}	errors.Error	"Bad endDate, over 90 days, unpublished, ended or near its end, no open question"
//	@Failure		401			{object}	errors.Error	"User is not admin of the organization"
//	@Failure		404			{object}	errors.Error	"Process not found"
//	@Failure		500			{object}	errors.Error	"Internal error or blockchain check failed; no job was created, retry"
//	@Failure		503			{object}	errors.Error	"Organization transaction in progress (50303), or tx queue full"
//	@Router			/processes/{processId}/endDate [put]
func (a *API) setVotingProcessEndDateHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	var req apicommon.SetProcessEndDateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	parsed, err := time.Parse(time.RFC3339, req.EndDate)
	if err != nil {
		errors.ErrMalformedBody.Withf("invalid endDate: %v", err).Write(w)
		return
	}
	newEnd := parsed.Truncate(time.Second).UTC()
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
	published, err := endDateTargets(vp, questions, newEnd)
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	if err := endDateTiming(vp.EndDate, len(published)); err != nil {
		// a repeat of the stored end is refused as before; only a different date can be a repair
		if newEnd.Equal(vp.EndDate.Truncate(time.Second)) {
			writeSubscriptionError(w, err)
			return
		}
		repair, unfinished, readErr := a.isEndDateRepair(published, vp.EndDate, newEnd)
		if readErr != nil {
			errors.ErrVochainRequestFailed.WithErr(readErr).Write(w)
			return
		}
		if !repair {
			// name the date that finishes an unfinished extension: no response exposes a question's chain end
			var apiErr errors.Error
			if !unfinished.IsZero() && errors.As(err, &apiErr) {
				err = apiErr.Withf("a previous extension is unfinished: send endDate %s to finish it",
					unfinished.UTC().Format(time.RFC3339))
			}
			writeSubscriptionError(w, err)
			return
		}
	}
	a.enqueueEndDateChange(w, r, vp, newEnd)
}

// endDateTargets validates moving vp's end date to newEnd, except for the time left before the
// current end (endDateTiming), and returns its open (READY or PAUSED) published questions, the only
// ones the job extends.
func endDateTargets(
	vp *db.VotingProcess, questions []db.VotingProcessQuestion, newEnd time.Time,
) ([]db.VotingProcessQuestion, error) {
	if !vp.Published {
		return nil, errors.ErrInvalidData.Withf("process is not published")
	}
	published := openPublishedQuestions(questions)
	if len(published) == 0 {
		return nil, errors.ErrInvalidData.Withf("process has no open (READY or PAUSED) published questions to extend")
	}
	if vp.EndDate.IsZero() {
		return nil, errors.ErrInvalidData.Withf("process has no end date")
	}
	// the current end date itself is an idempotent repeat: its job completes as no-ops
	current := vp.EndDate.Truncate(time.Second)
	if !newEnd.Equal(current) && newEnd.Before(current.Add(minEndDateLead)) {
		return nil, errors.ErrInvalidData.Withf(
			"endDate must be later than the current end date (by at least %s)", minEndDateLead)
	}
	if vp.StartDate.IsZero() {
		return nil, errors.ErrInvalidData.Withf("process has no start date")
	}
	// measured from the stored start (publish persists the real one), not from now as for a
	// draft, so repeated extensions cannot run a process past the cap; the job also bounds each
	// election from its own on-chain start
	if newEnd.Sub(vp.StartDate) > subscriptions.MaxProcessDurationDays*24*time.Hour {
		return nil, errors.ErrInvalidData.Withf(
			"process duration exceeds the limit of %d days", subscriptions.MaxProcessDurationDays)
	}
	return published, nil
}

// openPublishedQuestions returns the published questions still open (READY or PAUSED) in storage.
func openPublishedQuestions(questions []db.VotingProcessQuestion) []db.VotingProcessQuestion {
	open := make([]db.VotingProcessQuestion, 0, len(questions))
	for i := range questions {
		// closed questions (ENDED/CANCELED/RESULTS) keep their outcome; the CSP still refuses them by status
		if len(questions[i].UpstreamID) > 0 && isOpenStatus(questions[i].Status) {
			open = append(open, questions[i])
		}
	}
	return open
}

// endDateTiming refuses a change requested once the stored end has passed, or too close to it for
// every one of the n open questions to be mined before it: the CSP stops signing at the old end
// while the job runs. A repair (isEndDateRepair, a newEnd other than the stored end) is accepted
// despite it.
func endDateTiming(storedEnd time.Time, n int) error {
	current := storedEnd.Truncate(time.Second)
	if !current.After(time.Now()) {
		return errors.ErrInvalidData.Withf(
			"the process already ended at %s; an ended process cannot be extended", current.UTC().Format(time.RFC3339))
	}
	if budget := minEndDateLead + time.Duration(n)*perQuestionBudget; time.Until(current) < budget {
		return errors.ErrInvalidData.Withf(
			"the process ends at %s, less than %d minutes from now; it is too close to its end to be extended safely",
			current.UTC().Format(time.RFC3339), int(budget.Minutes()))
	}
	return nil
}

// isEndDateRepair reports whether newEnd repairs a split left by a partially failed job: some target
// election is still open on chain and already ends exactly at newEnd, so newEnd was validated and
// applied before and moving the stored end there serves that election. It reads every target
// (uncached); a read error is returned. It also returns the latest end of an open target that ends
// at least minEndDateLead after storedEnd (zero if none): the endDate that finishes an unfinished
// extension (a smaller gap is publish mining delay, not an extension).
func (a *API) isEndDateRepair(targets []db.VotingProcessQuestion, storedEnd, newEnd time.Time) (
	repair bool, unfinished time.Time, err error,
) {
	for i := range targets {
		election, err := a.account.Election(targets[i].UpstreamID)
		if err != nil {
			return false, time.Time{}, err
		}
		if !isOpenStatus(election.Status) {
			continue
		}
		if election.EndDate.Unix() == newEnd.Unix() {
			repair = true
		}
		if !election.EndDate.Before(storedEnd.Truncate(time.Second).Add(minEndDateLead)) &&
			election.EndDate.After(unfinished) {
			unfinished = election.EndDate
		}
	}
	return repair, unfinished.Truncate(time.Second), nil
}

// enqueueEndDateChange runs the end date change of vp (as validated by the handler) as a job on the
// tx worker pool, under the org lock taken here: see endDateJob.
func (a *API) enqueueEndDateChange(w http.ResponseWriter, r *http.Request, vp *db.VotingProcess, newEnd time.Time) {
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
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
	if err := a.db.CreateTxJob(jobID, db.JobTypeSetProcessEndDate, org.Address); err != nil {
		orgLock.Unlock()
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	run := func() (*db.JobResult, error) {
		defer orgLock.Unlock()
		return a.endDateJob(orgSigner, vp, newEnd)
	}
	// record keeps the per-question entries on the job even when the task fails as a whole
	record := func(result *db.JobResult, runErr error) {
		jobStatus, errMsg := db.JobStatusCompleted, ""
		if runErr != nil {
			jobStatus, errMsg = db.JobStatusFailed, runErr.Error()
		}
		if e := a.db.SetJobStatus(jobID, jobStatus, result, errMsg); e != nil {
			log.Warnw("could not record end date job", "jobId", jobID, "error", e)
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

// endDateJob moves every open question of vp to end at newEnd. It runs under the org lock, so every
// decision is made here against fresh data: it re-reads the process, fails before reading the chain
// if its stored end is no longer the one the handler validated (a repeat of the stored end completes
// as no-ops), reads each target election once, plans the whole change (planEndDateChange) and sends
// nothing unless every tx fits its time budget. It then sends one SET_PROCESS_DURATION tx per
// extended question, corrects the stored status of questions found closed on chain, and stores the
// new end once no question failed and some election ends at it.
func (a *API) endDateJob(orgSigner *ethereum.SignKeys, vp *db.VotingProcess, newEnd time.Time) (*db.JobResult, error) {
	result := &db.JobResult{Status: newEnd.Format(time.RFC3339)}
	fresh, questions, err := a.db.ProcessWithQuestions(vp.ID)
	if err != nil {
		return result, fmt.Errorf("could not read the process: %w", err)
	}
	targets := openPublishedQuestions(questions)
	entries := make([]db.QuestionJobResult, len(targets))
	for i := range targets {
		entries[i] = db.QuestionJobResult{QuestionID: targets[i].ID.Hex(), Status: db.JobStatusCompleted}
	}
	result.Questions = entries
	// fail every entry: nothing was sent
	failAll := func(err error) (*db.JobResult, error) {
		for i := range entries {
			entries[i].Status, entries[i].Error = db.JobStatusFailed, err.Error()
		}
		return result, err
	}
	// a repeat of the stored end reads and sends nothing, and stores nothing: that could undo an
	// extension that ran between this request's validation and its job
	freshEnd := fresh.EndDate.Truncate(time.Second)
	if freshEnd.Equal(newEnd) {
		for i := range entries {
			entries[i].NoOp = true
		}
		return result, nil
	}
	if !freshEnd.Equal(vp.EndDate.Truncate(time.Second)) {
		return failAll(fmt.Errorf("the process end date changed to %s since this request was validated; send a new request",
			freshEnd.UTC().Format(time.RFC3339)))
	}
	elections := make([]*dvoteapi.Election, len(targets))
	for i := range targets {
		if elections[i], err = a.account.Election(targets[i].UpstreamID); err != nil {
			return failAll(fmt.Errorf("could not read the election of question %s; nothing was changed: %w",
				targets[i].ID.Hex(), err))
		}
	}
	plan, err := planEndDateChange(time.Now(), newEnd, targets, elections)
	if err != nil {
		return failAll(err)
	}
	failed, reached, outOfTime := 0, false, false
	for i, step := range plan {
		q := &targets[i]
		switch step.action {
		case endDateNoOp:
			entries[i].NoOp, reached = true, true
		case endDateSkipClosed:
			entries[i].NoOp = true
			entries[i].Error = fmt.Sprintf("skipped: election is %s on chain", step.election.Status)
			// the job holds the fresh election: correct a stale stored status now, conditional on it
			// being unchanged, so the CSP stops signing for a closed question
			if _, err := a.db.SetQuestionStatusSynced(q.UpstreamID, q.Status, step.election.Status); err != nil {
				log.Warnw("could not correct the stored status of a closed question", "question", q.ID.Hex(), "error", err)
			}
		default: // endDateExtend
			switch {
			case outOfTime:
				entries[i].Error = "not attempted: out of time"
			case !step.election.EndDate.After(time.Now().Add(perQuestionBudget)):
				// a tx sent now could be mined after the election ends; stop sending
				outOfTime = true
				entries[i].Error = fmt.Sprintf("election ends at %s, too soon to extend safely",
					step.election.EndDate.UTC().Format(time.RFC3339))
			default:
				err := a.submitEndDateTx(orgSigner, q, step)
				if err == nil {
					reached = true
					continue
				}
				log.Warnw("could not extend the end date of an election", "question", q.ID.Hex(), "error", err)
				entries[i].Error = err.Error()
			}
			entries[i].Status = db.JobStatusFailed
			failed++
		}
	}
	if failed > 0 {
		return result, fmt.Errorf("%d of %d questions failed; send the same endDate again to finish the rest", failed, len(targets))
	}
	if !reached {
		return result, fmt.Errorf("no open question left to extend; nothing was changed")
	}
	if err := a.db.SetVotingProcessEndDate(vp.ID, newEnd); err != nil {
		return result, fmt.Errorf("could not store the new end date: %w", err)
	}
	return result, nil
}

// endDateAction is what the job does with one target question.
type endDateAction int

const (
	// endDateExtend sends a SET_PROCESS_DURATION tx
	endDateExtend endDateAction = iota
	// endDateNoOp: the election already ends at the new end
	endDateNoOp
	// endDateSkipClosed: the election is not READY or PAUSED on chain; it keeps its outcome
	endDateSkipClosed
)

// endDateStep is the plan for one target question: its action, its election as read by the job,
// and the new duration (seconds from the election's own start) for an extend.
type endDateStep struct {
	action   endDateAction
	election *dvoteapi.Election
	secs     uint32
}

// endDatePlan is index-aligned with the job's targets.
type endDatePlan []endDateStep

// planEndDateChange decides, before any tx is sent, what to do with each target given its election
// (index-aligned), and refuses the whole change when any tx would be rejected by the chain or could
// race an end: a duration out of range or over MaxProcessDurationDays from the election's on-chain
// start, a shortening, an i-th extend (0-based, in order) whose election ends within
// (i+1)*perQuestionBudget of now, or a newEnd within endDateSubmitLead plus one budget per extend.
func planEndDateChange(
	now, newEnd time.Time, targets []db.VotingProcessQuestion, elections []*dvoteapi.Election,
) (endDatePlan, error) {
	const maxSecs = subscriptions.MaxProcessDurationDays * 24 * 60 * 60
	plan := make(endDatePlan, len(targets))
	var tooSoon, lastTooSoon *dvoteapi.Election
	tooSoonID := ""
	extends := 0
	for i, election := range elections {
		plan[i].election = election
		if !isOpenStatus(election.Status) {
			plan[i].action = endDateSkipClosed
			continue
		}
		if election.EndDate.Unix() == newEnd.Unix() {
			plan[i].action = endDateNoOp
			continue
		}
		id := targets[i].ID.Hex()
		// the chain counts the duration from the election's own start, which can differ per question
		secs := newEnd.Unix() - election.StartDate.Unix()
		if secs <= 0 || secs > math.MaxUint32 || secs > maxSecs {
			return nil, fmt.Errorf("question %s: the new end is out of range for an election started at %s "+
				"(at most %d days); nothing was changed",
				id, election.StartDate.UTC().Format(time.RFC3339), subscriptions.MaxProcessDurationDays)
		}
		if newEnd.Unix() < election.EndDate.Unix() {
			return nil, fmt.Errorf("question %s already ends at %s, after the new end date; nothing was changed",
				id, election.EndDate.UTC().Format(time.RFC3339))
		}
		if !election.EndDate.After(now.Add(time.Duration(extends+1) * perQuestionBudget)) {
			if tooSoon == nil || election.EndDate.Before(tooSoon.EndDate) {
				tooSoon, tooSoonID = election, id
			}
			if lastTooSoon == nil || election.EndDate.After(lastTooSoon.EndDate) {
				lastTooSoon = election
			}
		}
		plan[i].action, plan[i].secs = endDateExtend, uint32(secs)
		extends++
	}
	if tooSoon != nil {
		end := tooSoon.EndDate.UTC().Format(time.RFC3339)
		verb := "ends"
		if !tooSoon.EndDate.After(now) {
			verb = "ended"
		}
		repair := ""
		for _, step := range plan {
			if step.action == endDateNoOp {
				// another election already ends at newEnd: this is the repair of a partial job
				// the retry only works once every too-soon question has ended: the latest one counts
				if !lastTooSoon.EndDate.After(now) {
					repair = "; send the same endDate again in a minute, once the chain marks the too-close " +
						"questions ended, to finish (they will be skipped)"
				} else {
					repair = fmt.Sprintf("; send the same endDate again after %s to finish (questions that have "+
						"ended by then will be skipped as ended)", lastTooSoon.EndDate.UTC().Format(time.RFC3339))
				}
				break
			}
		}
		return nil, fmt.Errorf("question %s %s at %s, too soon to extend safely; nothing was changed%s",
			tooSoonID, verb, end, repair)
	}
	if extends > 0 && !newEnd.After(now.Add(endDateSubmitLead+time.Duration(extends)*perQuestionBudget)) {
		return nil, fmt.Errorf("endDate %s is too soon to extend %d questions safely; nothing was changed",
			newEnd.UTC().Format(time.RFC3339), extends)
	}
	return plan, nil
}

// submitEndDateTx builds, funds (with the election the job already read), signs and submits one
// question's SET_PROCESS_DURATION tx and waits for it to be mined.
func (a *API) submitEndDateTx(orgSigner *ethereum.SignKeys, q *db.VotingProcessQuestion, step endDateStep) error {
	tx, err := a.account.BuildSetProcessDurationTx(orgSigner.Address(), q.UpstreamID, step.secs)
	if err != nil {
		return err
	}
	fundedTx, _, err := a.account.FundTransactionWith(tx, orgSigner.Address(), step.election)
	if err != nil {
		return err
	}
	stx, err := a.account.SignTransaction(fundedTx, orgSigner)
	if err != nil {
		return err
	}
	_, err = a.account.SubmitSignedTx(stx)
	return err
}
