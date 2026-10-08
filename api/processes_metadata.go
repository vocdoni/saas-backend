package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"reflect"
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
//	@Description	Read the editable content of a voting process: its title, description, header and
//	@Description	streamUri, and each question's title, description and choices (title and display info,
//	@Description	`meta`), in the order the process stores them. Public for a published process; a draft
//	@Description	is visible only to a Manager/Admin of the organization (or a voting:write API key acting
//	@Description	as one) and is a 404 otherwise. The same shape is the body PUT
//	@Description	/processes/{processId}/metadata takes.
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
//	@Description	Edit the content voters see of a voting process — its title, description, header and
//	@Description	streamUri, and each question's title, description, choice titles and choice display
//	@Description	info (`meta`: description, image...) — including after it is published. Nothing
//	@Description	structural can change: the body has no field for a question's or choice's type, setup,
//	@Description	value or ballot protocol, and questions and choices are matched by position, so their
//	@Description	counts must equal the process's exactly (400 otherwise). Send back the shape read from
//	@Description	GET /processes/{processId}/metadata; a choice without `meta` keeps its display info.
//	@Description	Requires the Manager or Admin role (or a voting:write API key). External images (the
//	@Description	header, choice images) are imported as on create; 422 (40179) names the field and URL
//	@Description	of one that cannot be, and nothing is changed.
//	@Description
//	@Description	On a draft the edit is stored right away (200). On a published process each election
//	@Description	whose metadata document changes gets a new document at a new metadataURL, committed on
//	@Description	chain with a SET_PROCESS_METADATA tx: the process's parent election when its title,
//	@Description	description, header or streamUri change, and each question whose title, description or
//	@Description	choices change. The request answers 202 with a jobId to poll at GET /jobs/{jobId} (type
//	@Description	`set_process_metadata`). The job result has one `questions` entry per changed question
//	@Description	and a `parent` entry when the parent changes, each with the metadataURL/metadataHash its
//	@Description	tx commits and its own status. Stored content, URL and hash change only once the tx is
//	@Description	mined, so what the API serves always matches what its election commits to on chain.
//	@Description	Until a question's tx is final its new version is pending: votes attesting either its
//	@Description	stored or its pending hash are relayed and the chain decides, any other hash is rejected
//	@Description	with 409 (40904). A tx not confirmed within the job's wait leaves its entry, and the job,
//	@Description	pending until the chain shows it (completed) or the Vochain mempool TTL has passed
//	@Description	without it (failed); reads of the process settle it, and so does a re-check once the TTL
//	@Description	has passed. If some failed, send the same edit again: the elections already updated are
//	@Description	skipped. Previous metadata documents stay served at their URLs.
//	@Description
//	@Description	An edit that changes no document is answered 200 without any tx.
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
//	@Failure		422			{object}	errors.Error	"Image could not be imported or read (40179)"
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
	// external images are copied into the object storage and the edit points at the copies
	if err := a.importImages(metadataImageRefs(req), user.Email); err != nil {
		writeSubscriptionError(w, err)
		return
	}
	if !vp.Published {
		if metadataUnchanged(vp, questions, req) {
			apicommon.HTTPWriteOK(w)
			return
		}
		a.updateDraftMetadata(w, oid, req)
		return
	}
	a.updatePublishedMetadata(w, user, oid, req)
}

// metadataImageRefs returns the image URLs an edit carries: the header and the images of the
// choice display info it sends.
func metadataImageRefs(req *apicommon.VotingProcessMetadata) []imageRef {
	var refs []imageRef
	if req.Header != "" {
		refs = append(refs, imageRef{field: "header", url: req.Header, set: func(u string) { req.Header = u }})
	}
	for i := range req.Questions {
		for j := range req.Questions[i].Choices {
			if meta := req.Questions[i].Choices[j].Meta; meta != nil {
				refs = append(refs, choiceImageRefs(fmt.Sprintf("questions[%d].choices[%d].image", i, j), meta)...)
			}
		}
	}
	return refs
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
// every text and display info exactly as stored.
func metadataUnchanged(vp *db.VotingProcess, questions []db.VotingProcessQuestion, req *apicommon.VotingProcessMetadata) bool {
	if !processTextUnchanged(vp, req) {
		return false
	}
	for i := range questions {
		if !questionContentUnchanged(&questions[i], &req.Questions[i]) {
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

// questionContentUnchanged reports whether an edit leaves a question's text and its choices'
// display info as stored.
func questionContentUnchanged(q *db.VotingProcessQuestion, sent *apicommon.VotingProcessMetadataQuestion) bool {
	if !maps.Equal(q.Title, sent.Title) || !maps.Equal(q.Description, sent.Description) {
		return false
	}
	_, choicesMeta := account.QuestionDisplayMeta(q.Metadata)
	for j := range q.Choices {
		if !maps.Equal(q.Choices[j].Title, sent.Choices[j].Title) {
			return false
		}
		if meta := sent.Choices[j].Meta; meta != nil && !reflect.DeepEqual(meta, choicesMeta[q.Choices[j].Value]) {
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

// editedQuestionMetadata returns the question's free-form metadata with the display info the edit
// sends for its choices: each choice that carries meta gets it as its metadata.choices entry
// (joined by its value), replacing the one it had; the other entries and keys are kept. It returns
// nil when the edit sends no choice meta, i.e. leaves the metadata as stored.
func editedQuestionMetadata(q *db.VotingProcessQuestion, sent *apicommon.VotingProcessMetadataQuestion) map[string]any {
	replacements := make(map[uint32]map[string]any)
	var order []uint32
	for j := range sent.Choices {
		if meta := sent.Choices[j].Meta; meta != nil {
			value := q.Choices[j].Value
			entry := maps.Clone(meta)
			entry["value"] = value
			replacements[value] = entry
			order = append(order, value)
		}
	}
	if len(replacements) == 0 {
		return nil
	}
	edited := maps.Clone(q.Metadata)
	if edited == nil {
		edited = make(map[string]any)
	}
	entries := make([]any, 0, len(order))
	placed := make(map[uint32]bool, len(order))
	for _, entry := range account.ChoicesMetaEntries(q.Metadata) {
		value, ok := account.ChoiceMetaValue(entry)
		if replacement, replaced := replacements[value]; ok && replaced {
			if !placed[value] {
				entries = append(entries, replacement)
				placed[value] = true
			}
			continue
		}
		entries = append(entries, maps.Clone(entry))
	}
	for _, value := range order {
		if !placed[value] {
			entries = append(entries, replacements[value])
			placed[value] = true
		}
	}
	edited["choices"] = entries
	return edited
}

// questionTextUpdate returns the content edit of one question, without any metadata repointing.
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
		Metadata:     editedQuestionMetadata(q, sent),
	}
}

// editedQuestion returns a copy of q carrying the edited content, leaving q and its choices untouched.
func editedQuestion(q *db.VotingProcessQuestion, sent *apicommon.VotingProcessMetadataQuestion) *db.VotingProcessQuestion {
	edited := *q
	edited.Title = sent.Title
	edited.Description = sent.Description
	edited.Choices = make([]db.Choice, len(q.Choices))
	for j := range q.Choices {
		edited.Choices[j] = q.Choices[j]
		edited.Choices[j].Title = sent.Choices[j].Title
	}
	if metadata := editedQuestionMetadata(q, sent); metadata != nil {
		edited.Metadata = metadata
	}
	return &edited
}

// updateDraftMetadata stores a content edit of a draft. It holds the publish claim while it writes,
// so a publish cannot start on a half-edited draft and a full draft PUT cannot reshape the questions
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

// metadataEdit is a content edit matched against the process and questions it applies to.
type metadataEdit struct {
	vp        *db.VotingProcess
	questions []db.VotingProcessQuestion
	req       *apicommon.VotingProcessMetadata
}

// metadataTarget is one election of a published process whose metadata an edit changes: a
// question's (question set) or the process's parent election (process set).
type metadataTarget struct {
	upstreamID internal.HexBytes
	// question is the question's content edit, repointed at the new metadata document and hash
	question *db.QuestionTextUpdate
	// process is the process's text edit, repointed at the parent's new metadata document and hash
	process *db.ProcessMetadataUpdate
}

// metadata returns the metadata document URL and hash the target's tx commits.
func (t *metadataTarget) metadata() (string, internal.HexBytes) {
	if t.process != nil {
		return t.process.MetadataURL, t.process.MetadataHash
	}
	return t.question.MetadataURL, t.question.MetadataHash
}

// updatePublishedMetadata puts a content edit of a published process on chain. Under the process's
// metadata update claim it builds the new metadata document of every election the edit changes
// through the same path publish uses, stores it, and enqueues one job submitting a
// SET_PROCESS_METADATA tx per changed election. A process published without a parent election has
// no document for its own text, which is then stored at once.
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
	// a claim reclaimed once stale may still find an earlier edit pending: an election never gets a
	// second pending version
	if hasPendingMetadata(vp, questions) {
		errors.ErrMetadataUpdateInProgress.Write(w)
		return
	}
	targets, err := a.metadataTargets(&metadataEdit{vp: vp, questions: questions, req: req}, user.Email)
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	// without a parent election no document carries the process's text, so it is stored as is
	if len(vp.UpstreamID) == 0 && !processTextUnchanged(vp, req) {
		if err := a.db.SetVotingProcessText(oid, processTextOf(req)); err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
	}
	if len(targets) == 0 {
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
		a: a, processID: oid, org: org, user: user,
		orgSigner: orgSigner, targets: targets, jobID: jobID,
	}
	if err := a.db.CreateTxJobWithResult(jobID, db.JobTypeSetProcessMetadata, org.Address,
		worker.jobResult(worker.results(db.JobStatusPending, ""))); err != nil {
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

// editableStatus reports whether an election in the given stored status accepts a metadata update.
func editableStatus(status string) bool {
	return status == db.QuestionStatusReady || status == db.QuestionStatusPaused
}

// metadataTargets returns the elections whose metadata document an edit changes, each with its new
// document already stored: every question whose own content changes, and the parent election when
// the process's text or header does. Documents are built exactly as on publish, images hashed from
// the stored bytes, and compared by hash with what each election commits to, so an election
// already on the edited version is skipped. An election to change must be READY or PAUSED, the
// only states the chain accepts the update in; the stored status is checked here and the chain
// stays the final arbiter.
func (a *API) metadataTargets(edit *metadataEdit, owner string) ([]metadataTarget, error) {
	vp, req := edit.vp, edit.req
	hasher := a.newImageHasher()
	var targets []metadataTarget
	for i := range edit.questions {
		q, sent := &edit.questions[i], &req.Questions[i]
		edited := editedQuestion(q, sent)
		mediaHashes, err := hasher.hashes(questionImageURLs(edited)...)
		if err != nil {
			return nil, err
		}
		doc, hash, err := metadataDoc(questionMetadataParams(edited), mediaHashes)
		if err != nil {
			return nil, errors.ErrGenericInternalServerError.WithErr(err)
		}
		if bytes.Equal(hash, q.MetadataHash) {
			continue
		}
		if len(q.UpstreamID) == 0 {
			return nil, errors.ErrMetadataNotEditable.Withf("question %d has no election", i)
		}
		if !editableStatus(q.Status) {
			return nil, errors.ErrMetadataNotEditable.Withf("question %d is %s", i, q.Status)
		}
		metadataURL, err := a.storeMetadataDoc(doc, owner)
		if err != nil {
			return nil, err
		}
		update := questionTextUpdate(q, sent)
		update.MetadataURL, update.MetadataHash = metadataURL, hash
		targets = append(targets, metadataTarget{upstreamID: q.UpstreamID, question: update})
	}
	if len(vp.UpstreamID) == 0 {
		return targets, nil
	}
	text := processTextOf(req)
	edited := *vp
	edited.Title, edited.Description, edited.Header, edited.StreamURI = text.Title, text.Description, text.Header, text.StreamURI
	// the parent keeps listing the same question elections: an edit never changes which they are
	questionElections, err := parentQuestionElections(edit.questions)
	if err != nil {
		return nil, errors.ErrGenericInternalServerError.WithErr(err)
	}
	mediaHashes, err := hasher.hashes(edited.Header)
	if err != nil {
		return nil, err
	}
	doc, hash, err := metadataDoc(electionParamsForParent(&edited, questionElections), mediaHashes)
	if err != nil {
		return nil, errors.ErrGenericInternalServerError.WithErr(err)
	}
	if bytes.Equal(hash, vp.MetadataHash) {
		return targets, nil
	}
	if !editableStatus(vp.UpstreamStatus) {
		return nil, errors.ErrMetadataNotEditable.Withf("the parent election is %s", vp.UpstreamStatus)
	}
	metadataURL, err := a.storeMetadataDoc(doc, owner)
	if err != nil {
		return nil, err
	}
	return append(targets, metadataTarget{
		upstreamID: vp.UpstreamID,
		process:    &db.ProcessMetadataUpdate{Text: *text, MetadataURL: metadataURL, MetadataHash: hash},
	}), nil
}

// storeMetadataDoc stores a metadata document content-addressed, returning its URL: every version
// keeps being served at its own URL, which is what lets a vote or an audit be checked against the
// content shown at the time.
func (a *API) storeMetadataDoc(doc []byte, owner string) (string, error) {
	objectName, err := a.objectStorage.PutJSON(doc, owner)
	if err != nil {
		return "", errors.ErrGenericInternalServerError.WithErr(err)
	}
	return a.objectStorage.LocalURL(objectName), nil
}

// metadataUpdateWorker carries a metadata edit of a published process onto the tx worker pool.
type metadataUpdateWorker struct {
	a         *API
	processID bson.ObjectID
	org       *db.Organization
	user      *db.User
	orgSigner *ethereum.SignKeys
	targets   []metadataTarget
	jobID     string
	// outcome is the per-target result run fills in, index-aligned with targets
	outcome []db.ElectionMetadataJobResult
	// unconfirmed counts the targets run left pending: their tx may still mine
	unconfirmed int
}

// results returns one entry per target, all with the given status and error, index-aligned with
// targets.
func (mw *metadataUpdateWorker) results(status db.JobStatus, errMsg string) []db.ElectionMetadataJobResult {
	out := make([]db.ElectionMetadataJobResult, len(mw.targets))
	for i := range mw.targets {
		t := &mw.targets[i]
		metadataURL, metadataHash := t.metadata()
		out[i] = db.ElectionMetadataJobResult{
			ProcessID:    t.upstreamID,
			MetadataURL:  metadataURL,
			MetadataHash: metadataHash,
			Status:       status,
			Error:        errMsg,
		}
		if t.question != nil {
			out[i].QuestionID = t.question.ID.Hex()
		}
	}
	return out
}

// jobResult arranges per-target entries (index-aligned with targets) as the job's result: the
// questions' in order, and the parent election's apart.
func (mw *metadataUpdateWorker) jobResult(entries []db.ElectionMetadataJobResult) *db.JobResult {
	result := &db.JobResult{}
	for i := range entries {
		if mw.targets[i].process != nil {
			parent := entries[i]
			result.Parent = &parent
			continue
		}
		result.Questions = append(result.Questions, entries[i])
	}
	return result
}

// run signs one SET_PROCESS_METADATA tx per target with consecutive nonces from the organization
// account's current nonce, submits them as one batch and confirms each. An election's stored
// content, URL and hash are written only once its tx is mined (or the chain already commits its
// new hash), so the API never serves a version its election does not commit to.
//
// Each election's new version is recorded as pending before anything is submitted. A tx known not
// to land (rejected, or not sent after an earlier one was) drops its pending version and fails its
// entry. One not confirmed within the wait stays pending, and so does the job: the reconcile on
// reads settles it once the chain shows its hash or its mempool TTL has passed.
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
			mw.dropPending(&mw.targets[i])
		}
		mw.outcome = mw.results(db.JobStatusFailed, err.Error())
		return mw.jobResult(mw.outcome), err
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
			mw.dropPending(t)
			continue
		}
		if err := mw.apply(t); err != nil {
			// still pending, so the next reconcile stores it
			mw.unconfirmed++
			mw.outcome[i].Error = fmt.Sprintf("metadata updated on chain but not stored yet: %v", err)
			continue
		}
		mw.outcome[i].Status = db.JobStatusCompleted
	}
	result := mw.jobResult(mw.outcome)
	if failed > 0 {
		return result, fmt.Errorf("%d of %d elections failed to update their metadata", failed, len(mw.targets))
	}
	return result, nil
}

// apply stores a target's new version, its tx being on chain.
func (mw *metadataUpdateWorker) apply(t *metadataTarget) error {
	if t.process != nil {
		return mw.a.db.SetVotingProcessMetadata(mw.processID, t.process)
	}
	return mw.a.db.SetQuestionText(t.question)
}

// recordPending stores each target's new version as pending, which the reconcile on reads applies
// once the chain shows it committed.
func (mw *metadataUpdateWorker) recordPending() error {
	for _, t := range mw.targets {
		if t.process != nil {
			if err := mw.a.db.SetVotingProcessPendingMetadata(mw.processID, t.process.Pending(mw.jobID)); err != nil {
				return fmt.Errorf("could not record pending metadata of the parent election: %w", err)
			}
			continue
		}
		if err := mw.a.db.SetQuestionPendingMetadata(t.question.ID, t.question.Pending(mw.jobID)); err != nil {
			return fmt.Errorf("could not record pending metadata of question %s: %w", t.question.ID.Hex(), err)
		}
	}
	return nil
}

// dropPending discards a target's pending version, which will not land.
func (mw *metadataUpdateWorker) dropPending(t *metadataTarget) {
	if t.process != nil {
		mw.a.dropPendingProcessMetadata(mw.processID)
		return
	}
	mw.a.dropPendingQuestionMetadata(t.question.ID)
}

// dropPendingQuestionMetadata discards a question's pending metadata edit, which will not land. It
// reports whether it was cleared.
func (a *API) dropPendingQuestionMetadata(questionID bson.ObjectID) bool {
	if err := a.db.ClearQuestionPendingMetadata(questionID); err != nil {
		log.Warnw("could not clear pending question metadata", "questionId", questionID.Hex(), "error", err)
		return false
	}
	return true
}

// dropPendingProcessMetadata discards the parent election's pending metadata edit, which will not
// land. It reports whether it was cleared.
func (a *API) dropPendingProcessMetadata(processID bson.ObjectID) bool {
	if err := a.db.ClearVotingProcessPendingMetadata(processID); err != nil {
		log.Warnw("could not clear pending process metadata", "processId", processID.Hex(), "error", err)
		return false
	}
	return true
}

// pendingOutcome is how a pending metadata edit stands against what its election commits to.
type pendingOutcome int

const (
	// pendingNotFinal: its tx may still mine.
	pendingNotFinal pendingOutcome = iota
	// pendingLanded: the election commits to its hash.
	pendingLanded
	// pendingLost: past db.PendingMetadataFinalAfter without the election committing to it, so its
	// tx was evicted from the mempool and never will mine.
	pendingLost
)

// pendingOutcomeOf judges a pending edit of hash, submitted at since, against chainHash.
func pendingOutcomeOf(hash []byte, since time.Time, chainHash []byte) pendingOutcome {
	switch {
	case bytes.Equal(hash, chainHash):
		return pendingLanded
	case time.Since(since) > db.PendingMetadataFinalAfter:
		return pendingLost
	default:
		return pendingNotFinal
	}
}

// reconcileQuestionMetadata settles a question's pending metadata edit against the metadata hash
// its election commits to on chain (chainHash): applied (content, URL and hash) and cleared, in the
// store and in q, once the chain commits its hash; dropped once final without it. Either way its
// job is then settled. A question with no pending edit, or one not final yet, is left as is.
// Failures are logged and leave the question to a later reconcile.
func (a *API) reconcileQuestionMetadata(q *db.VotingProcessQuestion, chainHash []byte) {
	pending := q.PendingMetadata
	if pending == nil {
		return
	}
	switch pendingOutcomeOf(pending.MetadataHash, pending.Since, chainHash) {
	case pendingLanded:
		update := pending.Update(q.ID)
		if err := a.db.SetQuestionText(update); err != nil {
			log.Warnw("could not apply pending question metadata", "questionId", q.ID.Hex(), "error", err)
			return
		}
		q.Title, q.Description = update.Title, update.Description
		for j := range q.Choices {
			if j < len(update.ChoiceTitles) {
				q.Choices[j].Title = update.ChoiceTitles[j]
			}
		}
		if update.Metadata != nil {
			q.Metadata = update.Metadata
		}
		q.MetadataURL, q.MetadataHash = update.MetadataURL, update.MetadataHash
	case pendingLost:
		if !a.dropPendingQuestionMetadata(q.ID) {
			return
		}
	default:
		return
	}
	q.PendingMetadata = nil
	a.settleMetadataEdit(q.ProcessID, pending.JobID)
}

// reconcileProcessMetadata is reconcileQuestionMetadata for the parent election of vp.
func (a *API) reconcileProcessMetadata(vp *db.VotingProcess, chainHash []byte) {
	pending := vp.PendingMetadata
	if pending == nil {
		return
	}
	switch pendingOutcomeOf(pending.MetadataHash, pending.Since, chainHash) {
	case pendingLanded:
		update := pending.Update()
		if err := a.db.SetVotingProcessMetadata(vp.ID, update); err != nil {
			log.Warnw("could not apply pending process metadata", "processId", vp.ID.Hex(), "error", err)
			return
		}
		vp.Title, vp.Description = update.Text.Title, update.Text.Description
		vp.Header, vp.StreamURI = update.Text.Header, update.Text.StreamURI
		vp.MetadataURL, vp.MetadataHash = update.MetadataURL, update.MetadataHash
	case pendingLost:
		if !a.dropPendingProcessMetadata(vp.ID) {
			return
		}
	default:
		return
	}
	vp.PendingMetadata = nil
	a.settleMetadataEdit(vp.ID, pending.JobID)
}

// reconcilePendingMetadata reconciles, against its election as read from the chain, every pending
// metadata edit of a process read: each question's and, when vp is given, its parent election's,
// so the read serves (and stores) an edit whose tx mined after its job stopped waiting. Elections
// without a pending edit, the common case, cost nothing.
func (a *API) reconcilePendingMetadata(vp *db.VotingProcess, questions []db.VotingProcessQuestion) {
	for i := range questions {
		q := &questions[i]
		if q.PendingMetadata == nil || len(q.UpstreamID) == 0 {
			continue
		}
		if chainHash, ok := a.chainMetadataHash(q.UpstreamID); ok {
			a.reconcileQuestionMetadata(q, chainHash)
		}
	}
	if vp == nil || vp.PendingMetadata == nil || len(vp.UpstreamID) == 0 {
		return
	}
	if chainHash, ok := a.chainMetadataHash(vp.UpstreamID); ok {
		a.reconcileProcessMetadata(vp, chainHash)
	}
}

// chainMetadataHash reads the metadata hash an election commits to on chain.
func (a *API) chainMetadataHash(electionID internal.HexBytes) ([]byte, bool) {
	election, err := a.account.Election(electionID)
	if err != nil {
		log.Warnw("could not read election to reconcile pending metadata",
			"election", electionID.String(), "error", err)
		return nil, false
	}
	return election.MetadataHash, true
}

// hasPendingMetadata reports whether the process's parent election or any of its questions has a
// pending metadata edit.
func hasPendingMetadata(vp *db.VotingProcess, questions []db.VotingProcessQuestion) bool {
	if vp.PendingMetadata != nil {
		return true
	}
	for i := range questions {
		if questions[i].PendingMetadata != nil {
			return true
		}
	}
	return false
}

// settleMetadataEdit finishes the set_process_metadata job jobID of a process once none of its
// elections is pending any more. Each entry still pending becomes completed when its question or
// process now stores the entry's hash and failed otherwise (its tx never landed); then the job is
// recorded as final and the process's metadata update claim released. It does nothing while the
// job's worker still runs (the worker records it), for a job already final, or while an election
// of the edit is still pending.
func (a *API) settleMetadataEdit(processID bson.ObjectID, jobID string) {
	if _, running := a.metadataEditsRunning.Load(processID.Hex()); running {
		return
	}
	job, err := a.db.Job(jobID)
	if err != nil {
		log.Warnw("could not read metadata edit job to settle it", "jobId", jobID, "error", err)
		return
	}
	if job.Status != db.JobStatusPending || job.Result == nil {
		return
	}
	vp, questions, err := a.db.ProcessWithQuestions(processID)
	if err != nil {
		log.Warnw("could not read process to settle its metadata edit", "processId", processID.Hex(), "error", err)
		return
	}
	if vp.PendingMetadata != nil && vp.PendingMetadata.JobID == jobID {
		return
	}
	byID := make(map[string]*db.VotingProcessQuestion, len(questions))
	for i := range questions {
		byID[questions[i].ID.Hex()] = &questions[i]
	}
	result := &db.JobResult{Questions: slices.Clone(job.Result.Questions)}
	total, failed := len(result.Questions), 0
	settle := func(entry *db.ElectionMetadataJobResult, storedHash []byte) {
		if entry.Status == db.JobStatusPending {
			if bytes.Equal(storedHash, entry.MetadataHash) {
				entry.Status, entry.Error = db.JobStatusCompleted, ""
			} else {
				entry.Status, entry.Error = db.JobStatusFailed, "tx not on chain once past the mempool TTL"
			}
		}
		if entry.Status == db.JobStatusFailed {
			failed++
		}
	}
	for i := range result.Questions {
		entry := &result.Questions[i]
		q := byID[entry.QuestionID]
		if q != nil && q.PendingMetadata != nil && q.PendingMetadata.JobID == jobID {
			return
		}
		var storedHash []byte
		if q != nil {
			storedHash = q.MetadataHash
		}
		settle(entry, storedHash)
	}
	if job.Result.Parent != nil {
		parent := *job.Result.Parent
		settle(&parent, vp.MetadataHash)
		result.Parent = &parent
		total++
	}
	status, errMsg := db.JobStatusCompleted, ""
	if failed > 0 {
		status = db.JobStatusFailed
		errMsg = fmt.Sprintf("%d of %d elections failed to update their metadata", failed, total)
	}
	if err := a.db.SetJobStatus(jobID, status, result, errMsg); err != nil {
		log.Warnw("could not record settled metadata edit job", "jobId", jobID, "error", err)
	}
	if err := a.db.ClearVotingProcessMetadataUpdate(processID); err != nil {
		log.Warnw("could not release metadata update claim", "processId", processID.Hex(), "error", err)
	}
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
	for i := range mw.targets {
		t := &mw.targets[i]
		metadataURL, metadataHash := t.metadata()
		tx, err := a.account.BuildSetProcessMetadataTx(&account.SetProcessMetadataParams{
			ProcessID:    t.upstreamID,
			MetadataURL:  metadataURL,
			MetadataHash: metadataHash,
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
	metadataURL, metadataHash := t.metadata()
	return election.MetadataURL == metadataURL && bytes.Equal(election.MetadataHash, metadataHash)
}

// record writes the job's outcome with its per-election entries. When some tx is unconfirmed the
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
		// a read may have reconciled the unconfirmed elections while the worker waited
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
		outcome := mw.outcome
		if outcome == nil {
			outcome = mw.results(db.JobStatusFailed, errMsg)
		}
		result = mw.jobResult(outcome)
	}
	if e := a.db.SetJobStatus(mw.jobID, status, result, errMsg); e != nil {
		log.Warnw("could not record metadata update job", "jobId", mw.jobID, "error", e)
	}
	if e := a.db.ClearVotingProcessMetadataUpdate(mw.processID); e != nil {
		log.Warnw("could not release metadata update claim", "processId", mw.processID.Hex(), "error", e)
	}
}
