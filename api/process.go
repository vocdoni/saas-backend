package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// createProcessHandler godoc
//
//	@Summary		Create a new voting process
//	@Description	Create a new voting process. Requires Manager/Admin role.
//	@Description	When draft=true, the process can be updated (overwritten).
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string							true	"Process ID"
//	@Param			request		body		apicommon.CreateProcessRequest	true	"Process creation information"
//	@Success		200			{string}	string							"OK"
//	@Failure		400			{object}	errors.Error					"Invalid input data"
//	@Failure		401			{object}	errors.Error					"Unauthorized"
//	@Failure		404			{object}	errors.Error					"Published census not found"
//	@Failure		409			{object}	errors.Error					"Process already exists"
//	@Failure		500			{object}	errors.Error					"Internal server error"
//	@Router			/process [post]
func (a *API) createProcessHandler(w http.ResponseWriter, r *http.Request) {
	// parse the process info from the request body
	processInfo := &apicommon.CreateProcessRequest{}
	if err := json.NewDecoder(r.Body).Decode(&processInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}

	if processInfo.CensusID == nil {
		errors.ErrMalformedBody.Withf("missing published census root or ID").Write(w)
		return
	}

	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	census, err := a.db.Census(processInfo.CensusID.String())
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("census not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(census.OrgAddress, db.ManagerRole) && !user.HasRoleFor(census.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	// check if the process exists
	existingProcess, err := a.db.Process(processID)
	if err == nil {
		// Process exists, check if it's in draft mode and can be overwritten
		if !existingProcess.Draft {
			errors.ErrDuplicateConflict.Withf("process already exists and is not in draft mode").Write(w)
			return
		}

		// Check if the user has permission to modify this process
		if !user.HasRoleFor(existingProcess.OrgAddress, db.ManagerRole) && !user.HasRoleFor(existingProcess.OrgAddress, db.AdminRole) {
			errors.ErrUnauthorized.Withf("user is not admin or manager of the organization that owns this process").Write(w)
			return
		}
	} else if err != db.ErrNotFound {
		// Some other error occurred
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// Create or update the process
	process := &db.Process{
		Census:     *census,
		Metadata:   processInfo.Metadata,
		OrgAddress: census.OrgAddress,
		Draft:      processInfo.Draft,
	}
	if len(processInfo.Address) > 0 {
		process.Address = processInfo.Address
	}

	processID, err := a.db.SetProcess(process)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, processID)
}

// processInfoHandler godoc
//
//	@Summary		Get process information
//	@Description	Retrieve voting process information by ID. Returns process details including census and metadata.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Param			processId	path		string	true	"Process ID"
//	@Success		200			{object}	db.Process
//	@Failure		400			{object}	errors.Error	"Invalid process ID"
//	@Failure		404			{object}	errors.Error	"Process not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/process/{processId} [get]
func (a *API) processInfoHandler(w http.ResponseWriter, r *http.Request) {
	processID := chi.URLParam(r, "processId")
	if len(processID) == 0 {
		errors.ErrMalformedURLParam.Withf("missing process ID").Write(w)
		return
	}

	process, err := a.db.Process(processID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("process not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, process)
}

// organizationListProcessDraftsHandler godoc
//
//	@Summary		Get paginated list of process drafts
//	@Description	Returns a list of voting process drafts.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Success		200	{object}	apicommon.ListOrganizationProcesses
//	@Failure		404	{object}	errors.Error	"Process not found"
//	@Failure		500	{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{address}/processes/drafts [get]
func (a *API) organizationListProcessDraftsHandler(w http.ResponseWriter, r *http.Request) {
	// get the organization info from the request context
	org, _, ok := a.organizationFromRequest(r)
	if !ok {
		errors.ErrNoOrganizationProvided.Write(w)
		return
	}
	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// check the user has the necessary permissions
	if !user.HasRoleFor(org.Address, db.ManagerRole) && !user.HasRoleFor(org.Address, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	// Parse pagination parameters from query string
	page := 1      // Default page number
	pageSize := 10 // Default page size

	if pageStr := r.URL.Query().Get("page"); pageStr != "" {
		if pageVal, err := strconv.Atoi(pageStr); err == nil && pageVal > 0 {
			page = pageVal
		}
	}

	if pageSizeStr := r.URL.Query().Get("pageSize"); pageSizeStr != "" {
		if pageSizeVal, err := strconv.Atoi(pageSizeStr); err == nil && pageSizeVal >= 0 {
			pageSize = pageSizeVal
		}
	}

	// retrieve the orgMembers with pagination
	pages, processes, err := a.db.ListProcesses(org.Address, page, pageSize, true)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not get processes: %v", err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, &apicommon.ListOrganizationProcesses{
		TotalPages:  pages,
		CurrentPage: page,
		Processes:   processes,
	})
}
