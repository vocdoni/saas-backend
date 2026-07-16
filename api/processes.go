package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// maxQuestionsPerProcess bounds the number of questions of a voting process (the node
// batch endpoint caps a batch at 100 transactions).
const maxQuestionsPerProcess = 100

// parseProcessDates parses the optional RFC3339 start/end dates of a create/update request.
func parseProcessDates(req *apicommon.CreateVotingProcessRequest) (start, end time.Time, err error) {
	if req.StartDate != "" {
		if start, err = time.Parse(time.RFC3339, req.StartDate); err != nil {
			return start, end, fmt.Errorf("invalid startDate: %w", err)
		}
	}
	if req.EndDate != "" {
		if end, err = time.Parse(time.RFC3339, req.EndDate); err != nil {
			return start, end, fmt.Errorf("invalid endDate: %w", err)
		}
	}
	return start, end, nil
}

// createVotingProcessHandler godoc
//
//	@Summary		Create a voting process draft
//	@Description	Create a multi-question voting process draft. Requires Manager/Admin role of the org
//	@Description	(or a scoped API key with `voting:write`). Creates the inline census unpublished.
//	@Description	Each question must define either a named `type` (with `typeSetup` for multichoice)
//	@Description	or a raw `ballotProtocol` override; if both are given the `ballotProtocol` wins.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.CreateVotingProcessRequest	true	"Voting process"
//	@Success		200		{object}	apicommon.CreateVotingProcessResponse
//	@Failure		400		{object}	errors.Error
//	@Failure		401		{object}	errors.Error
//	@Failure		403		{object}	errors.Error
//	@Router			/processes [post]
func (a *API) createVotingProcessHandler(w http.ResponseWriter, r *http.Request) {
	req := &apicommon.CreateVotingProcessRequest{}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// orgAddress is internal.HexBytes over the API (bare-hex JSON, like upstreamId); unlike
	// common.Address it doesn't enforce a 20-byte length on decode, so validate it here. The
	// zero address is treated as missing (it can never own an organization).
	orgAddr := common.BytesToAddress(req.OrgAddress)
	if len(req.OrgAddress) != common.AddressLength || orgAddr == (common.Address{}) {
		errors.ErrMalformedBody.Withf("missing or invalid org address").Write(w)
		return
	}
	if !user.HasRoleFor(orgAddr, db.ManagerRole) && !user.HasRoleFor(orgAddr, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of the organization").Write(w)
		return
	}
	if len(req.Questions) == 0 || len(req.Questions) > maxQuestionsPerProcess {
		errors.ErrMalformedBody.Withf("questions must be between 1 and %d", maxQuestionsPerProcess).Write(w)
		return
	}
	if err := a.subscriptions.OrgCanCreateVotingProcessDraft(orgAddr); err != nil {
		writeSubscriptionError(w, err)
		return
	}
	start, end, err := parseProcessDates(req)
	if err != nil {
		errors.ErrMalformedBody.WithErr(err).Write(w)
		return
	}
	census, err := a.resolveOrCreateDefaultCensus(req.Census, orgAddr)
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	// validate + build the questions (incl. eligibility against the census) before any process
	// write, so a bad request rolls the census back and never creates a half-written draft.
	built, err := a.buildQuestions(orgAddr, req.Questions, census)
	if err != nil {
		_ = a.db.DelCensus(census.ID.Hex())
		writeSubscriptionError(w, err)
		return
	}

	vp := &db.VotingProcess{
		OrgAddress:  orgAddr,
		Published:   false,
		Title:       req.Title,
		Description: req.Description,
		Header:      req.Header,
		StreamURI:   req.StreamURI,
		StartDate:   start,
		EndDate:     end,
		CensusID:    census.ID,
	}
	vpID, err := a.db.SetVotingProcess(vp)
	if err != nil {
		_ = a.db.DelCensus(census.ID.Hex())
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if err := a.writeQuestions(vp, built); err != nil {
		// roll back the just-created draft and its census so a failed create leaves nothing
		// behind (an orphaned draft would still count against the org's MaxDrafts quota).
		_ = a.db.DeleteVotingProcess(vpID)
		_ = a.db.DelCensus(census.ID.Hex())
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	apicommon.HTTPWriteJSON(w, apicommon.CreateVotingProcessResponse{ProcessID: vpID.Hex()})
}

// buildQuestions resolves and validates the questions of a voting process in memory — including
// each question's eligibility subset against the census — WITHOUT writing anything, so a caller
// can validate before mutating the draft. ProcessID is assigned later by writeQuestions.
func (a *API) buildQuestions(
	orgAddress common.Address, questions []apicommon.VotingProcessQuestionRequest, census *db.Census,
) ([]*db.VotingProcessQuestion, error) {
	built := make([]*db.VotingProcessQuestion, 0, len(questions))
	for i, q := range questions {
		// ballot shape: a question must define EITHER a named type OR a raw BallotProtocol
		// override; if both are set the BallotProtocol wins (VoteTypeFromQuestion uses it). For a
		// named type, typeSetup is required for every type except singlechoice (which ignores it
		// on chain); a multichoice maps MaxChoices onto MaxTotalCost so it must be bounded.
		if q.BallotProtocol == nil {
			switch q.Type {
			case "":
				return nil, errors.ErrInvalidData.Withf("question %d: a type or a ballotProtocol is required", i)
			case db.VotingTypeSingleChoice:
				// singlechoice ignores typeSetup
			case db.VotingTypeMultiChoice:
				if q.TypeSetup.MaxChoices < 1 || q.TypeSetup.MaxChoices > uint32(len(q.Choices)) {
					return nil, errors.ErrInvalidData.Withf(
						"question %d: maxChoices must be between 1 and the number of choices (%d)", i, len(q.Choices))
				}
				if q.TypeSetup.MinChoices > q.TypeSetup.MaxChoices {
					return nil, errors.ErrInvalidData.Withf("question %d: minChoices cannot exceed maxChoices", i)
				}
			default:
				return nil, errors.ErrInvalidData.Withf("question %d: unsupported type %q", i, q.Type)
			}
		}
		eligible, err := a.resolveEligibleMemberIDs(q.Eligibility, census, orgAddress)
		if err != nil {
			return nil, err
		}
		built = append(built, &db.VotingProcessQuestion{
			OrgAddress:        orgAddress,
			Order:             i,
			Title:             q.Title,
			Description:       q.Description,
			Choices:           q.Choices,
			Type:              q.Type,
			TypeSetup:         q.TypeSetup,
			BallotProtocol:    q.BallotProtocol,
			SecretUntilTheEnd: q.SecretUntilTheEnd,
			EligibleMemberIDs: eligible,
			Metadata:          q.Metadata,
		})
	}
	return built, nil
}

// writeQuestions replaces the process's stored questions with a pre-built (already validated)
// set and updates its ordered QuestionIDs. Existing questions are removed first so a draft
// update replaces them. Callers run buildQuestions first, so this only fails on infra errors.
func (a *API) writeQuestions(vp *db.VotingProcess, built []*db.VotingProcessQuestion) error {
	existing, err := a.db.QuestionsByProcess(vp.ID)
	if err != nil {
		return fmt.Errorf("failed to load existing questions: %w", err)
	}
	for i := range existing {
		if err := a.db.DeleteQuestion(existing[i].ID); err != nil {
			return fmt.Errorf("failed to remove existing question: %w", err)
		}
	}
	questionIDs := make([]primitive.ObjectID, 0, len(built))
	for _, question := range built {
		question.ProcessID = vp.ID
		qID, err := a.db.SetQuestion(question)
		if err != nil {
			return fmt.Errorf("failed to store question: %w", err)
		}
		questionIDs = append(questionIDs, qID)
	}
	vp.QuestionIDs = questionIDs
	if _, err := a.db.SetVotingProcess(vp); err != nil {
		return fmt.Errorf("failed to update process questions: %w", err)
	}
	return nil
}

// updateVotingProcessHandler godoc
//
//	@Summary		Update a voting process draft
//	@Description	Update a voting process while it is still a draft (not published). 409 if already published.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string									true	"Process ID"
//	@Param			request		body		apicommon.CreateVotingProcessRequest	true	"Voting process"
//	@Success		200			{string}	string									"OK"
//	@Failure		400			{object}	errors.Error
//	@Failure		401			{object}	errors.Error
//	@Failure		404			{object}	errors.Error
//	@Failure		409			{object}	errors.Error
//	@Router			/processes/{processId} [put]
func (a *API) updateVotingProcessHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	req := &apicommon.CreateVotingProcessRequest{}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	vp, ok := a.loadVotingProcess(w, oid)
	if !ok {
		return
	}
	if vp.Published {
		errors.ErrDuplicateConflict.Withf("process already published and not in draft mode").Write(w)
		return
	}
	if !user.HasRoleFor(vp.OrgAddress, db.ManagerRole) && !user.HasRoleFor(vp.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of the organization").Write(w)
		return
	}
	if len(req.Questions) == 0 || len(req.Questions) > maxQuestionsPerProcess {
		errors.ErrMalformedBody.Withf("questions must be between 1 and %d", maxQuestionsPerProcess).Write(w)
		return
	}
	start, end, err := parseProcessDates(req)
	if err != nil {
		errors.ErrMalformedBody.WithErr(err).Write(w)
		return
	}
	// a draft update re-resolves the census into a fresh unpublished db.Census; the previous
	// one is reaped only after the update fully succeeds, so a failed edit neither orphans the
	// new census nor destroys the old draft.
	oldCensusID := vp.CensusID
	census, err := a.resolveOrCreateDefaultCensus(req.Census, vp.OrgAddress)
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	// validate + build the new questions against the new census before any destructive write.
	built, err := a.buildQuestions(vp.OrgAddress, req.Questions, census)
	if err != nil {
		_ = a.db.DelCensus(census.ID.Hex())
		writeSubscriptionError(w, err)
		return
	}
	vp.Title, vp.Description, vp.Header, vp.StreamURI = req.Title, req.Description, req.Header, req.StreamURI
	vp.StartDate, vp.EndDate, vp.CensusID = start, end, census.ID
	if _, err := a.db.SetVotingProcess(vp); err != nil {
		_ = a.db.DelCensus(census.ID.Hex())
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if err := a.writeQuestions(vp, built); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	// success: reap the previous census (and its participants) so edits don't accumulate orphans.
	if oldCensusID != census.ID {
		_ = a.db.DelCensus(oldCensusID.Hex())
	}
	apicommon.HTTPWriteOK(w)
}

// votingProcessInfoHandler godoc
//
//	@Summary		Get a voting process
//	@Description	Read a voting process with its fully hydrated questions (protected read).
//	@Tags			processes
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string	true	"Process ID"
//	@Success		200			{object}	apicommon.VotingProcessResponse
//	@Failure		401			{object}	errors.Error
//	@Failure		404			{object}	errors.Error
//	@Router			/processes/{processId} [get]
func (a *API) votingProcessInfoHandler(w http.ResponseWriter, r *http.Request) {
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
	if !user.HasRoleFor(vp.OrgAddress, db.ManagerRole) && !user.HasRoleFor(vp.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Write(w)
		return
	}
	census, _ := a.db.Census(vp.CensusID.Hex())
	apicommon.HTTPWriteJSON(w, apicommon.VotingProcessResponseFromDB(vp, questions, census))
}

// listVotingProcessesHandler godoc
//
//	@Summary		List voting processes
//	@Description	Paginated list of an organization's voting processes (protected). Filter by question status.
//	@Tags			processes
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	query		string	true	"Organization address"
//	@Param			status		query		string	false	"Filter by question status"
//	@Param			page		query		int		false	"Page (1-based)"
//	@Param			limit		query		int		false	"Page size"
//	@Success		200			{object}	apicommon.VotingProcessListResponse
//	@Failure		401			{object}	errors.Error
//	@Router			/processes [get]
func (a *API) listVotingProcessesHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	orgAddressStr := r.URL.Query().Get("orgAddress")
	if orgAddressStr == "" {
		errors.ErrMalformedURLParam.Withf("missing orgAddress").Write(w)
		return
	}
	if !common.IsHexAddress(orgAddressStr) {
		errors.ErrMalformedURLParam.Withf("invalid orgAddress").Write(w)
		return
	}
	orgAddress := common.HexToAddress(orgAddressStr)
	if !user.HasRoleFor(orgAddress, db.ManagerRole) && !user.HasRoleFor(orgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Write(w)
		return
	}
	params, err := parsePaginationParams(r.URL.Query().Get(ParamPage), r.URL.Query().Get(ParamLimit))
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}
	total, list, err := a.db.ListVotingProcesses(orgAddress, r.URL.Query().Get("status"), params.Page, params.Limit)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	pagination, err := calculatePagination(params.Page, params.Limit, total)
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}
	resp := &apicommon.VotingProcessListResponse{
		Processes:  make([]apicommon.VotingProcessResponse, 0, len(list)),
		Pagination: pagination,
	}
	for i := range list {
		vp := &list[i]
		questions, err := a.db.QuestionsByProcess(vp.ID)
		if err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		census, _ := a.db.Census(vp.CensusID.Hex())
		resp.Processes = append(resp.Processes, *apicommon.VotingProcessResponseFromDB(vp, questions, census))
	}
	apicommon.HTTPWriteJSON(w, resp)
}

// validateVotingProcessHandler godoc
//
//	@Summary		Validate a voting process for publishing
//	@Description	Publish-readiness dry-run. Returns { valid, errors } without changing anything.
//	@Tags			processes
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string	true	"Process ID"
//	@Success		200			{object}	apicommon.VotingProcessValidateResponse
//	@Failure		401			{object}	errors.Error
//	@Failure		404			{object}	errors.Error
//	@Router			/processes/{processId}/check [get]
func (a *API) validateVotingProcessHandler(w http.ResponseWriter, r *http.Request) {
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
	if !user.HasRoleFor(vp.OrgAddress, db.ManagerRole) && !user.HasRoleFor(vp.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Write(w)
		return
	}
	census, _ := a.db.Census(vp.CensusID.Hex())
	problems := a.publishPreflightProblems(vp, questions, census, user)
	apicommon.HTTPWriteJSON(w, &apicommon.VotingProcessValidateResponse{
		Valid:  len(problems) == 0,
		Errors: problems,
	})
}

// votingProcessQuestionHandler godoc
//
//	@Summary		Get a voting process question
//	@Description	Public voter read of a single question, including its synced status and eligibility.
//	@Tags			processes
//	@Produce		json
//	@Param			processId	path		string	true	"Process ID"
//	@Param			questionId	path		string	true	"Question ID"
//	@Success		200			{object}	apicommon.PublicQuestionResponse
//	@Failure		404			{object}	errors.Error
//	@Router			/processes/{processId}/questions/{questionId} [get]
func (a *API) votingProcessQuestionHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	qid, err := primitive.ObjectIDFromHex(chi.URLParam(r, "questionId"))
	if err != nil {
		errors.ErrMalformedURLParam.Withf("invalid question ID").Write(w)
		return
	}
	question, err := a.db.Question(qid)
	if err != nil && err != db.ErrNotFound {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if err != nil || question.ProcessID != oid {
		errors.ErrProcessNotFound.Withf("question not found").Write(w)
		return
	}
	// hydrate the parent process's census config (the auth policy the voter must satisfy); the
	// member list and per-question eligibility subset are never exposed on this public endpoint.
	vp, err := a.db.VotingProcess(oid)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	// this is a public (voter-facing) read: only published processes are visible, so drafts are
	// not readable by unauthenticated callers.
	if !vp.Published {
		errors.ErrProcessNotFound.Withf("question not found").Write(w)
		return
	}
	census, _ := a.db.Census(vp.CensusID.Hex())
	apicommon.HTTPWriteJSON(w, apicommon.PublicQuestionResponseFromDB(question, census))
}

// votingProcessParticipantHandler godoc
//
//	@Summary		Get a voting process participant
//	@Description	Public participant info for a published voting process, mirroring the bundle
//	@Description	participant endpoint. PLACEHOLDER: validates the process (published only) and the
//	@Description	participant id, and currently returns null — participant election info is not yet
//	@Description	surfaced (the bundle equivalent is likewise a stub pending the CSP indexer lookup).
//	@Tags			processes
//	@Produce		json
//	@Param			processId		path		string		true	"Process ID"
//	@Param			participantId	path		string		true	"Participant ID"
//	@Success		200				{object}	interface{}	"Placeholder: null until participant info is surfaced"
//	@Failure		400				{object}	errors.Error
//	@Failure		404				{object}	errors.Error
//	@Router			/processes/{processId}/participant/{participantId} [get]
func (a *API) votingProcessParticipantHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	participantID := chi.URLParam(r, "participantId")
	if participantID == "" {
		errors.ErrMalformedURLParam.Withf("missing participant ID").Write(w)
		return
	}
	vp, ok := a.loadVotingProcess(w, oid)
	if !ok {
		return
	}
	// public (voter-facing) read: only published processes are visible, so a draft is not
	// revealed to unauthenticated callers.
	if !vp.Published {
		errors.ErrProcessNotFound.Withf("process not found").Write(w)
		return
	}
	// mirrors processBundleParticipantInfoHandler: participant election info is not yet surfaced
	// (the bundle equivalent returns nil pending the CSP indexer lookup).
	apicommon.HTTPWriteJSON(w, nil)
}

// votingProcessResultsHandler godoc
//
//	@Summary		Get a voting process results
//	@Description	Public per-question on-chain results of a published voting process: one entry per
//	@Description	published question, each with the trimmed election state (status, vote count,
//	@Description	dates, whether final, and the tally). No authentication is required.
//	@Tags			processes
//	@Produce		json
//	@Param			processId	path		string	true	"Process ID"
//	@Success		200			{object}	apicommon.VotingProcessResultsResponse
//	@Failure		400			{object}	errors.Error
//	@Failure		404			{object}	errors.Error
//	@Failure		500			{object}	errors.Error
//	@Router			/processes/{processId}/results [get]
func (a *API) votingProcessResultsHandler(w http.ResponseWriter, r *http.Request) {
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
	// results only exist once the process has been published on chain.
	if !vp.Published {
		errors.ErrProcessNotFound.Withf("process not published").Write(w)
		return
	}
	resp := &apicommon.VotingProcessResultsResponse{ID: oid.Hex()}
	for i := range questions {
		q := &questions[i]
		if len(q.UpstreamID) == 0 {
			continue // question not yet on chain
		}
		election, err := a.account.Election(q.UpstreamID)
		if err != nil {
			errors.ErrVochainRequestFailed.WithErr(err).Write(w)
			return
		}
		entry := apicommon.VotingProcessQuestionResults{
			QuestionID: q.ID.Hex(),
			UpstreamID: q.UpstreamID,
			ProcessResultsResponse: apicommon.ProcessResultsResponse{
				Status:       election.Status,
				VoteCount:    election.VoteCount,
				StartDate:    election.StartDate,
				EndDate:      election.EndDate,
				FinalResults: election.FinalResults,
			},
		}
		if len(election.Results) > 0 {
			results := make([][]string, len(election.Results))
			for j, question := range election.Results {
				values := make([]string, len(question))
				for k, value := range question {
					values[k] = value.String()
				}
				results[j] = values
			}
			entry.Results = results
		}
		resp.Questions = append(resp.Questions, entry)
	}
	apicommon.HTTPWriteJSON(w, resp)
}

// votingProcessID parses and validates the {processId} URL param.
func (*API) votingProcessID(w http.ResponseWriter, r *http.Request) (primitive.ObjectID, bool) {
	oid, err := primitive.ObjectIDFromHex(chi.URLParam(r, "processId"))
	if err != nil {
		errors.ErrMalformedURLParam.Withf("invalid process ID").Write(w)
		return primitive.NilObjectID, false
	}
	return oid, true
}

// loadVotingProcess loads a voting process, writing the proper error on failure.
func (a *API) loadVotingProcess(w http.ResponseWriter, oid primitive.ObjectID) (*db.VotingProcess, bool) {
	vp, err := a.db.VotingProcess(oid)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrProcessNotFound.Write(w)
			return nil, false
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return nil, false
	}
	return vp, true
}

// validateVotingProcessForPublish returns the list of reasons a process cannot be published
// (empty when it is ready). Used by GET .../check and by publish.
func validateVotingProcessForPublish(
	vp *db.VotingProcess, questions []db.VotingProcessQuestion, census *db.Census,
) []string {
	var problems []string
	if len(vp.Title) == 0 {
		problems = append(problems, "missing title")
	}
	if vp.EndDate.IsZero() || !vp.EndDate.After(time.Now()) {
		problems = append(problems, "endDate must be in the future")
	}
	if !vp.StartDate.IsZero() && !vp.EndDate.After(vp.StartDate) {
		problems = append(problems, "endDate must be after startDate")
	}
	if census == nil {
		problems = append(problems, "census not resolvable")
	}
	if len(questions) == 0 {
		problems = append(problems, "at least one question is required")
	}
	for i := range questions {
		q := &questions[i]
		if len(q.Choices) == 0 {
			problems = append(problems, fmt.Sprintf("question %d has no choices", i))
		}
		if q.BallotProtocol == nil && q.Type != db.VotingTypeSingleChoice && q.Type != db.VotingTypeMultiChoice {
			problems = append(problems, fmt.Sprintf("question %d has an unsupported type %q", i, q.Type))
		}
	}
	return problems
}

// writeSubscriptionError writes a typed API error verbatim, falling back to 500.
func writeSubscriptionError(w http.ResponseWriter, err error) {
	if apiErr, ok := err.(errors.Error); ok {
		apiErr.Write(w)
		return
	}
	errors.ErrGenericInternalServerError.WithErr(err).Write(w)
}
