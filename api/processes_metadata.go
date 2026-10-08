package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"time"

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

// votingProcessMetadataHandler godoc
//
//	@Summary		Get a voting process's editable metadata
//	@Description	Read the editable text of a voting process: its title, description, header and streamUri,
//	@Description	and each question's title, description and choice titles, in the order the process stores
//	@Description	them. Public for a published process; a draft is visible only to a Manager/Admin of the
//	@Description	organization (or a voting:write API key acting as one) and is a 404 otherwise. The same
//	@Description	shape is the body PUT /processes/{processId}/metadata takes.
//	@Tags			processes
//	@Produce		json
//	@Param			processId	path		string	true	"Process ID"
//	@Success		200			{object}	apicommon.VotingProcessMetadata
//	@Failure		400			{object}	errors.Error
//	@Failure		404			{object}	errors.Error
//	@Router			/processes/{processId}/metadata [get]
func (a *API) votingProcessMetadataHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
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
	// a draft is only visible to a manager/admin of the owning org, as in votingProcessInfoHandler
	if !vp.Published && !a.optionalManager(r, vp.OrgAddress) {
		errors.ErrProcessNotFound.Write(w)
		return
	}
	// a metadata edit whose tx mined after its job stopped waiting is applied before serving
	a.reconcilePendingMetadata(vp, questions)
	apicommon.HTTPWriteJSON(w, apicommon.VotingProcessMetadataFromDB(vp, questions))
}

// updateVotingProcessMetadataHandler godoc
//
//	@Summary		Edit a voting process's metadata
//	@Description	Edit the text of a voting process — its title, description, header and streamUri, and each
//	@Description	question's title, description and choice titles — including after it is published. Nothing
//	@Description	structural can change: the body has no field for a question's or choice's type, setup,
//	@Description	value or ballot protocol, and questions and choices are matched by position, so their
//	@Description	counts must equal the process's exactly (400 otherwise). Send back the shape read from
//	@Description	GET /processes/{processId}/metadata. Requires the Manager or Admin role (or a voting:write
//	@Description	API key).
//	@Description
//	@Description	On a draft the edit is stored right away (200). On a published process every question whose
//	@Description	text changes (or every question, when the process's title, description, header or
//	@Description	streamUri change, since each question's document carries them) gets a new election
//	@Description	metadata document at a new metadataURL, committed on chain with a SET_PROCESS_METADATA tx:
//	@Description	the request answers 202 with a jobId to poll at GET /jobs/{jobId} (type
//	@Description	`set_process_metadata`). The job result lists one entry per question with the
//	@Description	metadataURL/metadataHash its tx commits and its own status; a question's stored text, URL
//	@Description	and hash change only once its tx is mined, so what the API serves always matches what its
//	@Description	election commits to on chain. Until its tx is final the new version is pending: votes
//	@Description	attesting either the stored or the pending hash are relayed and the chain decides, any
//	@Description	other hash is rejected with 409 (40904). A tx not confirmed within the job's wait leaves its
//	@Description	entry, and the job, pending until the chain shows it (completed) or the Vochain mempool TTL
//	@Description	has passed without it (failed); reads of the process settle it, and so does a re-check
//	@Description	once the TTL has passed. The process's own fields are stored once every question landed; if
//	@Description	some failed, send the same edit again: the questions already updated are skipped. Previous
//	@Description	metadata documents stay served at their URLs.
//	@Description
//	@Description	An edit that changes nothing is answered 200 without any tx.
//	@Description
//	@Description	409: a publish is in progress (40903); a previous metadata edit of the process is not final
//	@Description	yet (40905); or an election whose metadata would change is not READY or
//	@Description	PAUSED (40906) — the chain accepts metadata updates only in those states.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string							true	"Process ID"
//	@Param			request		body		apicommon.VotingProcessMetadata	true	"Voting process metadata"
//	@Success		200			{string}	string							"Stored, no on-chain change needed"
//	@Success		202			{object}	apicommon.EnqueuedResponse
//	@Failure		400			{object}	errors.Error	"Malformed body, or question/choice counts do not match"
//	@Failure		401			{object}	errors.Error
//	@Failure		404			{object}	errors.Error
//	@Failure		409			{object}	errors.Error	"Publish or metadata update in progress, or election not editable"
//	@Failure		503			{object}	errors.Error
//	@Router			/processes/{processId}/metadata [put]
func (a *API) updateVotingProcessMetadataHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	req := &apicommon.VotingProcessMetadata{}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	vp, questions, ok := a.loadProcessForMetadataEdit(w, oid)
	if !ok {
		return
	}
	if !user.HasRoleFor(vp.OrgAddress, db.ManagerRole) && !user.HasRoleFor(vp.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of the organization").Write(w)
		return
	}
	if refusePublishInProgress(w, vp) {
		return
	}
	if problem := metadataShapeProblem(req, questions); problem != "" {
		errors.ErrMalformedBody.With(problem).Write(w)
		return
	}
	if metadataUnchanged(vp, questions, req) {
		apicommon.HTTPWriteOK(w)
		return
	}
	if !vp.Published {
		a.updateDraftMetadata(w, oid, req)
		return
	}
	a.updatePublishedMetadata(w, user, oid, req)
}

// loadProcessForMetadataEdit loads a process and its questions, writing the proper error on failure.
func (a *API) loadProcessForMetadataEdit(
	w http.ResponseWriter, oid bson.ObjectID,
) (*db.VotingProcess, []db.VotingProcessQuestion, bool) {
	vp, questions, err := a.db.ProcessWithQuestions(oid)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrProcessNotFound.Write(w)
			return nil, nil, false
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return nil, nil, false
	}
	return vp, questions, true
}

// metadataShapeProblem reports why an edit does not fit the process's questions, which it matches
// by position: the question count and each question's choice count must be the stored ones.
func metadataShapeProblem(req *apicommon.VotingProcessMetadata, questions []db.VotingProcessQuestion) string {
	if len(req.Questions) != len(questions) {
		return fmt.Sprintf("expected %d questions, got %d", len(questions), len(req.Questions))
	}
	for i := range questions {
		if len(req.Questions[i].Choices) != len(questions[i].Choices) {
			return fmt.Sprintf("question %d: expected %d choices, got %d",
				i, len(questions[i].Choices), len(req.Questions[i].Choices))
		}
	}
	return ""
}

// metadataUnchanged reports whether an edit (already matched against the process's shape) leaves
// every text exactly as stored.
func metadataUnchanged(vp *db.VotingProcess, questions []db.VotingProcessQuestion, req *apicommon.VotingProcessMetadata) bool {
	if !processTextUnchanged(vp, req) {
		return false
	}
	for i := range questions {
		if !questionTextUnchanged(&questions[i], &req.Questions[i]) {
			return false
		}
	}
	return true
}

// processTextUnchanged reports whether an edit leaves the process's own text as stored.
func processTextUnchanged(vp *db.VotingProcess, req *apicommon.VotingProcessMetadata) bool {
	return maps.Equal(vp.Title, req.Title) && maps.Equal(vp.Description, req.Description) &&
		vp.Header == req.Header && vp.StreamURI == req.StreamURI
}

// questionTextUnchanged reports whether an edit leaves a question's text as stored.
func questionTextUnchanged(q *db.VotingProcessQuestion, sent *apicommon.VotingProcessMetadataQuestion) bool {
	if !maps.Equal(q.Title, sent.Title) || !maps.Equal(q.Description, sent.Description) {
		return false
	}
	for j := range q.Choices {
		if !maps.Equal(q.Choices[j].Title, sent.Choices[j].Title) {
			return false
		}
	}
	return true
}

// processTextOf returns the process-level text an edit sets.
func processTextOf(req *apicommon.VotingProcessMetadata) *db.ProcessText {
	return &db.ProcessText{
		Title:       req.Title,
		Description: req.Description,
		Header:      req.Header,
		StreamURI:   req.StreamURI,
	}
}

// questionTextUpdate returns the text edit of one question, without any metadata repointing.
func questionTextUpdate(q *db.VotingProcessQuestion, sent *apicommon.VotingProcessMetadataQuestion) *db.QuestionTextUpdate {
	titles := make([]db.MultiLangString, len(sent.Choices))
	for j := range sent.Choices {
		titles[j] = sent.Choices[j].Title
	}
	return &db.QuestionTextUpdate{
		ID:           q.ID,
		Title:        sent.Title,
		Description:  sent.Description,
		ChoiceTitles: titles,
	}
}

// editedQuestion returns a copy of q carrying the edited text, leaving q and its choices untouched.
func editedQuestion(q *db.VotingProcessQuestion, sent *apicommon.VotingProcessMetadataQuestion) *db.VotingProcessQuestion {
	edited := *q
	edited.Title = sent.Title
	edited.Description = sent.Description
	edited.Choices = make([]db.Choice, len(q.Choices))
	for j := range q.Choices {
		edited.Choices[j] = q.Choices[j]
		edited.Choices[j].Title = sent.Choices[j].Title
	}
	return &edited
}

// updateDraftMetadata stores a text edit of a draft. It holds the publish claim while it writes, so
// a publish cannot start on a half-edited draft and a full draft PUT cannot reshape the questions
// underneath the edit (SetVotingProcessDraft refuses a claimed process).
func (a *API) updateDraftMetadata(w http.ResponseWriter, oid bson.ObjectID, req *apicommon.VotingProcessMetadata) {
	claimed, err := a.db.ClaimVotingProcessForPublish(oid)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if !claimed {
		// being published right now, or published since it was read: either way not a draft to edit
		errors.ErrPublishInProgress.Write(w)
		return
	}
	defer func() {
		if e := a.db.ClearVotingProcessPublishing(oid); e != nil {
			log.Warnw("could not release draft after metadata edit", "processId", oid.Hex(), "error", e)
		}
	}()
	// re-read under the claim: a full draft PUT may have reshaped the questions since the first read
	_, questions, ok := a.loadProcessForMetadataEdit(w, oid)
	if !ok {
		return
	}
	if problem := metadataShapeProblem(req, questions); problem != "" {
		errors.ErrMalformedBody.With(problem).Write(w)
		return
	}
	for i := range questions {
		if err := a.db.SetQuestionText(questionTextUpdate(&questions[i], &req.Questions[i])); err != nil {
			if err == db.ErrNotFound {
				errors.ErrStaleUpdate.Withf("question %d changed while being edited", i).Write(w)
				return
			}
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
	}
	if err := a.db.SetVotingProcessText(oid, processTextOf(req)); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	apicommon.HTTPWriteOK(w)
}

// metadataEdit is a text edit matched against the process and questions it applies to.
type metadataEdit struct {
	vp        *db.VotingProcess
	questions []db.VotingProcessQuestion
	req       *apicommon.VotingProcessMetadata
}

// metadataTarget is one question of a published process whose election metadata an edit changes.
type metadataTarget struct {
	upstreamID internal.HexBytes
	// update is the question's text edit, repointed at the new metadata document and hash
	update *db.QuestionTextUpdate
}

// updatePublishedMetadata puts a text edit of a published process on chain. Under the process's
// metadata update claim it builds every changed question's new metadata document through the same
// path publish uses, stores it, and enqueues one job submitting a SET_PROCESS_METADATA tx per
// changed question. When no election metadata changes, the process's own text is stored at once.
func (a *API) updatePublishedMetadata(
	w http.ResponseWriter, user *db.User, oid bson.ObjectID, req *apicommon.VotingProcessMetadata,
) {
	// settle an earlier edit whose txs are final by now, which releases its claim
	if vp, questions, err := a.db.ProcessWithQuestions(oid); err == nil {
		a.reconcilePendingMetadata(vp, questions)
	}
	claimed, err := a.db.ClaimVotingProcessMetadataUpdate(oid)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if !claimed {
		errors.ErrMetadataUpdateInProgress.Write(w)
		return
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if e := a.db.ClearVotingProcessMetadataUpdate(oid); e != nil {
			log.Warnw("could not release metadata update claim", "processId", oid.Hex(), "error", e)
		}
	}()
	// re-read under the claim: an update that finished since the first read changed the stored text
	vp, questions, ok := a.loadProcessForMetadataEdit(w, oid)
	if !ok {
		return
	}
	if problem := metadataShapeProblem(req, questions); problem != "" {
		errors.ErrMalformedBody.With(problem).Write(w)
		return
	}
	// a claim reclaimed once stale may still find an earlier edit pending: a question never gets a
	// second pending version
	if hasPendingMetadata(questions) {
		errors.ErrMetadataUpdateInProgress.Write(w)
		return
	}
	targets, apiErr := a.metadataTargets(&metadataEdit{vp: vp, questions: questions, req: req}, user.Email)
	if apiErr != nil {
		apiErr.Write(w)
		return
	}
	if len(targets) == 0 {
		// every election already commits to this edit (an earlier attempt put all of it on chain
		// but did not store the process's own text), so only that is left to store
		if err := a.db.SetVotingProcessText(oid, processTextOf(req)); err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		apicommon.HTTPWriteOK(w)
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
	worker := &metadataUpdateWorker{
		a: a, processID: oid, text: processTextOf(req), org: org, user: user,
		orgSigner: orgSigner, targets: targets, jobID: jobID,
	}
	if err := a.db.CreateTxJobWithResult(jobID, db.JobTypeSetProcessMetadata, org.Address,
		&db.JobResult{Questions: worker.results(db.JobStatusPending, "")}); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	orgLock := a.orgTxLocks.lock(org.Address)
	if !a.enqueueTx(txTask{
		jobID: jobID,
		run: func() (*db.JobResult, error) {
			defer orgLock.Unlock()
			return worker.run()
		},
		record: worker.record,
	}) {
		orgLock.Unlock()
		if e := a.db.SetJobStatus(jobID, db.JobStatusFailed, nil, "tx queue full"); e != nil {
			log.Warnw("could not mark job failed after full queue", "error", e)
		}
		errors.ErrTxQueueFull.Write(w)
		return
	}
	// the worker now owns the claim, released once every tx of the edit is final
	committed = true
	apicommon.HTTPWriteJSONStatus(w, http.StatusAccepted, &apicommon.EnqueuedResponse{JobID: jobID})
}

// metadataTargets returns the questions whose election metadata an edit changes, each with its new
// metadata document already stored. A question's document carries its own text plus all of the
// process's text (title, description, header and streamUri), so a change to the latter touches
// every question. A question to change must have an election that is READY or PAUSED, the only
// states the chain accepts the update in; the stored status is checked here and the chain stays the
// final arbiter.
func (a *API) metadataTargets(edit *metadataEdit, owner string) ([]metadataTarget, *errors.Error) {
	vp, req := edit.vp, edit.req
	edited := *vp
	edited.Title, edited.Description = req.Title, req.Description
	edited.Header, edited.StreamURI = req.Header, req.StreamURI
	processChanged := !processTextUnchanged(vp, req)
	var targets []metadataTarget
	for i := range edit.questions {
		q, sent := &edit.questions[i], &req.Questions[i]
		if !processChanged && questionTextUnchanged(q, sent) {
			continue
		}
		doc, hash, err := a.questionMetadataDoc(&edited, editedQuestion(q, sent))
		if err != nil {
			apiErr := errors.ErrGenericInternalServerError.WithErr(err)
			return nil, &apiErr
		}
		// already on this version: an earlier edit that failed on other questions updated this one
		if bytes.Equal(hash, q.MetadataHash) {
			continue
		}
		if len(q.UpstreamID) == 0 {
			apiErr := errors.ErrMetadataNotEditable.Withf("question %d has no election", i)
			return nil, &apiErr
		}
		if q.Status != db.QuestionStatusReady && q.Status != db.QuestionStatusPaused {
			apiErr := errors.ErrMetadataNotEditable.Withf("question %d is %s", i, q.Status)
			return nil, &apiErr
		}
		// content-addressed: every version keeps being served at its own URL, which is what lets a
		// vote or an audit be checked against the text shown at the time
		objectName, err := a.objectStorage.PutJSON(doc, owner)
		if err != nil {
			apiErr := errors.ErrGenericInternalServerError.WithErr(err)
			return nil, &apiErr
		}
		update := questionTextUpdate(q, sent)
		update.MetadataURL = a.objectStorage.LocalURL(objectName)
		update.MetadataHash = hash
		targets = append(targets, metadataTarget{upstreamID: q.UpstreamID, update: update})
	}
	return targets, nil
}

// metadataUpdateWorker carries a metadata edit of a published process onto the tx worker pool.
type metadataUpdateWorker struct {
	a         *API
	processID bson.ObjectID
	text      *db.ProcessText
	org       *db.Organization
	user      *db.User
	orgSigner *ethereum.SignKeys
	targets   []metadataTarget
	jobID     string
	// outcome is the per-target result run fills in, index-aligned with targets
	outcome []db.QuestionMetadataJobResult
	// unconfirmed counts the targets run left pending: their tx may still mine
	unconfirmed int
}

// results returns one job result entry per target, all with the given status and error.
func (mw *metadataUpdateWorker) results(status db.JobStatus, errMsg string) []db.QuestionMetadataJobResult {
	out := make([]db.QuestionMetadataJobResult, len(mw.targets))
	for i, t := range mw.targets {
		out[i] = db.QuestionMetadataJobResult{
			QuestionID:   t.update.ID.Hex(),
			ProcessID:    t.upstreamID,
			MetadataURL:  t.update.MetadataURL,
			MetadataHash: t.update.MetadataHash,
			Status:       status,
			Error:        errMsg,
		}
	}
	return out
}

// run signs one SET_PROCESS_METADATA tx per target with consecutive nonces from the organization
// account's current nonce, submits them as one batch and confirms each. A question's stored text,
// URL and hash are written only once its tx is mined (or the chain already commits its new hash),
// so the API never serves a version its election does not commit to.
//
// Each question's new version, and the process's new text, are recorded as pending before anything
// is submitted. A tx known not to land (rejected, or not sent after an earlier one was) drops its
// pending version and fails its question. One not confirmed within the wait stays pending, and so
// does the job: reconcileQuestionMetadata settles it once the chain shows its hash or its mempool
// TTL has passed. The process's own text, which every question's document carries, is stored once
// every question landed.
func (mw *metadataUpdateWorker) run() (*db.JobResult, error) {
	a := mw.a
	a.metadataEditsRunning.Store(mw.processID.Hex(), struct{}{})
	mw.outcome = mw.results(db.JobStatusPending, "")
	stxs, err := mw.signBatch()
	if err == nil {
		err = mw.recordPending()
	}
	if err != nil {
		for i := range mw.targets {
			a.dropPendingMetadata(mw.processID, mw.targets[i].update.ID)
		}
		mw.outcome = mw.results(db.JobStatusFailed, err.Error())
		return &db.JobResult{Questions: mw.outcome}, err
	}
	submitted, submitErr := a.account.SubmitSignedTxBatch(stxs)
	if submitErr == nil && len(submitted) != len(stxs) {
		submitErr = fmt.Errorf("unexpected batch result count %d for %d txs", len(submitted), len(stxs))
	}
	failed := 0
	for i := range mw.targets {
		t := &mw.targets[i]
		// a failed batch submit leaves unknown which txs reached the mempool
		mayStillMine, txErr := submitErr != nil, submitErr
		if txErr == nil {
			mayStillMine, txErr = mw.confirm(submitted[i])
		}
		// the tx may have mined after all, or an earlier attempt whose result was lost already
		// committed this very version: the chain has the final word
		if txErr != nil && !mw.chainCommits(t) {
			if mayStillMine {
				mw.unconfirmed++
				mw.outcome[i].Error = fmt.Sprintf("not confirmed yet: %v", txErr)
				continue
			}
			failed++
			mw.outcome[i].Status, mw.outcome[i].Error = db.JobStatusFailed, txErr.Error()
			a.dropPendingMetadata(mw.processID, t.update.ID)
			continue
		}
		if err := a.db.SetQuestionText(t.update); err != nil {
			// still pending, so the next reconcile stores it
			mw.unconfirmed++
			mw.outcome[i].Error = fmt.Sprintf("metadata updated on chain but not stored yet: %v", err)
			continue
		}
		mw.outcome[i].Status = db.JobStatusCompleted
	}
	result := &db.JobResult{Questions: mw.outcome}
	if failed > 0 {
		return result, fmt.Errorf("%d of %d questions failed to update their metadata", failed, len(mw.targets))
	}
	if mw.unconfirmed > 0 {
		return result, nil
	}
	if err := a.db.SetVotingProcessText(mw.processID, mw.text); err != nil {
		return result, fmt.Errorf("questions updated but the process text was not stored, send the edit again: %w", err)
	}
	return result, nil
}

// recordPending stores the edit as pending: the process's new text and each target question's new
// version, which reconcileQuestionMetadata applies once the chain shows it committed.
func (mw *metadataUpdateWorker) recordPending() error {
	if err := mw.a.db.SetVotingProcessPendingText(mw.processID, mw.text); err != nil {
		return fmt.Errorf("could not record pending process text: %w", err)
	}
	for _, t := range mw.targets {
		if err := mw.a.db.SetQuestionPendingMetadata(t.update.ID, t.update.Pending(mw.jobID)); err != nil {
			return fmt.Errorf("could not record pending metadata of question %s: %w", t.update.ID.Hex(), err)
		}
	}
	return nil
}

// dropPendingMetadata discards a question's pending metadata edit, which will not land. The
// process's pending text goes first: once no question is pending it would otherwise be stored,
// describing this question wrongly. It reports whether the question's pending edit was cleared.
func (a *API) dropPendingMetadata(processID, questionID bson.ObjectID) bool {
	if err := a.db.ClearVotingProcessPendingText(processID); err != nil {
		log.Warnw("could not clear pending process text", "processId", processID.Hex(), "error", err)
		return false
	}
	if err := a.db.ClearQuestionPendingMetadata(questionID); err != nil {
		log.Warnw("could not clear pending question metadata", "questionId", questionID.Hex(), "error", err)
		return false
	}
	return true
}

// reconcileQuestionMetadata settles a question's pending metadata edit against the metadata hash
// its election commits to on chain (chainHash). A pending edit lives until its tx is final: when the
// chain commits its hash it is applied (text, URL and hash) and cleared, in the store and in q; when
// db.PendingMetadataFinalAfter has passed since it was submitted without that, its tx was evicted
// from the mempool and never will mine, so it is dropped. Either way its job is then settled, which
// may store the process text; that text is returned, or nil. A question with no pending edit, or
// one not final yet, is left as is. Failures are logged and leave the question to a later reconcile.
func (a *API) reconcileQuestionMetadata(q *db.VotingProcessQuestion, chainHash []byte) *db.ProcessText {
	pending := q.PendingMetadata
	if pending == nil {
		return nil
	}
	switch {
	case bytes.Equal(pending.MetadataHash, chainHash):
		update := pending.Update(q.ID)
		if err := a.db.SetQuestionText(update); err != nil {
			log.Warnw("could not apply pending question metadata", "questionId", q.ID.Hex(), "error", err)
			return nil
		}
		q.Title, q.Description = update.Title, update.Description
		for j := range q.Choices {
			if j < len(update.ChoiceTitles) {
				q.Choices[j].Title = update.ChoiceTitles[j]
			}
		}
		q.MetadataURL, q.MetadataHash = update.MetadataURL, update.MetadataHash
	case time.Since(pending.Since) > db.PendingMetadataFinalAfter:
		if !a.dropPendingMetadata(q.ProcessID, q.ID) {
			return nil
		}
	default:
		return nil
	}
	q.PendingMetadata = nil
	return a.settleMetadataEdit(q.ProcessID, pending.JobID)
}

// reconcilePendingMetadata runs reconcileQuestionMetadata, against its election as read from the
// chain, on each question of a process read that has a pending metadata edit, so the read serves
// (and stores) an edit whose tx mined after its job stopped waiting. vp, when given, takes the
// process text that lands with it. Questions without a pending edit, the common case, cost nothing.
func (a *API) reconcilePendingMetadata(vp *db.VotingProcess, questions []db.VotingProcessQuestion) {
	for i := range questions {
		q := &questions[i]
		if q.PendingMetadata == nil || len(q.UpstreamID) == 0 {
			continue
		}
		election, err := a.account.Election(q.UpstreamID)
		if err != nil {
			log.Warnw("could not read election to reconcile pending metadata",
				"questionId", q.ID.Hex(), "election", q.UpstreamID.String(), "error", err)
			continue
		}
		text := a.reconcileQuestionMetadata(q, election.MetadataHash)
		if text != nil && vp != nil {
			vp.Title, vp.Description, vp.Header, vp.StreamURI = text.Title, text.Description, text.Header, text.StreamURI
		}
	}
}

// hasPendingMetadata reports whether any of the questions has a pending metadata edit.
func hasPendingMetadata(questions []db.VotingProcessQuestion) bool {
	for i := range questions {
		if questions[i].PendingMetadata != nil {
			return true
		}
	}
	return false
}

// settleMetadataEdit finishes the set_process_metadata job jobID of a process once none of its
// questions is pending any more. Each entry still pending becomes completed when its question now
// stores the entry's hash and failed otherwise (its tx never landed); the process's pending text is
// stored when every entry completed and dropped otherwise; then the job is recorded as final and the
// process's metadata update claim released. It does nothing while the job's worker still runs (the
// worker records it), for a job already final, or while a question is still pending. It returns the
// process text it stored, if any.
func (a *API) settleMetadataEdit(processID bson.ObjectID, jobID string) *db.ProcessText {
	if _, running := a.metadataEditsRunning.Load(processID.Hex()); running {
		return nil
	}
	job, err := a.db.Job(jobID)
	if err != nil {
		log.Warnw("could not read metadata edit job to settle it", "jobId", jobID, "error", err)
		return nil
	}
	if job.Status != db.JobStatusPending || job.Result == nil {
		return nil
	}
	_, questions, err := a.db.ProcessWithQuestions(processID)
	if err != nil {
		log.Warnw("could not read process to settle its metadata edit", "processId", processID.Hex(), "error", err)
		return nil
	}
	byID := make(map[string]*db.VotingProcessQuestion, len(questions))
	for i := range questions {
		byID[questions[i].ID.Hex()] = &questions[i]
	}
	result := &db.JobResult{Questions: slices.Clone(job.Result.Questions)}
	failed := 0
	for i := range result.Questions {
		entry := &result.Questions[i]
		q := byID[entry.QuestionID]
		if q != nil && q.PendingMetadata != nil && q.PendingMetadata.JobID == jobID {
			return nil
		}
		if entry.Status == db.JobStatusPending {
			if q != nil && bytes.Equal(q.MetadataHash, entry.MetadataHash) {
				entry.Status, entry.Error = db.JobStatusCompleted, ""
			} else {
				entry.Status, entry.Error = db.JobStatusFailed, "tx not on chain once past the mempool TTL"
			}
		}
		if entry.Status == db.JobStatusFailed {
			failed++
		}
	}
	var text *db.ProcessText
	status, errMsg := db.JobStatusCompleted, ""
	if failed > 0 {
		status = db.JobStatusFailed
		errMsg = fmt.Sprintf("%d of %d questions failed to update their metadata", failed, len(result.Questions))
		if err := a.db.ClearVotingProcessPendingText(processID); err != nil {
			log.Warnw("could not clear pending process text", "processId", processID.Hex(), "error", err)
		}
	} else if text, err = a.db.ApplyVotingProcessPendingText(processID); err != nil {
		log.Warnw("could not store pending process text", "processId", processID.Hex(), "error", err)
		return nil
	}
	if err := a.db.SetJobStatus(jobID, status, result, errMsg); err != nil {
		log.Warnw("could not record settled metadata edit job", "jobId", jobID, "error", err)
	}
	if err := a.db.ClearVotingProcessMetadataUpdate(processID); err != nil {
		log.Warnw("could not release metadata update claim", "processId", processID.Hex(), "error", err)
	}
	return text
}

// scheduleMetadataRecheck settles an edit left with unconfirmed txs once they are all final, in case
// no read of the process does it first. A restart loses it; the next read or edit of the process
// then settles the edit, and the claim's MetadataUpdateStaleAfter bounds it.
func (a *API) scheduleMetadataRecheck(processID bson.ObjectID, jobID string) {
	time.AfterFunc(db.PendingMetadataFinalAfter, func() {
		vp, questions, err := a.db.ProcessWithQuestions(processID)
		if err != nil {
			log.Warnw("could not read process to re-check its metadata edit", "processId", processID.Hex(), "error", err)
			return
		}
		a.reconcilePendingMetadata(vp, questions)
		a.settleMetadataEdit(processID, jobID)
	})
}

// signBatch builds, funds, permission-checks and signs one SET_PROCESS_METADATA tx per target with
// consecutive nonces, so they can go out as one batch and mine together.
func (mw *metadataUpdateWorker) signBatch() ([][]byte, error) {
	a := mw.a
	nonce, err := a.account.AccountNonce(mw.org.Address)
	if err != nil {
		return nil, fmt.Errorf("could not read account nonce: %w", err)
	}
	stxs := make([][]byte, 0, len(mw.targets))
	for i, t := range mw.targets {
		tx, err := a.account.BuildSetProcessMetadataTx(&account.SetProcessMetadataParams{
			ProcessID:    t.upstreamID,
			MetadataURL:  t.update.MetadataURL,
			MetadataHash: t.update.MetadataHash,
			Nonce:        nonce + uint32(i),
		})
		if err != nil {
			return nil, err
		}
		fundedTx, txType, err := a.account.FundTransaction(tx, mw.orgSigner.Address())
		if err != nil {
			return nil, fmt.Errorf("could not fund metadata update: %w", err)
		}
		if txType == nil || *txType != models.TxType_SET_PROCESS_METADATA {
			return nil, fmt.Errorf("unexpected tx type for metadata update")
		}
		if ok, err := a.subscriptions.HasTxPermission(fundedTx, *txType, mw.org, mw.user); err != nil || !ok {
			return nil, fmt.Errorf("metadata update not permitted: %v", err)
		}
		stx, err := a.account.SignTransaction(fundedTx, mw.orgSigner)
		if err != nil {
			return nil, err
		}
		stxs = append(stxs, stx)
	}
	return stxs, nil
}

// confirm waits for one submitted batch item to be mined. On error, mayStillMine reports whether
// the tx reached the chain's mempool and can still be mined later (the wait timed out), as opposed
// to having been rejected or never sent.
func (mw *metadataUpdateWorker) confirm(item account.BatchItemResult) (mayStillMine bool, err error) {
	switch item.Status {
	case account.BatchSubmitted:
		return true, mw.a.account.WaitTxMined(item.Hash)
	case account.BatchFailed:
		return false, fmt.Errorf("tx rejected: %s", item.Err)
	default:
		return false, fmt.Errorf("tx not sent after an earlier one in the batch was rejected")
	}
}

// chainCommits reports whether the target's election already commits to its new metadata.
func (mw *metadataUpdateWorker) chainCommits(t *metadataTarget) bool {
	election, err := mw.a.account.Election(t.upstreamID)
	if err != nil {
		log.Warnw("could not read election to reconcile metadata update",
			"upstreamId", t.upstreamID.String(), "error", err)
		return false
	}
	return election.MetadataURL == t.update.MetadataURL && bytes.Equal(election.MetadataHash, t.update.MetadataHash)
}

// record writes the job's outcome with its per-question entries. When some tx is unconfirmed the
// job stays pending, and so does the process's metadata update claim, until settleMetadataEdit
// finishes it; otherwise the job is final and the claim is released so the next edit can start.
func (mw *metadataUpdateWorker) record(result *db.JobResult, runErr error) {
	a := mw.a
	// the worker owns the outcome until it is written: settleMetadataEdit skips a running edit
	defer a.metadataEditsRunning.Delete(mw.processID.Hex())
	if result != nil && mw.unconfirmed > 0 {
		if e := a.db.SetJobResult(mw.jobID, result); e != nil {
			log.Warnw("could not record metadata update job", "jobId", mw.jobID, "error", e)
		}
		a.metadataEditsRunning.Delete(mw.processID.Hex())
		// a read may have reconciled the unconfirmed questions while the worker waited
		a.settleMetadataEdit(mw.processID, mw.jobID)
		a.scheduleMetadataRecheck(mw.processID, mw.jobID)
		return
	}
	status, errMsg := db.JobStatusCompleted, ""
	if runErr != nil {
		status, errMsg = db.JobStatusFailed, runErr.Error()
	}
	if result == nil {
		// run panicked: nothing below the panic is known to have happened
		result = &db.JobResult{Questions: mw.outcome}
		if result.Questions == nil {
			result.Questions = mw.results(db.JobStatusFailed, errMsg)
		}
	}
	if e := a.db.SetJobStatus(mw.jobID, status, result, errMsg); e != nil {
		log.Warnw("could not record metadata update job", "jobId", mw.jobID, "error", e)
	}
	if e := a.db.ClearVotingProcessMetadataUpdate(mw.processID); e != nil {
		log.Warnw("could not release metadata update claim", "processId", mw.processID.Hex(), "error", e)
	}
}
