package api

import (
	"net/http"

	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.vocdoni.io/proto/build/go/models"
)

// openChoice returns the question's single open-value choice (the one accepting a voter memo) and
// true if one is defined. buildQuestions guarantees at most one, so the first match is authoritative.
func openChoice(choices []db.Choice) (db.Choice, bool) {
	for _, c := range choices {
		if c.OpenValue {
			return c, true
		}
	}
	return db.Choice{}, false
}

// votingProcessMemosHandler godoc
//
//	@Summary		Get a voting process voter memos
//	@Description	Per-question raw voter memos of a published voting process, restricted to
//	@Description	questions whose on-chain election has reached RESULTS status and that define an
//	@Description	open-value choice: one entry per such question, listing every free-text memo cast
//	@Description	by a vote that selected the open choice, repeated once per such vote. Questions not
//	@Description	yet in results, or without an open-value choice, are excluded. Requires
//	@Description	Manager/Admin role of the owning organization.
//	@Tags			processes
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string	true	"Process ID"
//	@Success		200			{object}	apicommon.VotingProcessMemosResponse
//	@Failure		400			{object}	errors.Error
//	@Failure		401			{object}	errors.Error
//	@Failure		404			{object}	errors.Error
//	@Failure		500			{object}	errors.Error
//	@Router			/processes/{processId}/results/memos [get]
func (a *API) votingProcessMemosHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	// loads the process + questions and gates on Manager/Admin of the owning org.
	vp, questions, ok := a.authorizeStatusChange(w, r, oid)
	if !ok {
		return
	}
	// memos only exist once the process has been published on chain.
	if !vp.Published {
		errors.ErrProcessNotFound.Withf("process not published").Write(w)
		return
	}

	resp := &apicommon.VotingProcessMemosResponse{ID: oid.Hex()}
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
		// only surface memos once the question's election has reached results.
		if election.Status != models.ProcessStatus_RESULTS.String() {
			continue
		}
		// memos are gated to the question's single "open" choice: only votes that selected it carry
		// a meaningful memo. A question without an open choice surfaces no memos and is omitted.
		open, ok := openChoice(q.Choices)
		if !ok {
			continue
		}
		memos, err := a.account.ElectionMemos(q.UpstreamID, open.Value)
		if err != nil {
			errors.ErrVochainRequestFailed.WithErr(err).Write(w)
			return
		}
		resp.Questions = append(resp.Questions, apicommon.VotingProcessQuestionMemos{
			QuestionID: q.ID.Hex(),
			UpstreamID: q.UpstreamID,
			Memos:      memos,
		})
	}
	apicommon.HTTPWriteJSON(w, resp)
}
