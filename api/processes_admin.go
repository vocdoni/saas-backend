package api

import (
	"encoding/json"
	stderrors "errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.vocdoni.io/dvote/log"
)

// deleteVotingProcessHandler godoc
//
//	@Summary		Delete a voting process draft
//	@Description	Delete an unpublished voting process draft together with its inline census. A
//	@Description	published process has on-chain elections and cannot be deleted. Requires
//	@Description	Manager/Admin role of the organization that owns the process.
//	@Tags			processes
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string			true	"Process ID"
//	@Success		200			{string}	string			"OK"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		404			{object}	errors.Error	"Process not found"
//	@Failure		409			{object}	errors.Error	"Process already published"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/processes/{processId} [delete]
func (a *API) deleteVotingProcessHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	// loads the process + questions and gates on Manager/Admin of the owning org.
	vp, _, ok := a.authorizeStatusChange(w, r, oid)
	if !ok {
		return
	}
	// only a draft can be deleted; a published process lives on-chain and is immutable.
	if vp.Published {
		errors.ErrDuplicateConflict.Withf("process already published and not in draft mode").Write(w)
		return
	}
	if err := a.db.DeleteVotingProcess(oid); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	// best-effort: drop the draft's inline census so it is not orphaned.
	if !vp.CensusID.IsZero() {
		_ = a.db.DelCensus(vp.CensusID.Hex())
	}
	apicommon.HTTPWriteOK(w)
}

// votingProcessParticipantsHandler godoc
//
//	@Summary		List voted participants of a voting process
//	@Description	Manager/Admin lookup of organization members by a single field (email, phone,
//	@Description	memberNumber, nationalId), intersected with the process census, reporting each
//	@Description	matched member's per-question voted status. For `phone` pass the plaintext number;
//	@Description	it is hashed server-side. Requires Manager/Admin of the owning organization.
//	@Tags			processes
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string	true	"Process ID"
//	@Param			field		query		string	true	"Lookup field: email, phone, memberNumber or nationalId"
//	@Param			value		query		string	true	"Value to match for the given field"
//	@Success		200			{object}	apicommon.ProcessParticipantsResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		404			{object}	errors.Error	"Process not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/processes/{processId}/participants [get]
func (a *API) votingProcessParticipantsHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	vp, questions, ok := a.authorizeStatusChange(w, r, oid)
	if !ok {
		return
	}
	field := db.OrgMemberLookupField(r.URL.Query().Get("field"))
	if !field.IsValid() {
		errors.ErrMalformedBody.Withf("invalid field: must be one of email, phone, memberNumber, nationalId").Write(w)
		return
	}
	value := r.URL.Query().Get("value")
	if value == "" {
		errors.ErrMalformedBody.Withf("missing value").Write(w)
		return
	}
	// phone is stored hashed, so hash the plaintext before looking up.
	var lookupValue any = value
	if field == db.OrgMemberLookupFieldPhone {
		org, err := a.db.Organization(vp.OrgAddress)
		if err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		hashed, err := db.NewHashedPhone(value, org)
		if err != nil || hashed.IsEmpty() {
			errors.ErrMalformedBody.Withf("invalid phone").Write(w)
			return
		}
		lookupValue = hashed
	}

	resp := apicommon.ProcessParticipantsResponse{Participants: []apicommon.ProcessParticipantEntry{}}
	members, err := a.db.OrgMembersByField(vp.OrgAddress, field, lookupValue)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if len(members) == 0 {
		apicommon.HTTPWriteJSON(w, resp)
		return
	}
	memberIDs := make([]string, 0, len(members))
	membersByID := make(map[string]*db.OrgMember, len(members))
	for _, m := range members {
		id := m.ID.Hex()
		memberIDs = append(memberIDs, id)
		membersByID[id] = m
	}
	participants, err := a.db.CensusParticipantsByMemberIDs(vp.CensusID.Hex(), memberIDs)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if len(participants) == 0 {
		apicommon.HTTPWriteJSON(w, resp)
		return
	}
	participantIDs := make([]string, 0, len(participants))
	for _, p := range participants {
		participantIDs = append(participantIDs, p.ParticipantID)
	}
	// per-question voted status: each question is its own on-chain election, keyed by upstreamId.
	votedByQuestion := make(map[string]map[string]bool, len(questions))
	for i := range questions {
		q := &questions[i]
		if len(q.UpstreamID) == 0 {
			continue // question not yet on chain
		}
		voted, err := a.db.MembersWithUsedCSPProcess(q.UpstreamID, participantIDs)
		if err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		votedByQuestion[q.ID.Hex()] = voted
	}
	for _, p := range participants {
		m, exists := membersByID[p.ParticipantID]
		if !exists {
			continue
		}
		entry := apicommon.ProcessParticipantEntry{
			MemberID:     m.ID.Hex(),
			Name:         m.Name,
			Surname:      m.Surname,
			Email:        m.Email,
			MemberNumber: m.MemberNumber,
		}
		for i := range questions {
			q := &questions[i]
			if len(q.UpstreamID) == 0 {
				continue
			}
			entry.Questions = append(entry.Questions, apicommon.ProcessParticipantQuestionVote{
				QuestionID: q.ID.Hex(),
				UpstreamID: q.UpstreamID,
				HasVoted:   votedByQuestion[q.ID.Hex()][m.ID.Hex()],
			})
		}
		resp.Participants = append(resp.Participants, entry)
	}
	apicommon.HTTPWriteJSON(w, resp)
}

// updateVotingProcessCensusHandler godoc
//
//	@Summary		Add members to a published process's census
//	@Description	Add existing organization members to the census of an already-published voting process
//	@Description	(same behaviour as POST /census/{id}, resolving the census from the process) and raise
//	@Description	each affected on-chain election's maxCensusSize so the new members can vote. Members are
//	@Description	added synchronously; the maxCensusSize update runs as an async job (poll GET /jobs/{jobId}).
//	@Description	Questions with an eligibility subset keep their fixed size and are unaffected. Requires
//	@Description	Manager/Admin role and is subject to the plan's census quota.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string									true	"Process ID"
//	@Param			request		body		apicommon.AddCensusParticipantsRequest	true	"Member IDs to add"
//	@Success		200			{object}	apicommon.UpdateProcessCensusResponse	"Members added; no on-chain resize needed"
//	@Success		202			{object}	apicommon.UpdateProcessCensusResponse	"Members added; maxCensusSize update enqueued"
//	@Failure		400			{object}	errors.Error							"Invalid input data"
//	@Failure		401			{object}	errors.Error							"Unauthorized"
//	@Failure		404			{object}	errors.Error							"Process not found"
//	@Failure		409			{object}	errors.Error							"Process is not published"
//	@Failure		500			{object}	errors.Error							"Internal server error"
//	@Router			/processes/{processId}/census [put]
func (a *API) updateVotingProcessCensusHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	// loads the process + questions and gates on Manager/Admin of the owning org.
	vp, questions, ok := a.authorizeStatusChange(w, r, oid)
	if !ok {
		return
	}
	// only a published process can have its on-chain census extended; drafts use PUT /processes.
	if !vp.Published {
		errors.ErrDuplicateConflict.Withf("process is not published; edit the draft via PUT /processes/{processId}").Write(w)
		return
	}
	census, err := a.db.Census(vp.CensusID.Hex())
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	var req apicommon.AddCensusParticipantsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Withf("couldn't decode participant IDs").Write(w)
		return
	}
	if len(req.MemberIDs) == 0 {
		apicommon.HTTPWriteJSON(w, &apicommon.UpdateProcessCensusResponse{Added: 0})
		return
	}

	if err := a.subscriptions.OrgCanAddCensusParticipants(census.OrgAddress, census.ID.Hex(), len(req.MemberIDs)); err != nil {
		writeSubscriptionError(w, err)
		return
	}

	added, memberErrs, err := a.db.AddCensusParticipantsByMemberIDs(census.ID.Hex(), req.MemberIDs)
	switch {
	case err == nil:
	case stderrors.Is(err, db.ErrInvalidData), stderrors.Is(err, db.ErrUpdateWouldCreateDuplicates):
		errors.ErrInvalidData.WithErr(err).Write(w)
		return
	case stderrors.Is(err, db.ErrNotFound):
		errors.ErrCensusNotFound.Write(w)
		return
	default:
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// recount and persist the census size so the on-chain maxCensusSize we set below is correct.
	size, err := a.db.CountCensusParticipants(census.ID.Hex())
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	census.Size = size
	if _, err := a.db.SetCensus(census); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	jobID, ok := a.enqueueCensusSizeUpdate(w, vp, census, questions)
	if !ok {
		return
	}
	resp := &apicommon.UpdateProcessCensusResponse{JobID: jobID, Added: uint32(added), Errors: memberErrs}
	if jobID == "" {
		// no whole-census election to resize (subset-only questions): nothing async is pending, so 200.
		apicommon.HTTPWriteJSON(w, resp)
		return
	}
	apicommon.HTTPWriteJSONStatus(w, http.StatusAccepted, resp)
}

// updateVotingProcessQuestionCensusHandler godoc
//
//	@Summary		Set the members eligible to vote one question
//	@Description	Replace the list of organization members eligible to vote a single question. The body
//	@Description	carries the complete list, not a delta, and every id must already be a participant of the
//	@Description	process census — add them with PUT /processes/{processId}/census first.
//	@Description	While the process is a draft the list is applied as given, so members can be added,
//	@Description	removed or replaced. Once the process is published the list may only grow: it must still
//	@Description	contain every member it already had (409 otherwise), a question that was open to the whole
//	@Description	census cannot be narrowed to a subset, and a subset cannot be widened back to the whole
//	@Description	census. Sending the list a question already has is a no-op.
//	@Description	Growing a published question's list raises its on-chain election maxCensusSize as an
//	@Description	async job (poll GET /jobs/{jobId}); without that the new members would be signed by the
//	@Description	CSP only for the chain to reject their vote. Requires Manager/Admin role.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string									true	"Process ID"
//	@Param			questionId	path		string									true	"Question ID"
//	@Param			request		body		apicommon.UpdateQuestionCensusRequest	true	"Eligible member IDs"
//	@Success		200			{object}	apicommon.UpdateQuestionCensusResponse	"Applied; no on-chain resize needed"
//	@Success		202			{object}	apicommon.UpdateQuestionCensusResponse	"Applied; maxCensusSize update enqueued"
//	@Failure		400			{object}	errors.Error							"Invalid input data"
//	@Failure		401			{object}	errors.Error							"Unauthorized"
//	@Failure		404			{object}	errors.Error							"Process or question not found"
//	@Failure		409			{object}	errors.Error							"Would restrict a published question"
//	@Failure		500			{object}	errors.Error							"Internal server error"
//	@Router			/processes/{processId}/questions/{questionId}/census [put]
func (a *API) updateVotingProcessQuestionCensusHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	qid, err := primitive.ObjectIDFromHex(chi.URLParam(r, "questionId"))
	if err != nil {
		errors.ErrMalformedURLParam.Withf("invalid question ID").Write(w)
		return
	}
	var req apicommon.UpdateQuestionCensusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	// loads the process + questions and gates on Manager/Admin of the owning org.
	vp, questions, ok := a.authorizeStatusChange(w, r, oid)
	if !ok {
		return
	}
	var question *db.VotingProcessQuestion
	for i := range questions {
		if questions[i].ID == qid {
			question = &questions[i]
			break
		}
	}
	if question == nil {
		errors.ErrProcessNotFound.Withf("question not found").Write(w)
		return
	}
	census, err := a.db.Census(vp.CensusID.Hex())
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	// resolve and validate against the census: eligibility is a subset of the process census, so an
	// id that is not a participant is rejected rather than silently stored.
	eligible, err := a.resolveEligibleMemberIDs(
		&apicommon.EligibilitySpec{MemberIDs: req.MemberIDs}, census, vp.OrgAddress)
	if err != nil {
		// passes the resolver's own 400 through (e.g. "member X is not part of the process census")
		writeSubscriptionError(w, err)
		return
	}
	if vp.Published {
		if apiErr, valid := checkEligibilityGrows(question.EligibleMemberIDs, eligible); !valid {
			apiErr.Write(w)
			return
		}
	}

	added, removed := diffEligibility(question.EligibleMemberIDs, eligible)
	resp := &apicommon.UpdateQuestionCensusResponse{Eligible: len(eligible), Added: added, Removed: removed}
	if added == 0 && removed == 0 {
		// idempotent replay: nothing stored, nothing to resize on chain.
		apicommon.HTTPWriteJSON(w, resp)
		return
	}
	switch err := a.db.SetQuestionEligibleMemberIDs(qid, question.EligibleMemberIDs, eligible); {
	case err == nil:
	case stderrors.Is(err, db.ErrStaleWrite):
		errors.ErrDuplicateConflict.
			Withf("question eligibility changed concurrently; re-read the question and retry").Write(w)
		return
	case stderrors.Is(err, db.ErrNotFound):
		errors.ErrProcessNotFound.Withf("question not found").Write(w)
		return
	default:
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// a published question's election was sized for its old eligible count, so it has no room for
	// the members just added: raise its on-chain maxCensusSize to match.
	if len(question.UpstreamID) == 0 || added == 0 {
		apicommon.HTTPWriteJSON(w, resp)
		return
	}
	jobID, ok := a.enqueueSetProcessCensus(w, vp, census,
		[]censusSizeTarget{{upstreamID: question.UpstreamID, size: uint64(len(eligible))}})
	if !ok {
		return
	}
	resp.JobID = jobID
	apicommon.HTTPWriteJSONStatus(w, http.StatusAccepted, resp)
}

// checkEligibilityGrows enforces the append-only rule of a published question: the new eligible set
// must keep every member the stored one had. The two whole-census edges are not implied by that
// containment check and are rejected explicitly — an empty stored list means "everyone", so
// narrowing it to a subset restricts the electorate, and widening a subset back to everyone would
// need the election resized to the full census, which is a different operation.
// It reports the error to write back and false when the change is not allowed.
func checkEligibilityGrows(stored, requested []string) (errors.Error, bool) {
	switch {
	case len(stored) == 0 && len(requested) > 0:
		return errors.ErrQuestionEligibilityRestricted.Withf(
			"question is open to the whole census; restricting it to %d members is not allowed once published",
			len(requested)), false
	case len(stored) > 0 && len(requested) == 0:
		return errors.ErrQuestionEligibilityRestricted.With(
			"opening a published question to the whole census is not allowed"), false
	}
	have := make(map[string]bool, len(requested))
	for _, id := range requested {
		have[id] = true
	}
	missing := make([]string, 0, len(stored))
	for _, id := range stored {
		if !have[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return errors.ErrQuestionEligibilityRestricted.Withf(
			"would drop %d already-eligible member(s): %s", len(missing), strings.Join(missing, ", ")), false
	}
	return errors.Error{}, true
}

// diffEligibility counts the members added and removed between the stored and the new eligible list.
func diffEligibility(stored, updated []string) (added, removed int) {
	was := make(map[string]bool, len(stored))
	for _, id := range stored {
		was[id] = true
	}
	now := make(map[string]bool, len(updated))
	for _, id := range updated {
		now[id] = true
		if !was[id] {
			added++
		}
	}
	for _, id := range stored {
		if !now[id] {
			removed++
		}
	}
	return added, removed
}

// enqueueCensusSizeUpdate submits a SET_PROCESS_CENSUS tx per published whole-census question to raise
// its on-chain maxCensusSize to the census's new size. A question with an eligibility subset is sized
// by that subset, not by the census, so growing the census leaves it alone; it is resized instead when
// its own eligible list grows, through PUT /processes/{processId}/questions/{questionId}/census.
// Returns the job id (empty when nothing on-chain needs updating) and false on failure (after writing
// the error response).
func (a *API) enqueueCensusSizeUpdate(
	w http.ResponseWriter, vp *db.VotingProcess, census *db.Census, questions []db.VotingProcessQuestion,
) (string, bool) {
	targets := make([]censusSizeTarget, 0, len(questions))
	for i := range questions {
		if len(questions[i].UpstreamID) > 0 && len(questions[i].EligibleMemberIDs) == 0 {
			targets = append(targets, censusSizeTarget{upstreamID: questions[i].UpstreamID, size: uint64(census.Size)})
		}
	}
	return a.enqueueSetProcessCensus(w, vp, census, targets)
}

// censusSizeTarget is one published election whose on-chain maxCensusSize must be raised, and the
// size to raise it to. A whole-census question takes the census size; a question restricted to an
// eligibility subset takes the size of that subset.
type censusSizeTarget struct {
	upstreamID internal.HexBytes
	size       uint64
}

// enqueueSetProcessCensus submits a SET_PROCESS_CENSUS tx per target, on a background worker, to
// raise each election's on-chain maxCensusSize while keeping its census root and URI. Returns the
// job id (empty when there is nothing to resize) and false on failure (after writing the error
// response).
func (a *API) enqueueSetProcessCensus(
	w http.ResponseWriter, vp *db.VotingProcess, census *db.Census, targets []censusSizeTarget,
) (string, bool) {
	if len(targets) == 0 {
		return "", true // nothing on chain to resize
	}
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return "", false
	}
	orgSigner, err := account.OrganizationSigner(a.secret, org.Creator, org.Nonce)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not restore organization signer: %v", err).Write(w)
		return "", false
	}
	jobID, err := apicommon.NewJobID()
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return "", false
	}
	if err := a.db.CreateTxJob(jobID, db.JobTypeSetProcessCensus, org.Address); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return "", false
	}
	root := census.Published.Root
	uri := census.Published.URI
	orgLock := a.orgTxLocks.lock(org.Address)
	if !a.enqueueTx(txTask{jobID: jobID, run: func() (*db.JobResult, error) {
		defer orgLock.Unlock()
		for _, target := range targets {
			tx, err := a.account.BuildSetProcessCensusTx(
				orgSigner.Address(), target.upstreamID, root, uri, target.size)
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
		}
		return &db.JobResult{Status: string(db.JobStatusCompleted)}, nil
	}}) {
		orgLock.Unlock()
		if e := a.db.SetJobStatus(jobID, db.JobStatusFailed, nil, "tx queue full"); e != nil {
			log.Warnw("could not mark job failed after full queue", "error", e)
		}
		errors.ErrTxQueueFull.Write(w)
		return "", false
	}
	return jobID, true
}
