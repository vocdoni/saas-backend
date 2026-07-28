package api

import (
	"encoding/json"
	stderrors "errors"
	"net/http"

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

// removeVotingProcessCensusHandler godoc
//
//	@Summary		Remove members from a published process's census
//	@Description	Take organization members off the census of an already-published voting process, so
//	@Description	they can no longer authenticate or be signed for by the CSP. Their participant rows
//	@Description	are deleted, they are pruned from every question's eligibility subset, and their CSP
//	@Description	sessions are invalidated, so a token minted while they were still in the census cannot
//	@Description	be spent afterwards.
//	@Description	A member who has already voted any of the process's questions cannot be removed: the
//	@Description	request is refused with 409 and the offending ids in the error's data.votedMemberIds.
//	@Description	Their ballot is already on chain and removing them would only hide that, not undo it.
//	@Description	Nothing is sent on chain — an election's maxCensusSize can only grow, and the leftover
//	@Description	headroom is harmless because the CSP, not that number, decides who may vote. Note that a
//	@Description	CSP signature already handed out stays valid until the election closes; removal closes
//	@Description	the door on new signatures, it cannot recall one. Requires Manager/Admin role.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string									true	"Process ID"
//	@Param			request		body		apicommon.AddCensusParticipantsRequest	true	"Member IDs to remove"
//	@Success		200			{object}	apicommon.RemoveProcessCensusResponse	"Members removed"
//	@Failure		400			{object}	errors.Error							"Invalid input data"
//	@Failure		401			{object}	errors.Error							"Unauthorized"
//	@Failure		404			{object}	errors.Error							"Process not found"
//	@Failure		409			{object}	errors.Error							"Process is not published, or a member has already voted"
//	@Failure		500			{object}	errors.Error							"Internal server error"
//	@Router			/processes/{processId}/census [delete]
func (a *API) removeVotingProcessCensusHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	// loads the process + questions and gates on Manager/Admin of the owning org.
	vp, questions, ok := a.authorizeStatusChange(w, r, oid)
	if !ok {
		return
	}
	// a draft's census is rebuilt wholesale by PUT /processes/{processId}; this endpoint exists for
	// the published case, where that is not possible.
	if !vp.Published {
		errors.ErrDuplicateConflict.
			Withf("process is not published; edit the draft via PUT /processes/{processId}").Write(w)
		return
	}
	var req apicommon.AddCensusParticipantsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Withf("couldn't decode participant IDs").Write(w)
		return
	}
	if len(req.MemberIDs) == 0 {
		apicommon.HTTPWriteJSON(w, &apicommon.RemoveProcessCensusResponse{Removed: 0})
		return
	}
	// member ids reach code that decodes them as hex, so reject a malformed one here with a 400
	// rather than let it surface as an opaque failure deeper down.
	for _, id := range req.MemberIDs {
		if _, err := primitive.ObjectIDFromHex(id); err != nil {
			errors.ErrInvalidData.Withf("invalid member id %q", id).Write(w)
			return
		}
	}
	// a ballot already cast cannot be taken back, so refuse rather than pretend the removal undid it
	voted, apiErr := a.votedAmong(questions, req.MemberIDs)
	if apiErr != nil {
		apiErr.Write(w)
		return
	}
	if len(voted) > 0 {
		errors.ErrCensusMemberAlreadyVoted.
			Withf("%d of the %d members to remove already voted", len(voted), len(req.MemberIDs)).
			WithData(map[string]any{"votedMemberIds": voted}).Write(w)
		return
	}

	removed, err := a.db.RemoveCensusParticipantsByMemberIDs(vp.CensusID.Hex(), req.MemberIDs)
	if err != nil {
		if stderrors.Is(err, db.ErrNotFound) {
			errors.ErrCensusNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	apicommon.HTTPWriteJSON(w, &apicommon.RemoveProcessCensusResponse{Removed: removed})
}

// votedAmong returns which of the given members have already voted any of the process's on-chain
// questions, sorted so the response is stable. It reports the API error to write back on failure.
func (a *API) votedAmong(questions []db.VotingProcessQuestion, memberIDs []string) ([]string, *errors.Error) {
	seen := make(map[string]bool)
	for i := range questions {
		if len(questions[i].UpstreamID) == 0 {
			continue // not on chain, so nothing can have been voted
		}
		votedInQuestion, err := a.db.MembersWithUsedCSPProcess(questions[i].UpstreamID, memberIDs)
		if err != nil {
			e := errors.ErrGenericInternalServerError.WithErr(err)
			return nil, &e
		}
		for id := range votedInQuestion {
			seen[id] = true
		}
	}
	voted := make([]string, 0, len(seen))
	for _, id := range memberIDs {
		if seen[id] {
			voted = append(voted, id)
		}
	}
	return voted, nil
}

// enqueueCensusSizeUpdate submits a SET_PROCESS_CENSUS tx per published whole-census question to raise
// its on-chain maxCensusSize to the census's new size. Questions with an eligibility subset keep their
// fixed size and are skipped. Returns the job id (empty when nothing on-chain needs updating) and false
// on failure (after writing the error response).
func (a *API) enqueueCensusSizeUpdate(
	w http.ResponseWriter, vp *db.VotingProcess, census *db.Census, questions []db.VotingProcessQuestion,
) (string, bool) {
	size := uint64(census.Size)
	published := make([]db.VotingProcessQuestion, 0, len(questions))
	for i := range questions {
		if len(questions[i].UpstreamID) == 0 || len(questions[i].EligibleMemberIDs) > 0 {
			continue
		}
		// the chain only accepts a maxCensusSize increase, and the census can now shrink (a removed
		// member, a group edit), so compare against what the election actually carries instead of
		// submitting census.Size blindly and having the tx rejected.
		elec, err := a.account.Election(questions[i].UpstreamID)
		if err != nil {
			errors.ErrVochainRequestFailed.WithErr(err).Write(w)
			return "", false
		}
		if elec.Census != nil && size <= elec.Census.MaxCensusSize {
			continue
		}
		published = append(published, questions[i])
	}
	if len(published) == 0 {
		return "", true // no whole-census election on chain needs a bigger size
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
		for i := range published {
			tx, err := a.account.BuildSetProcessCensusTx(orgSigner.Address(), published[i].UpstreamID, root, uri, size)
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
