package api

import (
	"encoding/json"
	stderrors "errors"
	"fmt"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
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
	// deleting mid-publish would orphan on chain whatever the worker has already mined.
	if refusePublishInProgress(w, vp) {
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

	// only whole-census questions grow when participants are added; one that names an eligibility
	// subset is unaffected by who else joined the census.
	targets := make([]censusSizeTarget, 0, len(questions))
	for i := range questions {
		if len(questions[i].EligibleMemberIDs) == 0 {
			targets = append(targets, censusSizeTarget{
				question: questions[i], census: census, size: uint64(census.Size),
			})
		}
	}
	jobID, err := a.enqueueSetProcessCensus(vp.OrgAddress, targets)
	if err != nil {
		writeSubscriptionError(w, err)
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
//	@Summary		Set a question's eligibility list
//	@Description	Replace the set of members eligible to vote one question of a voting process, for a
//	@Description	draft or a published process (closes #611). Requires Manager/Admin role.
//	@Description
//	@Description	The body is the **complete desired list, not a delta**, so the request is idempotent:
//	@Description	resend the whole list to change it. Every id must already be a participant of the
//	@Description	process census. Input order is preserved and duplicates are dropped, so a client can
//	@Description	diff what it reads back against its next request.
//	@Description
//	@Description	**An empty list does not mean "nobody": it is the encoding for "no restriction" and
//	@Description	opens the question to every member of the census.** A response of `eligible: 0`
//	@Description	therefore means the question is open to everyone.
//	@Description
//	@Description	Because reopening a restricted question can multiply its electorate while its election
//	@Description	was sized on chain for the old subset, a maxCensusSize increase is enqueued as an async
//	@Description	job whenever the question needs more room than it was published with (202; poll GET
//	@Description	/jobs/{jobId}).
//	@Description
//	@Description	Removing a member the CSP has already signed for, while a question of this process is
//	@Description	still READY or PAUSED, is refused with 409 and the offending ids in
//	@Description	`data.votedMemberIds`.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `voting:write`).
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string									true	"Process ID"
//	@Param			questionId	path		string									true	"Question ID"
//	@Param			request		body		apicommon.UpdateQuestionCensusRequest	true	"Complete desired eligibility list"
//	@Success		200			{object}	apicommon.UpdateQuestionCensusResponse	"Eligibility updated; no on-chain resize needed"
//	@Success		202			{object}	apicommon.UpdateQuestionCensusResponse	"Eligibility updated; maxCensusSize update enqueued"
//	@Failure		400			{object}	errors.Error							"Invalid input data, or a member is not part of the census"
//	@Failure		401			{object}	errors.Error							"Unauthorized"
//	@Failure		404			{object}	errors.Error							"Process or question not found"
//	@Failure		409			{object}	errors.Error							"Publish in progress, member already signed for, or list changed"
//	@Failure		500			{object}	errors.Error							"Internal server error"
//	@Router			/processes/{processId}/questions/{questionId}/census [put]
func (a *API) updateVotingProcessQuestionCensusHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	// loads the process + questions and gates on Manager/Admin of the owning org.
	vp, questions, ok := a.authorizeStatusChange(w, r, oid)
	if !ok {
		return
	}
	// while a publish worker holds the process, its questions all look like drafts: an eligibility
	// write would take the draft path, skip the resize, and the worker would then mint an election
	// sized from its older snapshot.
	if refusePublishInProgress(w, vp) {
		return
	}
	question, ok := questionOfProcess(w, questions, chi.URLParam(r, "questionId"))
	if !ok {
		return
	}

	var req apicommon.UpdateQuestionCensusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Withf("couldn't decode member IDs").Write(w)
		return
	}
	census, err := a.db.Census(vp.CensusID.Hex())
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	eligible, err := a.validateCensusParticipants(census.ID.Hex(), req.MemberIDs)
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}

	previous := question.EligibleMemberIDs
	added, removed := diffMemberIDs(previous, eligible)
	// Members losing eligibility lose their ability to vote this question, so the refusal has to
	// happen before the write. It is keyed off who has been signed for, not off `removed`: a
	// question open to the whole census names nobody, so restricting it drops voters that no diff
	// against the stored list can report.
	if a.refuseVotersLosingEligibility(w, question, eligible) {
		return
	}
	if len(added) == 0 && len(removed) == 0 && len(question.UpstreamID) == 0 {
		// a draft that already says this: nothing to store, and no election to size
		apicommon.HTTPWriteJSON(w, &apicommon.UpdateQuestionCensusResponse{Eligible: uint32(len(eligible))})
		return
	}

	won, err := a.db.SetQuestionEligibleMemberIDs(question.ID, previous, eligible)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if !won {
		errors.ErrDuplicateConflict.Withf("the eligibility list changed while this request was being served").Write(w)
		return
	}

	resp := &apicommon.UpdateQuestionCensusResponse{
		Eligible: uint32(len(eligible)),
		Added:    uint32(len(added)),
		Removed:  uint32(len(removed)),
	}

	// What the election needs, with an empty list meaning the whole census.
	//
	// This is always handed to enqueueSetProcessCensus rather than pre-filtered here against what
	// the stored list implies the election was sized for. That proxy is wrong in exactly the case
	// that matters: the list is committed above before the resize is enqueued, so a queue-full 503
	// leaves the two disagreeing — and the retry, which is the whole recovery since nothing sweeps
	// failed SetProcessCensus jobs, diffs to nothing and would conclude the election already has
	// the room it never got. enqueueSetProcessCensus reads the election itself and enqueues only a
	// genuine increase, so it is the one place that can tell a completed request from an
	// interrupted one. The cost is a chain read on an idempotent replay.
	//
	// Note this is not keyed off "were members added" either: reopening a restricted question adds
	// nobody by name yet can multiply the electorate, and account.ComputeMaxCensusSize stamped that
	// election at exactly the old subset size — zero headroom. The CSP would sign and the chain
	// would reject.
	needed := uint64(len(eligible))
	if len(eligible) == 0 {
		needed = uint64(census.Size)
	}
	question.EligibleMemberIDs = eligible
	jobID, err := a.enqueueSetProcessCensus(vp.OrgAddress, []censusSizeTarget{
		{question: *question, census: census, size: needed},
	})
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	if jobID == "" {
		apicommon.HTTPWriteJSON(w, resp)
		return
	}
	resp.JobID = jobID
	apicommon.HTTPWriteJSONStatus(w, http.StatusAccepted, resp)
}

// questionOfProcess resolves a question id against the process's own questions, so a question of
// another process is a 404 rather than a cross-process write.
func questionOfProcess(
	w http.ResponseWriter, questions []db.VotingProcessQuestion, questionID string,
) (*db.VotingProcessQuestion, bool) {
	qid, err := primitive.ObjectIDFromHex(questionID)
	if err != nil {
		errors.ErrMalformedURLParam.Withf("invalid question ID").Write(w)
		return nil, false
	}
	for i := range questions {
		if questions[i].ID == qid {
			return &questions[i], true
		}
	}
	errors.ErrProcessNotFound.Withf("question not found").Write(w)
	return nil, false
}

// diffMemberIDs reports which ids next gains over previous, and which it drops.
func diffMemberIDs(previous, next []string) (added, removed []string) {
	prev := make(map[string]bool, len(previous))
	for _, id := range previous {
		prev[id] = true
	}
	cur := make(map[string]bool, len(next))
	for _, id := range next {
		cur[id] = true
		if !prev[id] {
			added = append(added, id)
		}
	}
	for _, id := range previous {
		if !cur[id] {
			removed = append(removed, id)
		}
	}
	return added, removed
}

// removeVotingProcessCensusHandler godoc
//
//	@Summary		Remove members from a published process census
//	@Description	Remove members from a published voting process's census and from every question
//	@Description	eligibility list built on it, so the CSP stops signing for them. Refused with 409 for
//	@Description	any member the CSP has already signed for while a question of the process is still
//	@Description	READY or PAUSED; once voting closes on those questions the removal succeeds. The
//	@Description	offending ids come back in `data.votedMemberIds`.
//	@Description
//	@Description	Pruning a question's eligibility list to empty opens it to the whole census, so a
//	@Description	maxCensusSize increase may be enqueued as an async job (poll GET /jobs/{jobId}).
//	@Description	Requires Manager/Admin role.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `voting:write`).
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string									true	"Process ID"
//	@Param			request		body		apicommon.AddCensusParticipantsRequest	true	"Member IDs to remove"
//	@Success		200			{object}	apicommon.UpdateProcessCensusResponse	"Members removed"
//	@Success		202			{object}	apicommon.UpdateProcessCensusResponse	"Members removed; maxCensusSize update enqueued"
//	@Failure		400			{object}	errors.Error							"Invalid input data"
//	@Failure		401			{object}	errors.Error							"Unauthorized"
//	@Failure		404			{object}	errors.Error							"Process not found"
//	@Failure		409			{object}	errors.Error							"Process is not published, or a member has already been signed for"
//	@Failure		500			{object}	errors.Error							"Internal server error"
//	@Router			/processes/{processId}/census [delete]
func (a *API) removeVotingProcessCensusHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	// loads the process + questions and gates on Manager/Admin of the owning org.
	vp, _, ok := a.authorizeStatusChange(w, r, oid)
	if !ok {
		return
	}
	if !vp.Published {
		errors.ErrDuplicateConflict.Withf(
			"process is not published; edit the draft via PUT /processes/{processId}",
		).Write(w)
		return
	}

	var req apicommon.AddCensusParticipantsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Withf("couldn't decode participant IDs").Write(w)
		return
	}
	if len(req.MemberIDs) == 0 {
		apicommon.HTTPWriteJSON(w, &apicommon.UpdateProcessCensusResponse{Removed: 0})
		return
	}
	if len(req.MemberIDs) > maxCensusRemoval {
		errors.ErrInvalidData.Withf(
			"too many member ids: %d, the maximum per request is %d", len(req.MemberIDs), maxCensusRemoval,
		).Write(w)
		return
	}

	// scoped before the guard and the write, exactly as deleteOrganizationMembersHandler does. The
	// cascade derives the censuses to touch from the ids themselves and deletes CSP sessions by
	// userid alone, so an id that reaches it unscoped revokes a member of another organization —
	// being a Manager of *this* process's org is not authority over an id this org never owned.
	// Unknown and foreign ids are dropped rather than rejected, so `removed` stays the count of
	// rows actually deleted.
	targetIDs, err := a.db.FilterOrgMemberIDs(vp.OrgAddress, req.MemberIDs)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not resolve org members: %v", err).Write(w)
		return
	}
	if len(targetIDs) == 0 {
		apicommon.HTTPWriteJSON(w, &apicommon.UpdateProcessCensusResponse{Removed: 0})
		return
	}

	censusID := vp.CensusID.Hex()
	// before any write, so a refusal leaves the census exactly as it was
	if a.refuseBlockedVoters(w, []string{censusID}, targetIDs) {
		return
	}

	removed, emptied, err := a.db.RevokeMembersFromCensuses([]string{censusID}, targetIDs)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// the participant rows actually deleted, not the ids submitted: an id naming nobody, or the
	// same id twice, must not be reported as a removal
	resp := &apicommon.UpdateProcessCensusResponse{Removed: uint32(removed)}
	if jobID := a.resizeEmptiedQuestions(vp.OrgAddress, emptied); jobID != "" {
		resp.JobID = jobID
		apicommon.HTTPWriteJSONStatus(w, http.StatusAccepted, resp)
		return
	}
	apicommon.HTTPWriteJSON(w, resp)
}

// maxCensusRemoval bounds one DELETE /processes/{processId}/census body. Every id travels as a
// single $in filter through the guard and the three revocation writes, so an unbounded list is an
// unbounded query; a caller with more to remove pages through.
const maxCensusRemoval = 1000

// censusSizeTarget is one published question whose on-chain election may need its maxCensusSize
// raised, with the census the new size is read from. A single group census can back several
// processes, so one request can produce targets spread across them.
type censusSizeTarget struct {
	question db.VotingProcessQuestion
	census   *db.Census
	// size is what the election has to hold from now on: the eligibility subset when the question
	// names one, otherwise the whole census.
	size uint64
}

// electionMaxCensusSize reads a published question's current on-chain maxCensusSize.
func (a *API) electionMaxCensusSize(q *db.VotingProcessQuestion) (uint64, error) {
	election, err := a.account.Election(q.UpstreamID)
	if err != nil {
		return 0, err
	}
	if election.Census == nil {
		return 0, fmt.Errorf("election %s carries no census", q.UpstreamID.String())
	}
	return election.Census.MaxCensusSize, nil
}

// enqueueSetProcessCensus submits one SET_PROCESS_CENSUS tx per target, as a single background job,
// to raise those elections' maxCensusSize. It returns an empty job id when nothing needs raising.
//
// It returns an error rather than writing a response, so one request can drive several processes —
// a group census can back more than one. Callers map it with writeSubscriptionError, which
// preserves ErrTxQueueFull's 503.
//
// It never enqueues a shrink. The chain accepts an increase only, and a census can now lose members,
// so blindly submitting the census size would eventually fail the job; each election is read first
// and only a genuine increase is sent.
//
// A failed election read does not fail the request. By the time this runs, the participant or
// eligibility write it follows has already committed, so reporting failure would report it for work
// that landed — the tx goes out anyway and a chain refusal surfaces on the job with its real reason.
func (a *API) enqueueSetProcessCensus(orgAddress common.Address, targets []censusSizeTarget) (string, error) {
	growing := make([]censusSizeTarget, 0, len(targets))
	for _, t := range targets {
		if len(t.question.UpstreamID) == 0 || t.size == 0 {
			continue // a draft has no election yet, and there is nothing to size against
		}
		current, err := a.electionMaxCensusSize(&t.question)
		switch {
		case err != nil:
			log.Warnw("could not read election size, enqueueing the resize anyway",
				"question", t.question.ID.Hex(),
				"upstreamId", t.question.UpstreamID.String(), "error", err)
		case t.size <= current:
			continue // the election already has the room
		default:
			// a genuine increase
		}
		growing = append(growing, t)
	}
	if len(growing) == 0 {
		return "", nil
	}

	org, err := a.db.Organization(orgAddress)
	if err != nil {
		return "", fmt.Errorf("could not load organization: %w", err)
	}
	orgSigner, err := account.OrganizationSigner(a.secret, org.Creator, org.Nonce)
	if err != nil {
		return "", fmt.Errorf("could not restore organization signer: %w", err)
	}
	jobID, err := apicommon.NewJobID()
	if err != nil {
		return "", fmt.Errorf("could not create job id: %w", err)
	}
	if err := a.db.CreateTxJob(jobID, db.JobTypeSetProcessCensus, org.Address); err != nil {
		return "", fmt.Errorf("could not create tx job: %w", err)
	}

	orgLock := a.orgTxLocks.lock(org.Address)
	if !a.enqueueTx(txTask{jobID: jobID, run: func() (*db.JobResult, error) {
		defer orgLock.Unlock()
		for i := range growing {
			t := &growing[i]
			tx, err := a.account.BuildSetProcessCensusTx(
				orgSigner.Address(), t.question.UpstreamID,
				t.census.Published.Root, t.census.Published.URI, t.size,
			)
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
		return "", errors.ErrTxQueueFull
	}
	return jobID, nil
}
