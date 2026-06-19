package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// jobStatusHandler godoc
//
//	@Summary		Poll the status of an async transaction job
//	@Description	Returns the current state of a transaction job created by an async endpoint
//	@Description	(publish, status change or vote relay). Always responds 200; the `status` field
//	@Description	carries the lifecycle state (`pending`, `completed` or `failed`). On `completed`
//	@Description	the `result` field holds the public on-chain outcome; on `failed` the `error`
//	@Description	field holds the reason. Public endpoint: the 32-byte job id is the capability and
//	@Description	results contain only public on-chain data.
//	@Tags			process
//	@Produce		json
//	@Param			jobId	path		string						true	"Job id returned by the async endpoint (hex)"
//	@Success		200		{object}	apicommon.JobStatusResponse	"Job status and (when completed) result"
//	@Failure		400		{object}	errors.Error				"Invalid job id"
//	@Failure		404		{object}	errors.Error				"Job not found"
//	@Failure		500		{object}	errors.Error				"Internal server error"
//	@Router			/jobs/{jobId} [get]
func (a *API) jobStatusHandler(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobId")
	if jobID == "" {
		errors.ErrMalformedURLParam.Withf("missing job id").Write(w)
		return
	}

	job, err := a.db.Job(jobID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrJobNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, &apicommon.JobStatusResponse{
		JobID:  job.JobID,
		Type:   job.Type,
		Status: job.Status,
		Result: job.Result,
		Error:  job.Error,
	})
}
