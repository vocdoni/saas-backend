package api

import (
	"encoding/json"
	stderrors "errors"
	"fmt"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
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
		apicommon.HTTPWriteJSON(w, &apicommon.UpdateProcessCensusResponse{Added: 0})
		return
	}

	censusID := vp.CensusID.Hex()
	// before any write, so a refusal leaves the census exactly as it was
	if a.refuseBlockedVoters(w, []string{censusID}, req.MemberIDs) {
		return
	}

	emptied, err := a.db.RevokeMembersFromCensuses([]string{censusID}, req.MemberIDs)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	resp := &apicommon.UpdateProcessCensusResponse{Removed: uint32(len(req.MemberIDs))}
	if jobID := a.resizeEmptiedQuestions(vp.OrgAddress, emptied); jobID != "" {
		resp.JobID = jobID
		apicommon.HTTPWriteJSONStatus(w, http.StatusAccepted, resp)
		return
	}
	apicommon.HTTPWriteJSON(w, resp)
}

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
