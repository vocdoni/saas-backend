package api

import (
	"encoding/json"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// createProcessHandler godoc
//
//	@Summary		Create a new voting process
//	@Description	Create a new voting process. Requires Manager/Admin role.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.CreateProcessRequest	true	"Process creation information"
//	@Success		200		{object}	primitive.ObjectID				"Process ID"
//	@Failure		400		{object}	errors.Error					"Invalid input data"
//	@Failure		401		{object}	errors.Error					"Unauthorized"
//	@Failure		404		{object}	errors.Error					"Published census not found"
//	@Failure		409		{object}	errors.Error					"Process already exists"
//	@Failure		500		{object}	errors.Error					"Internal server error"
//	@Router			/process [post]
func (a *API) createProcessHandler(w http.ResponseWriter, r *http.Request) {
	// parse the process info from the request body
	processInfo := &apicommon.CreateProcessRequest{}
	if err := json.NewDecoder(r.Body).Decode(&processInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}

	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	// if it's a draft process
	if processInfo.Address.Equals(nil) && processInfo.OrgAddress == (common.Address{}) {
		errors.ErrMalformedBody.Withf("draft processes must provide an org address").Write(w)
		return
	}

	// Create or update the process
	process := &db.Process{
		Metadata: processInfo.Metadata,
	}

	var orgAddress common.Address
	if processInfo.CensusID != nil {
		var err error
		census, err := a.db.Census(processInfo.CensusID.String())
		if err != nil {
			if err == db.ErrNotFound {
				errors.ErrMalformedURLParam.Withf("invalid census provided").Write(w)
				return
			}
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		process.Census = *census
		orgAddress = census.OrgAddress
	} else if processInfo.OrgAddress != (common.Address{}) {
		orgAddress = processInfo.OrgAddress
	} else {
		errors.ErrMalformedBody.Withf("either census ID or organization address must be provided").Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(orgAddress, db.ManagerRole) && !user.HasRoleFor(orgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	process.OrgAddress = orgAddress
	if !processInfo.Address.Equals(nil) {
		process.Address = processInfo.Address
	}

	processID, err := a.db.SetProcess(process)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, processID)
}

// updateProcessHandler godoc
//
//	@Summary		Update an existing voting process
//	@Description	Update an existing voting process. Requires Manager/Admin role.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string							true	"Process ID"
//	@Param			request		body		apicommon.CreateProcessRequest	true	"Process update information"
//	@Success		200			{string}	string							"OK"
//	@Failure		400			{object}	errors.Error					"Invalid input data"
//	@Failure		401			{object}	errors.Error					"Unauthorized"
//	@Failure		404			{object}	errors.Error					"Process not found"
//	@Failure		500			{object}	errors.Error					"Internal server error"
//	@Router			/process/{processId} [put]
func (a *API) updateProcessHandler(w http.ResponseWriter, r *http.Request) {
	processID := chi.URLParam(r, "processId")
	if processID == "" {
		errors.ErrMalformedURLParam.Withf("missing process ID").Write(w)
		return
	}
	parsedID, err := primitive.ObjectIDFromHex(processID)
	if err != nil {
		errors.ErrMalformedURLParam.Withf("invalid process ID").Write(w)
		return
	}

	// parse the process info from the request body
	processInfo := &apicommon.UpdateProcessRequest{}
	if err := json.NewDecoder(r.Body).Decode(&processInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}

	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	existingProcess, err := a.db.Process(parsedID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrProcessNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// check if it's a draft process and can be overwritten
	if !existingProcess.Address.Equals(nil) {
		errors.ErrDuplicateConflict.Withf("process already exists and is not in draft mode").Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(existingProcess.OrgAddress, db.ManagerRole) &&
		!user.HasRoleFor(existingProcess.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of the organization that owns this process").Write(w)
		return
	}

	var census *db.Census
	if !processInfo.CensusID.Equals(nil) {
		census, err = a.db.Census(processInfo.CensusID.String())
		if err != nil {
			if err == db.ErrNotFound {
				errors.ErrMalformedURLParam.Withf("census not found").Write(w)
				return
			}
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		existingProcess.Census = *census
	}

	if len(processInfo.Metadata) > 0 {
		existingProcess.Metadata = processInfo.Metadata
	}

	if !processInfo.Address.Equals(nil) {
		existingProcess.Address = processInfo.Address
	}

	_, err = a.db.SetProcess(existingProcess)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteOK(w)
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
	parsedID, err := primitive.ObjectIDFromHex(processID)
	if err != nil {
		errors.ErrMalformedURLParam.Withf("invalid process ID").Write(w)
		return
	}

	process, err := a.db.Process(parsedID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrProcessNotFound.Write(w)
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

	params, err := parsePaginationParams(r.URL.Query().Get(ParamPage), r.URL.Query().Get(ParamLimit))
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}

	// retrieve the orgMembers with pagination
	totalItems, processes, err := a.db.ListProcesses(org.Address, params.Page, params.Limit, db.DraftOnly)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not get processes: %v", err).Write(w)
		return
	}
	pagination, err := calculatePagination(params.Page, params.Limit, totalItems)
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, &apicommon.ListOrganizationProcesses{
		Pagination: pagination,
		Processes:  processes,
	})
}

// deleteProcessHandler godoc
//
//	@Summary		Delete a voting process
//	@Description	Delete a voting process. Requires Manager/Admin role.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string			true	"Process ID"
//	@Success		200			{string}	string			"OK"
//	@Failure		400			{object}	errors.Error	"Invalid process ID"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		404			{object}	errors.Error	"Process not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/process/{processId} [delete]
func (a *API) deleteProcessHandler(w http.ResponseWriter, r *http.Request) {
	processID := chi.URLParam(r, "processId")
	if processID == "" {
		errors.ErrMalformedURLParam.Withf("missing process ID").Write(w)
		return
	}
	parsedID, err := primitive.ObjectIDFromHex(processID)
	if err != nil {
		errors.ErrMalformedURLParam.Withf("invalid process ID").Write(w)
		return
	}

	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	existingProcess, err := a.db.Process(parsedID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrProcessNotFound.Withf("process not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(existingProcess.OrgAddress, db.ManagerRole) &&
		!user.HasRoleFor(existingProcess.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of the organization that owns this process").Write(w)
		return
	}

	err = a.db.DelProcess(parsedID)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteOK(w)
}
