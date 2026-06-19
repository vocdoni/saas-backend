package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/log"
)

// processResultsHandler godoc
//
//	@Summary		Get the trimmed on-chain election results
//	@Description	Fetches the current on-chain state of the election identified by its process id
//	@Description	and returns a trimmed view of it: status, vote count, start/end dates, whether the
//	@Description	results are final and the tally (if any). Public endpoint: no authentication is
//	@Description	required.
//	@Tags			process
//	@Produce		json
//	@Param			processId	path		string								true	"On-chain process id (hex)"
//	@Success		200			{object}	apicommon.ProcessResultsResponse	"Trimmed on-chain election state"
//	@Failure		400			{object}	errors.Error						"Invalid input data"
//	@Failure		404			{object}	errors.Error						"Process not found"
//	@Failure		500			{object}	errors.Error						"Internal server error"
//	@Router			/process/{processId}/results [get]
func (a *API) processResultsHandler(w http.ResponseWriter, r *http.Request) {
	var pid internal.HexBytes
	if err := pid.ParseString(chi.URLParam(r, "processId")); err != nil || len(pid) == 0 {
		errors.ErrMalformedURLParam.Withf("invalid process id").Write(w)
		return
	}

	// ensure we manage this process
	if _, err := a.db.ProcessByAddress(pid); err != nil {
		if err == db.ErrNotFound {
			errors.ErrProcessNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	election, err := a.account.Election(pid.Bytes())
	if err != nil {
		errors.ErrVochainRequestFailed.WithErr(err).Write(w)
		return
	}

	resp := apicommon.ProcessResultsResponse{
		Status:       election.Status,
		VoteCount:    election.VoteCount,
		StartDate:    election.StartDate,
		EndDate:      election.EndDate,
		FinalResults: election.FinalResults,
	}
	if len(election.Results) > 0 {
		results := make([][]string, len(election.Results))
		for i, question := range election.Results {
			values := make([]string, len(question))
			for j, value := range question {
				values[j] = value.String()
			}
			results[i] = values
		}
		resp.Results = results
	}

	apicommon.HTTPWriteJSON(w, &resp)
}

// processMetadataHandler godoc
//
//	@Summary		Get the election metadata JSON
//	@Description	Rebuilds and returns the raw ElectionMetadata JSON document of the process
//	@Description	identified by its on-chain process id. The metadata is deterministically derived
//	@Description	from the stored ElectionParams, so it is identical to the content-addressed document
//	@Description	published on chain. Public endpoint: no authentication is required.
//	@Tags			process
//	@Produce		json
//	@Param			processId	path		string			true	"On-chain process id (hex)"
//	@Success		200			{object}	object			"Election metadata JSON"
//	@Failure		400			{object}	errors.Error	"Invalid input data"
//	@Failure		404			{object}	errors.Error	"Process not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/process/{processId}/metadata [get]
func (a *API) processMetadataHandler(w http.ResponseWriter, r *http.Request) {
	var pid internal.HexBytes
	if err := pid.ParseString(chi.URLParam(r, "processId")); err != nil || len(pid) == 0 {
		errors.ErrMalformedURLParam.Withf("invalid process id").Write(w)
		return
	}

	process, err := a.db.ProcessByAddress(pid)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrProcessNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if process.ElectionParams == nil {
		errors.ErrProcessNotFound.Withf("process has no metadata").Write(w)
		return
	}

	metaBytes, err := account.BuildElectionMetadata(process.ElectionParams)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(metaBytes); err != nil {
		log.Warnw("failed to write metadata response", "error", err)
	}
}
