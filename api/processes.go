package api

import (
	"encoding/json"
	stderrors "errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/bson/primitive"
	dvoteapi "go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/log"
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
//	@Description
//	@Description	Each question must define a named `type` — `singlechoice`, `multichoice`, `ranked`
//	@Description	or `cumulative` — a raw `ballotProtocol`, or both. `multichoice` and `cumulative`
//	@Description	need a `typeSetup` (maxChoices for the former, budget and costExponent for the
//	@Description	latter); `singlechoice` and `ranked` derive their whole protocol from the choices.
//	@Description	The two are kept in sync: whichever is supplied, the other is derived, so the
//	@Description	stored question always describes the election it will mint. A supplied
//	@Description	`ballotProtocol` is authoritative — it is what reaches the chain — so `type`
//	@Description	and `typeSetup` are re-derived from it, and come back empty only when it encodes a
//	@Description	shape with no named type at all (vote overwrites, weighted cost, or a hand-crafted
//	@Description	non-canonical shape). Sending both halves is fine when they agree; when they
//	@Description	describe two different named ballots it is a 400 rather than a silent win
//	@Description	for the protocol. **Omit `ballotProtocol` to author or edit a question through its
//	@Description	`typeSetup`** — responses always carry a protocol, so echoing one back unchanged
//	@Description	re-applies it.
//	@Description
//	@Description	`typeSetup.minChoices` has no on-chain counterpart and is a validation hint for
//	@Description	clients. It is stored as sent for multichoice, and is always `1` for singlechoice,
//	@Description	whose ballot is the single field a voter fills.
//	@Description
//	@Description	`typeSetup.uniqueChoices` is rejected for multichoice: every choice is an independent
//	@Description	yes/no field, so a unique-values ballot admits no vote and the election would tally
//	@Description	every vote to zero. A `ballotProtocol` whose `uniqueValues` cannot be satisfied
//	@Description	(fewer legal values than fields) is rejected for the same reason.
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
		// ballot shape: a question must define a named type, a raw BallotProtocol, or both. The
		// two halves are reconciled below so they describe the same ballot; a supplied protocol
		// is what the election will be, so it wins and the type is re-derived from it. typeSetup is
		// required for multichoice and cumulative (singlechoice and ranked derive their whole
		// protocol from the choices); a multichoice maps MaxChoices onto MaxTotalCost so it must be
		// bounded.
		if q.BallotProtocol == nil {
			switch q.Type {
			case "":
				return nil, errors.ErrInvalidData.Withf("question %d: a type or a ballotProtocol is required", i)
			case db.VotingTypeMultiChoice:
				if q.TypeSetup.MaxChoices < 1 || q.TypeSetup.MaxChoices > uint32(len(q.Choices)) {
					return nil, errors.ErrInvalidData.Withf(
						"question %d: maxChoices must be between 1 and the number of choices (%d)", i, len(q.Choices),
					)
				}
				if q.TypeSetup.MinChoices > q.TypeSetup.MaxChoices {
					return nil, errors.ErrInvalidData.Withf("question %d: minChoices cannot exceed maxChoices", i)
				}
				if q.TypeSetup.UniqueChoices {
					// multichoice gives every choice its own 0/1 field, so a voter already cannot
					// select one twice — and a unique-values ballot over those fields admits no
					// vote at all: the election would accept every envelope and tally zero.
					return nil, errors.ErrInvalidData.Withf(
						"question %d: uniqueChoices is not supported for multichoice, where each choice is an "+
							"independent yes/no field; use type %q for a ranked ballot", i, db.VotingTypeRanked,
					)
				}
			case db.VotingTypeRanked:
				// ranked uses no typeSetup: its protocol is fixed by the choices. Reject a non-empty
				// setup rather than silently dropping it, so a client does not believe it set a
				// constraint the ballot does not have.
				if q.TypeSetup != (db.QuestionTypeSetup{}) {
					return nil, errors.ErrInvalidData.Withf(
						"question %d: type %q takes no typeSetup; its ballot is fixed by the choices", i, q.Type,
					)
				}
			case db.VotingTypeCumulative:
				// cumulative uses only budget/costExponent (validated when the protocol is derived);
				// the multichoice fields do not apply, so reject them rather than silently dropping them.
				if q.TypeSetup.MinChoices != 0 || q.TypeSetup.MaxChoices != 0 || q.TypeSetup.UniqueChoices {
					return nil, errors.ErrInvalidData.Withf(
						"question %d: minChoices, maxChoices and uniqueChoices apply only to multichoice; "+
							"cumulative is configured through typeSetup.budget and typeSetup.costExponent", i,
					)
				}
			case db.VotingTypeSingleChoice:
				// singlechoice ignores typeSetup
			default:
				return nil, errors.ErrInvalidData.Withf("question %d: unsupported type %q", i, q.Type)
			}
		} else if err := account.ValidateBallotProtocol(q.BallotProtocol); err != nil {
			return nil, errors.ErrInvalidData.Withf("question %d: invalid ballotProtocol: %v", i, err)
		}
		shape, err := account.ResolveBallotShape(account.BallotShapeInput{
			Type:      q.Type,
			TypeSetup: q.TypeSetup,
			Protocol:  q.BallotProtocol,
			Choices:   q.Choices,
		})
		if err != nil {
			return nil, errors.ErrInvalidData.Withf("question %d: %v", i, err)
		}
		// a memo is gated to a single "open" choice per question: at most one choice may set
		// openValue, since the memo resolution correlates each vote package against it.
		open := 0
		for _, c := range q.Choices {
			if c.OpenValue {
				open++
			}
		}
		if open > 1 {
			return nil, errors.ErrInvalidData.Withf("question %d: at most one choice can have openValue", i)
		}
		// openValue only where "the vote selected the open choice" is well-defined (see
		// account.OpenChoiceMatcher): ranked ranks every choice on every ballot, and an unnamed
		// raw protocol has no defined package layout to correlate against.
		if open == 1 {
			switch shape.Type {
			case db.VotingTypeSingleChoice, db.VotingTypeMultiChoice, db.VotingTypeCumulative:
			default:
				return nil, errors.ErrInvalidData.Withf(
					"question %d: openValue requires a singlechoice, multichoice or cumulative question", i)
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
			Type:              shape.Type,
			TypeSetup:         shape.TypeSetup,
			BallotProtocol:    shape.Protocol,
			SecretUntilTheEnd: q.SecretUntilTheEnd,
			EligibleMemberIDs: eligible,
			Metadata:          q.Metadata,
		})
	}
	return built, nil
}

// refusePublishInProgress answers 409 while a publish worker holds the process, and reports
// whether it did.
//
// publishVotingProcessHandler snapshots the questions, claims the process and hands the snapshot to
// a worker that runs for minutes. Throughout that window the process is still unpublished and its
// questions carry no upstreamId, so every "is this a draft?" check reads true — and a mutation
// taken on that basis lands on a process the worker is concurrently putting on chain.
func refusePublishInProgress(w http.ResponseWriter, vp *db.VotingProcess) bool {
	if !vp.PublishInProgress() {
		return false
	}
	errors.ErrPublishInProgress.Write(w)
	return true
}

// writeQuestions replaces the process's stored questions with a pre-built (already validated) set
// and records their ids on the process. The replacement is per slot rather than delete-all then
// insert-all, so two overlapping draft updates cannot strand a row — see db.SetProcessQuestions.
// Callers run buildQuestions first, so this only fails on infra errors.
//
// The ids are stored through a targeted update rather than a whole-document write: this runs right
// after a conditional update may have forced updatedAt forward, and re-stamping the document with a
// fresh time.Now() could push that token back to a value a client already spent.
func (a *API) writeQuestions(vp *db.VotingProcess, built []*db.VotingProcessQuestion) error {
	questionIDs, err := a.db.SetProcessQuestions(vp.ID, built)
	if err != nil {
		return fmt.Errorf("failed to store questions: %w", err)
	}
	vp.QuestionIDs = questionIDs
	if err := a.db.SetVotingProcessQuestionIDs(vp.ID, questionIDs); err != nil {
		return fmt.Errorf("failed to update process questions: %w", err)
	}
	return nil
}

// parseUpdatedAt reads the optional conditional-update token of an update request. The zero time
// means the client sent none and opts out of the check.
//
// It parses with apicommon.UpdatedAtLayout first — the exact shape the read endpoint emits, so the
// round-trip of a value the client just read goes through the layout that documents itself as the
// wire format — and falls back to RFC3339 for a client sending a different sub-second precision or a
// non-Z offset.
func parseUpdatedAt(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(apicommon.UpdatedAtLayout, s)
	if err != nil {
		if t, err = time.Parse(time.RFC3339, s); err != nil {
			return time.Time{}, fmt.Errorf(
				"invalid updatedAt %q: expected %s (or RFC3339) as returned by the read endpoint",
				s, apicommon.UpdatedAtLayout)
		}
	}
	return t.UTC(), nil
}

// writeDraftWriteConflict reports which of SetVotingProcessDraft's preconditions refused the write.
// The function returns a bare db.ErrConflict, so the process is re-read to tell a publish that
// started during the request apart from the optimistic-concurrency token going stale.
func (a *API) writeDraftWriteConflict(w http.ResponseWriter, id primitive.ObjectID, updatedAt string) {
	switch current, err := a.db.VotingProcess(id); {
	case err != nil:
		// the state that refused us is unreadable; the token is the likelier cause and the
		// recovery — refetch and retry — is the same either way
		errors.ErrStaleUpdate.Withf("the process changed while it was being updated; refetch and retry").Write(w)
	case current.Published:
		errors.ErrDuplicateConflict.Withf("process already published and not in draft mode").Write(w)
	case current.PublishInProgress():
		errors.ErrPublishInProgress.Write(w)
	default:
		errors.ErrStaleUpdate.Withf("the process was modified after updatedAt %s; refetch and retry",
			updatedAt).Write(w)
	}
}

// updateVotingProcessHandler godoc
//
//	@Summary		Update a voting process draft
//	@Description	Update a voting process while it is still a draft (not published). 409 if already published.
//	@Description	The questions are replaced wholesale, and each one's ballot shape is reconciled exactly
//	@Description	as on create. Reading a question and PUTting it back unchanged is a no-op; editing its
//	@Description	`typeSetup` while echoing the `ballotProtocol` that still encodes the old shape is a
//	@Description	400 — omit `ballotProtocol` to edit a question through its `typeSetup`.
//	@Description
//	@Description	Send the updatedAt read from GET /processes/{processId} to make the update conditional: it is
//	@Description	rejected with 409 (40171) if anything wrote the process in between, so two editors cannot
//	@Description	overwrite each other. Omitting updatedAt opts out of that guarantee and keeps last-writer-wins.
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
	if refusePublishInProgress(w, vp) {
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
	seen, err := parseUpdatedAt(req.UpdatedAt)
	if err != nil {
		_ = a.db.DelCensus(census.ID.Hex())
		errors.ErrMalformedBody.WithErr(err).Write(w)
		return
	}
	vp.Title, vp.Description, vp.Header, vp.StreamURI = req.Title, req.Description, req.Header, req.StreamURI
	vp.StartDate, vp.EndDate, vp.CensusID = start, end, census.ID
	// a stale marker got us past the guard above: editing the draft releases it rather than
	// writing it back, matching what ClaimVotingProcessForPublish would reclaim anyway. A marker
	// that went live *after* that guard read is a different matter — the write's own precondition
	// refuses it rather than replacing it away.
	vp.Publishing = time.Time{}
	if err := a.db.SetVotingProcessDraft(vp, seen); err != nil {
		_ = a.db.DelCensus(census.ID.Hex())
		if stderrors.Is(err, db.ErrConflict) {
			a.writeDraftWriteConflict(w, vp.ID, req.UpdatedAt)
			return
		}
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
//	@Description	Read a voting process with its hydrated questions (live per-question results included).
//	@Description	Public for published processes; a draft is visible only to a Manager/Admin of the org
//	@Description	(or a voting:write API key acting as one) and returns 404 otherwise. Non-managers never
//	@Description	receive the per-question eligibleMemberIds. A Manager/Admin caller additionally receives
//	@Description	each open-value question's free-text voter memos inside its results.
//	@Tags			processes
//	@Produce		json
//	@Param			processId	path		string	true	"Process ID"
//	@Success		200			{object}	apicommon.VotingProcessResponse
//	@Failure		404			{object}	errors.Error
//	@Router			/processes/{processId} [get]
func (a *API) votingProcessInfoHandler(w http.ResponseWriter, r *http.Request) {
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
	// public read: published processes are visible to anyone; a draft only to a manager/admin of the
	// owning org (and its existence is hidden from everyone else via 404).
	isManager := a.optionalManager(r, vp.OrgAddress)
	if !vp.Published && !isManager {
		errors.ErrProcessNotFound.Write(w)
		return
	}
	census, _ := a.db.Census(vp.CensusID.Hex())
	// serve the stored status now; refresh each published question from the chain in the background
	// to catch status changes made directly on-chain (outside this API).
	for i := range questions {
		a.enqueueReconcileIfStale(&questions[i])
	}
	// resolve the vote encryption keys of encrypted questions (so clients can seal encrypted ballots)
	// and the live on-chain tally of each published question (finalResults marks final), concurrently:
	// each needs a Vochain round-trip, so bounded pools keep this read fast for a many-question process.
	a.resolveQuestionEncryptionKeysBatch(questions)
	// memos (free-text voter input on open-value choices) are manager-only, so they are resolved into
	// the results only for a manager/admin caller — never for the public view.
	a.resolveQuestionResultsBatch(questions, isManager)
	// a draft written before the two ballot halves were reconciled is served reconciled, so the
	// body a client echoes back is one authoring still accepts.
	reconcileDraftQuestionShapes(questions)
	// non-managers must not see the per-question eligibility member ids (who can vote).
	if !isManager {
		redactQuestionsForPublic(questions)
	}
	resp := apicommon.VotingProcessResponseFromDB(vp, questions, census, a.account.ChainID())
	resp.Census.TotalWeight = a.censusTotalWeight(census)
	apicommon.HTTPWriteJSON(w, resp)
}

// reconcileDraftQuestionShapes serves the reconciled ballot shape of the questions that have not
// minted an election yet, mutating the passed slice in place.
//
// Questions stored before the two halves were reconciled can carry anything the old code accepted:
// a typeSetup.uniqueChoices no named type supports, no ballotProtocol at all, or two halves that
// contradict each other. Authoring now refuses all three, so a client that reads such a draft and
// PUTs the question array back — the only way to edit one, since update replaces them wholesale —
// would be refused for a lie it did not write. Serving the shape a publish would actually use makes
// that round-trip work and makes the read consistent with the invariant.
//
// Only unminted questions: a published legacy question's election really does carry the parameters
// it was minted with (the #619 elections have uniqueValues set on chain), so deriving a clean shape
// for it would misreport the chain — and it cannot be edited anyway. Nothing is written back; the
// first update persists it.
func reconcileDraftQuestionShapes(questions []db.VotingProcessQuestion) {
	for i := range questions {
		q := &questions[i]
		if len(q.UpstreamID) > 0 || len(q.Choices) == 0 {
			continue
		}
		in := account.BallotShapeInput{
			Type: q.Type, TypeSetup: q.TypeSetup, Protocol: q.BallotProtocol, Choices: q.Choices,
		}
		// a stored protocol is what publish would mint, so it wins over a stored type outright —
		// asking to reconcile both halves would only reject a contradiction nobody can edit away.
		if in.Protocol != nil {
			in.Type, in.TypeSetup = "", db.QuestionTypeSetup{}
		}
		shape, err := account.ResolveBallotShape(in)
		if err != nil {
			continue // an unusable stored shape is publish's problem to report, not a read's
		}
		q.Type, q.TypeSetup, q.BallotProtocol = shape.Type, shape.TypeSetup, shape.Protocol
	}
}

// redactQuestionsForPublic strips manager-only fields from questions before serving them to a
// non-manager (anonymous, viewer, or other-org) caller — currently the per-question eligibility member
// ids (the list of who may vote). Mutates the passed slice in place.
func redactQuestionsForPublic(questions []db.VotingProcessQuestion) {
	for i := range questions {
		questions[i].EligibleMemberIDs = nil
	}
}

// listVotingProcessesHandler godoc
//
//	@Summary		List voting processes
//	@Description	Paginated list of an organization's voting processes. Public: anonymous callers get
//	@Description	published processes only (without per-question eligibleMemberIds). A Manager/Admin of
//	@Description	the org (or a voting:write API key acting as one) also gets drafts and the eligibility.
//	@Description	Filter by question status, and by published state.
//	@Description	published=true returns published processes only; published=false returns drafts only and
//	@Description	requires Manager/Admin (401 otherwise). Omitting it keeps the caller's default view.
//	@Description	Combining published=false with status returns nothing: a draft's questions have no
//	@Description	on-chain status yet, and status matches on that field.
//	@Tags			processes
//	@Produce		json
//	@Param			orgAddress	query		string	true	"Organization address"
//	@Param			status		query		string	false	"Filter by question status"
//	@Param			published	query		bool	false	"Filter by published state; false (drafts) requires Manager/Admin"
//	@Param			page		query		int		false	"Page (1-based)"
//	@Param			limit		query		int		false	"Page size"
//	@Success		200			{object}	apicommon.VotingProcessListResponse
//	@Failure		400			{object}	errors.Error
//	@Failure		401			{object}	errors.Error
//	@Router			/processes [get]
func (a *API) listVotingProcessesHandler(w http.ResponseWriter, r *http.Request) {
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
	// IsHexAddress accepts the zero address; reject it as malformed (400) so it never reaches the db
	// layer (which errors on it and would otherwise surface as a 500 on this now-public endpoint).
	if orgAddress == (common.Address{}) {
		errors.ErrMalformedURLParam.Withf("invalid orgAddress").Write(w)
		return
	}
	// public read: anonymous callers see published processes only; a manager/admin of the org (or a
	// voting:write API key acting as one) also sees drafts (and keeps the per-question eligibility).
	isManager := a.optionalManager(r, orgAddress)
	draft := db.PublishedOnly
	if isManager {
		draft = db.AllProcesses
	}
	// the optional published filter narrows that default view. Asking for drafts is manager-only,
	// mirroring organizationListProcessDraftsHandler on the legacy routes.
	if s := r.URL.Query().Get("published"); s != "" {
		published, err := strconv.ParseBool(s)
		if err != nil {
			errors.ErrMalformedURLParam.Withf("invalid published").Write(w)
			return
		}
		switch {
		case published:
			draft = db.PublishedOnly
		case !isManager:
			errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
			return
		default:
			draft = db.DraftOnly
		}
	}
	params, err := parsePaginationParams(r.URL.Query().Get(ParamPage), r.URL.Query().Get(ParamLimit))
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}
	// stored question status is uppercase; upper-case the filter so client input stays case-insensitive.
	statusFilter := strings.ToUpper(r.URL.Query().Get("status"))
	total, list, err := a.db.ListVotingProcesses(orgAddress, statusFilter, draft, params.Page, params.Limit)
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
	chainID := a.account.ChainID()
	for i := range list {
		vp := &list[i]
		questions, err := a.db.QuestionsByProcess(vp.ID)
		if err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		census, _ := a.db.Census(vp.CensusID.Hex())
		reconcileDraftQuestionShapes(questions)
		if !isManager {
			redactQuestionsForPublic(questions)
		}
		resp.Processes = append(resp.Processes, *apicommon.VotingProcessResponseFromDB(vp, questions, census, chainID))
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
//	@Router			/processes/{processId}/validation [get]
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
	// the dry-run reports every problem the same way: a mismatched question set is just one more
	// entry in errors, so the mismatch flag publish acts on is irrelevant here.
	problems, _ := a.publishPreflightProblems(vp, questions, census, user)
	apicommon.HTTPWriteJSON(w, &apicommon.VotingProcessValidateResponse{
		Valid:  len(problems) == 0,
		Errors: problems,
	})
}

// votingProcessQuestionHandler godoc
//
//	@Summary		Get a voting process question
//	@Description	Public voter read of a single question, including its synced status and live results.
//	@Description	Auth is optional: only a Manager/Admin of the org (or a voting:write API key) receives
//	@Description	the question's eligibleMemberIds and, for an open-value question, the free-text voter
//	@Description	memos inside its results.
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
	// census member list is never exposed here, and the per-question eligibility subset only to a
	// manager/admin of the owning org.
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
	// serve the stored status now; refresh it from the chain in the background so a status change
	// made directly on-chain (outside this API) is picked up.
	a.enqueueReconcileIfStale(question)
	// resolve the vote encryption keys so voters can seal an encrypted ballot for this question.
	question.EncryptionKeys = a.resolveQuestionEncryptionKeys(question)
	// surface the live on-chain tally for a published question; memos (manager-only) are included only
	// when the caller is a manager/admin of the owning org.
	isManager := a.optionalManager(r, vp.OrgAddress)
	question.Results = a.resolveQuestionResults(question, isManager)
	resp := apicommon.PublicQuestionResponseFromDB(question, census)
	// the eligibility subset names who may vote: only a manager/admin of the owning org sees it
	if isManager {
		resp.EligibleMemberIDs = question.EligibleMemberIDs
	}
	apicommon.HTTPWriteJSON(w, resp)
}

// parallelForEach runs fn(0..n-1) concurrently with a bounded worker pool and waits for all to
// finish. It backs the per-question read resolvers and the results handler, which each fan out one
// Vochain round-trip per question: the bound keeps GET /processes/{id} and /results fast for a process
// with many questions (up to maxQuestionsPerProcess) without an unbounded goroutine burst.
func parallelForEach(n int, fn func(i int)) {
	const workers = 8
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i := range n {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			fn(i)
		})
	}
	wg.Wait()
}

// resolveQuestionEncryptionKeysBatch resolves the vote encryption keys of every question concurrently
// (bounded), setting q.EncryptionKeys in place. Gated/cached questions return immediately (no chain call).
func (a *API) resolveQuestionEncryptionKeysBatch(questions []db.VotingProcessQuestion) {
	parallelForEach(len(questions), func(i int) {
		questions[i].EncryptionKeys = a.resolveQuestionEncryptionKeys(&questions[i])
	})
}

// resolveQuestionEncryptionKeys returns the vote-encryption public keys of a question's on-chain
// election, fetching them from the Vochain only when needed and caching them on the question once
// published. It is a no-op (nil, no chain call) for non-encrypted questions and unpublished drafts,
// and serves cached keys without a chain round-trip. The result is cached only when non-empty:
// between an election's creation and the keykeepers publishing its keys the node returns an empty
// set, and a later read must still resolve them (mirror of resolveProcessEncryptionKeys).
func (a *API) resolveQuestionEncryptionKeys(q *db.VotingProcessQuestion) []db.EncryptionKey {
	// gate: only encrypted questions have encryption keys; keeps every other question chain-free.
	if !q.SecretUntilTheEnd {
		return nil
	}
	// nothing on chain yet (draft) — no keys to fetch.
	if len(q.UpstreamID) == 0 {
		return nil
	}
	// immutable once published: serve the cached keys without a chain round-trip.
	if len(q.EncryptionKeys) > 0 {
		return q.EncryptionKeys
	}
	keys, err := a.account.ElectionEncryptionKeys(q.UpstreamID)
	if err != nil {
		log.Warnw("encryption keys: election keys fetch failed",
			"question", q.ID.Hex(), "upstreamId", q.UpstreamID.String(), "error", err)
		return nil
	}
	// not published yet — do not cache an empty set so a later read still resolves them.
	if len(keys) == 0 {
		return nil
	}
	if err := a.db.SetQuestionEncryptionKeys(q.ID, keys); err != nil {
		log.Warnw("encryption keys: could not persist question keys", "question", q.ID.Hex(), "error", err)
	}
	return keys
}

// censusTotalWeight returns the whole-census total voting weight (sum of members' weights) exposed
// on CensusSpec. A non-weighted census contributes weight 1 per member, so the total is just the
// participant count (Size) with no query; a weighted census sums OrgMember.Weight over its members.
// On aggregation failure it returns 0 (NOT Size): totalWeight backs a report/certification denominator,
// where a plausible-but-wrong total is worse than an absent one — 0 makes omitempty drop the field so
// the client renders "not available" instead of computing every percentage against a wrong total.
func (a *API) censusTotalWeight(census *db.Census) int64 {
	if census == nil {
		return 0
	}
	if !census.Weighted {
		return census.Size
	}
	total, err := a.db.CensusTotalWeight(census.ID.Hex())
	if err != nil {
		log.Warnw("census total weight: aggregation failed", "census", census.ID.Hex(), "error", err)
		return 0
	}
	return total
}

// questionResultsFromElection maps a question's on-chain election onto the QuestionResults shape.
// MaxVoters is the election's own maxCensusSize — already restricted to the question's eligibility
// subset at publish (account.ComputeMaxCensusSize). Results is the full tally matrix stringified
// verbatim (one row per ballot field), so both single-choice (one row of value buckets) and
// multi-choice (one row per choice) questions are represented losslessly; it stays nil until the
// tally publishes.
func questionResultsFromElection(e *dvoteapi.Election) db.QuestionResults {
	qr := db.QuestionResults{
		VoteCount:    e.VoteCount,
		FinalResults: e.FinalResults,
	}
	if e.Census != nil {
		qr.MaxVoters = e.Census.MaxCensusSize
	}
	if len(e.Results) > 0 {
		results := make([][]string, len(e.Results))
		for i, field := range e.Results {
			values := make([]string, len(field))
			for j, v := range field {
				values[j] = v.String()
			}
			results[i] = values
		}
		qr.Results = results
	}
	return qr
}

// resolveQuestionMemos returns the free-text voter memos cast alongside a published question's
// open-value choice, or nil when the question has no open choice / no election. The per-ballot-type
// correlation lives in account.OpenChoiceMatcher; buildQuestions only accepts openValue on the types
// it supports. Best-effort: a chain error yields nil (logged), never fails the read. Manager-gated
// by the caller (see withMemos).
//
// ponytail: ElectionMemos is an N+1 over the election's votes (page the list + a per-memo-vote fetch to
// correlate the memo to a choice). Bounded to manager reads of open-value questions; add a short-TTL
// cache if a manager polls a large open-value election hard.
func (a *API) resolveQuestionMemos(q *db.VotingProcessQuestion) []string {
	selectsOpen := account.OpenChoiceMatcher(q.Type, q.Choices)
	if selectsOpen == nil || len(q.UpstreamID) == 0 {
		return nil
	}
	memos, err := a.account.ElectionMemos(q.UpstreamID, selectsOpen)
	if err != nil {
		log.Warnw("results: memos fetch failed",
			"question", q.ID.Hex(), "upstreamId", q.UpstreamID.String(), "error", err)
		return nil
	}
	return memos
}

// resolveQuestionResults returns a question's live on-chain tally for any published (on-chain)
// question — the caller distinguishes live from final via QuestionResults.FinalResults. It is a no-op
// (nil, no chain call) only for an unpublished draft (no UpstreamID). A secretUntilTheEnd question
// returns empty results until the keys are revealed (the node hides the tally, not this gate). When
// withMemos is set (a manager/admin caller), the open-value question's voter memos are included too.
//
// ponytail: not cached, so a live read hits the chain per poll (one Election call per published
// question); add a short-TTL cache if this read gets hot — /results already does the same per poll.
//
//nolint:revive // withMemos gates the manager-only memo fetch; a bool keeps the three resolvers uniform
func (a *API) resolveQuestionResults(q *db.VotingProcessQuestion, withMemos bool) *db.QuestionResults {
	// nothing on chain yet (draft) — no results.
	if len(q.UpstreamID) == 0 {
		return nil
	}
	election, err := a.account.Election(q.UpstreamID)
	if err != nil {
		log.Warnw("results: election fetch failed",
			"question", q.ID.Hex(), "upstreamId", q.UpstreamID.String(), "error", err)
		return nil
	}
	qr := questionResultsFromElection(election)
	if withMemos {
		qr.Memos = a.resolveQuestionMemos(q)
	}
	return &qr
}

// resolveQuestionResultsBatch resolves the on-chain tally of every published question concurrently
// (bounded), setting q.Results in place. Draft questions short-circuit to nil without a chain call.
// withMemos (a manager/admin caller) additionally includes each open-value question's voter memos.
func (a *API) resolveQuestionResultsBatch(questions []db.VotingProcessQuestion, withMemos bool) {
	parallelForEach(len(questions), func(i int) {
		questions[i].Results = a.resolveQuestionResults(&questions[i], withMemos)
	})
}

// electionResultsBatch fetches the on-chain tally of every published question concurrently (bounded)
// and maps each to a results entry, preserving question order. Questions not yet on chain are skipped.
// withMemos (a manager/admin caller) additionally includes each open-value question's voter memos.
// Unlike the read resolvers it is all-or-error: the first election fetch failure (by question order)
// is returned so GET /processes/{id}/results never emits a silent partial tally set. Returns nil when
// no question is on chain (preserving the endpoint's "questions": null shape for that case).
func (a *API) electionResultsBatch(
	questions []db.VotingProcessQuestion, withMemos bool,
) ([]apicommon.VotingProcessQuestionResults, error) {
	entries := make([]*apicommon.VotingProcessQuestionResults, len(questions))
	errs := make([]error, len(questions))
	parallelForEach(len(questions), func(i int) {
		q := &questions[i]
		if len(q.UpstreamID) == 0 {
			return // question not yet on chain
		}
		election, err := a.account.Election(q.UpstreamID)
		if err != nil {
			errs[i] = fmt.Errorf("question %s: %w", q.ID.Hex(), err)
			return
		}
		qr := questionResultsFromElection(election)
		if withMemos {
			qr.Memos = a.resolveQuestionMemos(q)
		}
		entries[i] = &apicommon.VotingProcessQuestionResults{
			QuestionID:      q.ID.Hex(),
			UpstreamID:      q.UpstreamID,
			QuestionResults: qr,
		}
	})
	var out []apicommon.VotingProcessQuestionResults
	for i := range questions {
		if errs[i] != nil {
			return nil, errs[i]
		}
		if entries[i] != nil {
			out = append(out, *entries[i])
		}
	}
	return out, nil
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
//	@Router			/processes/{processId}/participants/{participantId} [get]
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
//	@Description	published question, each with its tally (vote count, max voters, whether final, and
//	@Description	the per-choice results). Auth is optional: a Manager/Admin of the org (or a voting:write
//	@Description	API key) additionally receives each open-value question's free-text voter memos.
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
	// fetch every published question's tally concurrently (bounded); all-or-error so this endpoint
	// never emits a partial tally set (a transient chain error on one question fails the response).
	// memos (manager-only) are folded in only for a manager/admin caller.
	entries, err := a.electionResultsBatch(questions, a.optionalManager(r, vp.OrgAddress))
	if err != nil {
		errors.ErrVochainRequestFailed.WithErr(err).Write(w)
		return
	}
	apicommon.HTTPWriteJSON(w, &apicommon.VotingProcessResultsResponse{ID: oid.Hex(), Questions: entries})
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

// validateVotingProcessForPublish returns the structural reasons a process cannot be published
// (empty when it is ready). The stored-question-set check lives in publishPreflightProblems instead,
// which needs to tell that one apart from the rest to answer publish with a 409.
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
		if q.BallotProtocol == nil && !db.IsNamedVotingType(q.Type) {
			problems = append(problems, fmt.Sprintf("question %d has an unsupported type %q", i, q.Type))
		}
		// A question stored before authoring rejected these can still hold a ballot no voter can
		// satisfy. Publishing it would mint an election that accepts every vote and tallies zero,
		// with nothing on the way reporting a problem, so stop it at the last point that can.
		if err := account.ValidateBallotProtocol(q.BallotProtocol); err != nil {
			problems = append(problems, fmt.Sprintf("question %d: invalid ballotProtocol: %v", i, err))
		}
	}
	return problems
}

// questionSetProblem reports a stored question set that does not match the ids the process itself
// records. That means a row carrying this processId is not one of the questions the last writer
// stored — a leftover from a pre-fix concurrent draft update, or a direct database write. Publishing
// such a process is the one irreversible step in the whole flow (the stray becomes a real on-chain
// election that cannot be withdrawn), so it is refused and the draft has to be saved again first.
// Processes predating QuestionIDs carry none and are not checked.
func questionSetProblem(vp *db.VotingProcess, questions []db.VotingProcessQuestion) string {
	if len(vp.QuestionIDs) == 0 {
		return ""
	}
	expected := make(map[primitive.ObjectID]bool, len(vp.QuestionIDs))
	for _, id := range vp.QuestionIDs {
		expected[id] = true
	}
	stray := 0
	for i := range questions {
		if !expected[questions[i].ID] {
			stray++
		}
	}
	if stray == 0 && len(questions) == len(vp.QuestionIDs) {
		return ""
	}
	return fmt.Sprintf(
		"stored questions do not match the process (%d found, %d expected, %d unknown): save the draft again",
		len(questions), len(vp.QuestionIDs), stray)
}

// writeSubscriptionError writes a typed API error verbatim, falling back to 500.
func writeSubscriptionError(w http.ResponseWriter, err error) {
	if apiErr, ok := err.(errors.Error); ok {
		apiErr.Write(w)
		return
	}
	errors.ErrGenericInternalServerError.WithErr(err).Write(w)
}
