package api

import (
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/util"
)

// Using types from apicommon package

// AddProcessesToBundleRequest represents the request body for adding processes to an existing bundle.
// It contains an array of process IDs to add to the bundle.
type AddProcessesToBundleRequest struct {
	Processes []string `json:"processes"` // Array of process creation requests to add
}

// createProcessBundleHandler godoc
//
//	@Summary		Create a new process bundle
//	@Description	Create a new process bundle with the specified census and optional list of processes. Requires
//	@Description	Manager/Admin role for the organization that owns the census. The census root will be the same as the
//	@Description	account's public key.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.CreateProcessBundleRequest	true	"Process bundle creation information"
//	@Success		200		{object}	apicommon.CreateProcessBundleResponse
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		404		{object}	errors.Error	"Census not found"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/process/bundle [post]
func (a *API) createProcessBundleHandler(w http.ResponseWriter, r *http.Request) {
	var req apicommon.CreateProcessBundleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}

	census, err := a.db.Census(req.CensusID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("census not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// Get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	// Check if the user has the necessary permissions for the organization
	if !user.HasRoleFor(census.OrgAddress, db.ManagerRole) && !user.HasRoleFor(census.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of organization").Write(w)
		return
	}

	// generate a new bundle ID
	bundleID := a.db.NewBundleID()
	// The cenus root will be the same as the account's public key
	censusRoot, err := a.csp.PubKey()
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("failed to get CSP public key").Write(w)
		return
	}

	if len(req.Processes) == 0 {
		// Create the process bundle
		bundle := &db.ProcessesBundle{
			ID:         bundleID,
			CensusRoot: censusRoot.String(),
			OrgAddress: census.OrgAddress,
			Census:     *census,
		}
		_, err = a.db.SetProcessBundle(bundle)
		if err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}

		var rootHex internal.HexBytes
		if err := rootHex.ParseString(censusRoot.String()); err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		apicommon.HttpWriteJSON(w, apicommon.CreateProcessBundleResponse{
			URI:  a.serverURL + "/process/bundle/" + bundleID.Hex(),
			Root: rootHex,
		})
		return
	}

	// Collect all processes
	var processes []internal.HexBytes

	for _, processReq := range req.Processes {
		if len(processReq) == 0 {
			errors.ErrMalformedBody.Withf("missing process ID").Write(w)
			return
		}
		processID, err := hex.DecodeString(util.TrimHex(processReq))
		if err != nil {
			errors.ErrMalformedBody.Withf("invalid process ID").Write(w)
			return
		}

		processes = append(processes, processID)
	}

	// Create the process bundle
	cspPubKey, err := a.csp.PubKey()
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("failed to get CSP public key").Write(w)
		return
	}

	bundle := &db.ProcessesBundle{
		ID:         bundleID,
		Processes:  processes,
		CensusRoot: cspPubKey.String(),
		OrgAddress: census.OrgAddress,
		Census:     *census,
	}

	_, err = a.db.SetProcessBundle(bundle)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	var rootHex internal.HexBytes
	if err := rootHex.ParseString(cspPubKey.String()); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	apicommon.HttpWriteJSON(w, apicommon.CreateProcessBundleResponse{
		URI:  a.serverURL + "/process/bundle/" + bundleID.Hex(),
		Root: rootHex,
	})
}

// updateProcessBundleHandler godoc
//
//	@Summary		Add processes to an existing bundle
//	@Description	Add additional processes to an existing bundle. Requires Manager/Admin role for the organization
//	@Description	that owns the bundle.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			bundleId	path		string						true	"Bundle ID"
//	@Param			request		body		AddProcessesToBundleRequest	true	"Processes to add"
//	@Success		200			{object}	apicommon.CreateProcessBundleResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		404			{object}	errors.Error	"Bundle or census not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/process/bundle/{bundleId} [put]
func (a *API) updateProcessBundleHandler(w http.ResponseWriter, r *http.Request) {
	bundleIDStr := chi.URLParam(r, "bundleId")
	if bundleIDStr == "" {
		errors.ErrMalformedURLParam.Withf("missing bundle ID").Write(w)
		return
	}

	var bundleID internal.HexBytes
	if err := bundleID.ParseString(bundleIDStr); err != nil {
		errors.ErrMalformedURLParam.Withf("invalid bundle ID").Write(w)
		return
	}

	var req AddProcessesToBundleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}

	// Get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	// Get the existing bundle
	bundle, err := a.db.ProcessBundle(bundleID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("bundle not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	if len(req.Processes) == 0 {
		var rootHex internal.HexBytes
		if err := rootHex.ParseString(bundle.CensusRoot); err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		apicommon.HttpWriteJSON(w, apicommon.CreateProcessBundleResponse{
			URI:  "/process/bundle/" + bundleIDStr,
			Root: rootHex,
		})
		return
	}

	// Check if the user has the necessary permissions for the organization
	if !user.HasRoleFor(bundle.OrgAddress, db.ManagerRole) && !user.HasRoleFor(bundle.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of organization").Write(w)
		return
	}

	// Collect all processes to add
	var processesToAdd []internal.HexBytes

	for _, processReq := range req.Processes {
		if len(processReq) == 0 {
			errors.ErrMalformedBody.Withf("missing process ID").Write(w)
			return
		}
		processID, err := hex.DecodeString(util.TrimHex(processReq))
		if err != nil {
			errors.ErrMalformedBody.Withf("invalid process ID").Write(w)
			return
		}

		processesToAdd = append(processesToAdd, processID)
	}

	// Add processes to the bundle
	if err := a.db.AddProcessesToBundle(bundleID, processesToAdd); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	var rootHex internal.HexBytes
	if err := rootHex.ParseString(bundle.CensusRoot); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	apicommon.HttpWriteJSON(w, apicommon.CreateProcessBundleResponse{
		URI:  "/process/bundle/" + bundleIDStr,
		Root: rootHex,
	})
}

// processBundleInfoHandler godoc
//
//	@Summary		Get process bundle information
//	@Description	Retrieve process bundle information by ID. Returns bundle details including the associated census,
//	@Description	census root, organization address, and list of processes.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Param			bundleId	path		string	true	"Bundle ID"
//	@Success		200			{object}	db.ProcessesBundle
//	@Failure		400			{object}	errors.Error	"Invalid bundle ID"
//	@Failure		404			{object}	errors.Error	"Bundle not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/process/bundle/{bundleId} [get]
func (a *API) processBundleInfoHandler(w http.ResponseWriter, r *http.Request) {
	bundleIDStr := chi.URLParam(r, "bundleId")
	if bundleIDStr == "" {
		errors.ErrMalformedURLParam.Withf("missing bundle ID").Write(w)
		return
	}

	var bundleID internal.HexBytes
	if err := bundleID.ParseString(bundleIDStr); err != nil {
		errors.ErrMalformedURLParam.Withf("invalid bundle ID").Write(w)
		return
	}

	bundle, err := a.db.ProcessBundle(bundleID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("bundle not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	apicommon.HttpWriteJSON(w, bundle)
}

// processBundleParticipantInfoHandler godoc
//
//	@Summary		Get participant information for a process bundle
//	@Description	Retrieve process information for a participant in a process bundle. Returns process details including
//	@Description	the census and metadata.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Param			bundleId		path		string	true	"Bundle ID"
//	@Param			participantId	path		string	true	"Participant ID"
//	@Success		200				{object}	interface{}
//	@Failure		400				{object}	errors.Error	"Invalid bundle ID or participant ID"
//	@Failure		404				{object}	errors.Error	"Bundle not found"
//	@Failure		500				{object}	errors.Error	"Internal server error"
//	@Router			/process/bundle/{bundleId}/{participantId} [get]
func (a *API) processBundleParticipantInfoHandler(w http.ResponseWriter, r *http.Request) {
	bundleIDStr := chi.URLParam(r, "bundleId")
	if bundleIDStr == "" {
		errors.ErrMalformedURLParam.Withf("missing bundle ID").Write(w)
		return
	}

	var bundleID internal.HexBytes
	if err := bundleID.ParseString(bundleIDStr); err != nil {
		errors.ErrMalformedURLParam.Withf("invalid bundle ID").Write(w)
		return
	}

	_, err := a.db.ProcessBundle(bundleID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("bundle not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	participantID := chi.URLParam(r, "participantId")
	if participantID == "" {
		errors.ErrMalformedURLParam.Withf("missing participant ID").Write(w)
		return
	}

	// TODO
	/*	elections := a.csp.Indexer(participantID, bundleIDStr, "")
		if len(elections) == 0 {
			httpWriteJSON(w, []twofactor.Election{})
			return
		}
	*/

	apicommon.HttpWriteJSON(w, nil)
}
