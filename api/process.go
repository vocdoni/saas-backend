package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
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
	processID := internal.HexBytes{}
	if err := processID.ParseString(chi.URLParam(r, "processId")); err != nil {
		errors.ErrMalformedURLParam.Withf("missing process ID").Write(w)
		return
	}

	processInfo := &apicommon.CreateProcessRequest{}
	if err := json.NewDecoder(r.Body).Decode(&processInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}

	if processInfo.PublishedCensusRoot == nil || processInfo.CensusID == nil {
		errors.ErrMalformedBody.Withf("missing published census root or ID").Write(w)
		return
	}

	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	pubCensus, err := a.db.PublishedCensus(
		processInfo.PublishedCensusRoot.String(),
		processInfo.PublishedCensusURI,
		processInfo.CensusID.String(),
	)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("published census not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(pubCensus.Census.OrgAddress, db.ManagerRole) && !user.HasRoleFor(pubCensus.Census.OrgAddress, db.AdminRole) {
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
		ID:              processID,
		PublishedCensus: *pubCensus,
		Metadata:        processInfo.Metadata,
		OrgAddress:      pubCensus.Census.OrgAddress,
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
	processID := chi.URLParam(r, "processId")
	if len(processID) == 0 {
		errors.ErrMalformedURLParam.Withf("missing process ID").Write(w)
		return
	}

	process, err := a.db.Process([]byte(processID))
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
