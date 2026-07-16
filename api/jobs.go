package api

import (
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// jobStatusHandler godoc
//
//	@Summary		Poll the status of an async job
//	@Description	Returns the current state of an async job (member/census import or a tx job: publish,
//	@Description	status change, census update, vote relay), in the same unified shape as GET /jobs.
//	@Description	Always responds 200; `status` carries the lifecycle state (`pending`, `completed`,
//	@Description	`failed`) and `result` carries only the attributes the job produced (import counters
//	@Description	`added`/`total`/`progress`, or tx `address`/`voteID`/`status`), each omitted when empty.
//	@Description	Public endpoint: the 32-byte job id is the capability and results carry only public data.
//	@Tags			jobs
//	@Produce		json
//	@Param			jobId	path		string					true	"Job id returned by the async endpoint (hex)"
//	@Success		200		{object}	apicommon.JobResponse	"Unified job status and result"
//	@Failure		400		{object}	errors.Error			"Invalid job id"
//	@Failure		404		{object}	errors.Error			"Job not found"
//	@Failure		500		{object}	errors.Error			"Internal server error"
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

	resp := apicommon.JobResponseFromDB(job)
	apicommon.HTTPWriteJSON(w, &resp)
}

// jobsHandler godoc
//
//	@Summary		List an organization's jobs
//	@Description	Paginated list of an organization's async jobs — member/census imports and tx jobs
//	@Description	(publish, status change, census update, vote relay) — newest first. Requires
//	@Description	Manager/Admin role of the organization given by the `orgAddress` query parameter. Each
//	@Description	job carries a unified `result` that only includes the attributes its type produced.
//	@Tags			jobs
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	query		string	true	"Organization address"
//	@Param			type		query		string	false	"Filter by job type (e.g. org_members, census_participants, relay_vote)"
//	@Param			page		query		integer	false	"Page number (default: 1)"
//	@Param			limit		query		integer	false	"Items per page (default: 10)"
//	@Success		200			{object}	apicommon.JobsListResponse
//	@Failure		400			{object}	errors.Error	"Invalid input"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/jobs [get]
func (a *API) jobsHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	addrStr := r.URL.Query().Get("orgAddress")
	if !common.IsHexAddress(addrStr) {
		errors.ErrMalformedURLParam.Withf("invalid orgAddress").Write(w)
		return
	}
	orgAddress := common.HexToAddress(addrStr)
	if !user.HasRoleFor(orgAddress, db.ManagerRole) && !user.HasRoleFor(orgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of organization").Write(w)
		return
	}
	params, err := parsePaginationParams(r.URL.Query().Get(ParamPage), r.URL.Query().Get(ParamLimit))
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}
	var jobType *db.JobType
	if t := r.URL.Query().Get("type"); t != "" {
		jt := db.JobType(t)
		if !jt.IsValid() {
			errors.ErrMalformedURLParam.Withf("invalid job type: %s", t).Write(w)
			return
		}
		jobType = &jt
	}
	totalItems, jobs, err := a.db.Jobs(orgAddress, params.Page, params.Limit, jobType)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not get jobs: %v", err).Write(w)
		return
	}
	pagination, err := calculatePagination(params.Page, params.Limit, totalItems)
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}
	list := make([]apicommon.JobResponse, 0, len(jobs))
	for i := range jobs {
		list = append(list, apicommon.JobResponseFromDB(&jobs[i]))
	}
	apicommon.HTTPWriteJSON(w, &apicommon.JobsListResponse{Pagination: pagination, Jobs: list})
}
