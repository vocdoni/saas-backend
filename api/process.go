package api

import (
	"encoding/json"
	"net/http"

	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// createProcessHandler godoc
//
//	@Summary		Create a new voting process
//	@Description	Create a new voting process. Requires Manager/Admin role.
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
//	@Router			/process/{processId} [post]
func (a *API) createProcessHandler(w http.ResponseWriter, r *http.Request) {
	processID, err := apicommon.ProcessIDFromRequest(r)
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}

	processInfo := &apicommon.CreateProcessRequest{}
	if err := json.NewDecoder(r.Body).Decode(&processInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}

	if processInfo.PublishedCensusRoot == nil || processInfo.CensusID.IsZero() {
		errors.ErrMalformedBody.Withf("missing published census root or ID").Write(w)
		return
	}

	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	census, err := a.db.Census(processInfo.CensusID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("census not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	if processInfo.PublishedCensusRoot.String() != census.Published.Root.String() ||
		processInfo.PublishedCensusURI != census.Published.URI {
		errors.ErrMalformedBody.Withf("published census root or URI does not match census").Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(census.OrgAddress, db.ManagerRole) && !user.HasRoleFor(census.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	// check that the process does not exist
	if _, err := a.db.Process(processID); err == nil {
		errors.ErrDuplicateConflict.Withf("process already exists").Write(w)
		return
	}

	// finally create the process
	process := &db.Process{
		ID:         processID,
		Census:     *census,
		Metadata:   processInfo.Metadata,
		OrgAddress: census.OrgAddress,
	}

	if err := a.db.SetProcess(process); err != nil {
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
	processID, err := apicommon.ProcessIDFromRequest(r)
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
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
