package api

import (
	"encoding/hex"
	"encoding/json"
	stderrors "errors"
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
//	@Description	Create a new process bundle linking a census to an optional list of on-chain processes,
//	@Description	used by the CSP voter flow. Requires Manager/Admin role for the organization that owns
//	@Description	the census. The census root is the CSP (account) public key.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `voting:write`).
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.CreateProcessBundleRequest	true	"Process bundle creation information"
//	@Success		200		{object}	apicommon.CreateProcessBundleResponse
//	@Failure		400		{object}	errors.Error	"Invalid input data, or census/organization not found"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
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

	org, err := a.db.Organization(census.OrgAddress)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrInvalidCensusData.Withf("organization not found").Write(w)
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
		apicommon.HTTPWriteJSON(w, apicommon.CreateProcessBundleResponse{
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
		OrgAddress: census.OrgAddress,
		Census:     *census,
	}

	// get organization metadata from the vochain
	if meta, err := a.client.AccountMetadata(census.OrgAddress.String()); err == nil {
		org.Meta["name"], org.Meta["logo"] = db.ParseVochainOrganizationMeta(meta)
	}
	if err := a.db.SetOrganization(org); err != nil {
		errors.ErrGenericInternalServerError.
			Withf("tried to update update organization name and logo but failed: %v", err).
			Write(w)
		return
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
	apicommon.HTTPWriteJSON(w, apicommon.CreateProcessBundleResponse{
		URI:  a.serverURL + "/process/bundle/" + bundleID.Hex(),
		Root: rootHex,
	})
}

// updateProcessBundleHandler godoc
//
//	@Summary		Add processes to an existing bundle
//	@Description	Add additional on-chain processes to an existing bundle. Requires Manager/Admin role for
//	@Description	the organization that owns the bundle. An empty process list is a no-op that returns the
//	@Description	bundle's current root.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `voting:write`).
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			bundleId	path		string						true	"Bundle ID"
//	@Param			request		body		AddProcessesToBundleRequest	true	"Processes to add"
//	@Success		200			{object}	apicommon.CreateProcessBundleResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data, or bundle not found"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
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
		apicommon.HTTPWriteJSON(w, apicommon.CreateProcessBundleResponse{
			URI:  "/process/bundle/" + bundleIDStr,
			Root: bundle.Census.Published.Root,
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

	apicommon.HTTPWriteJSON(w, apicommon.CreateProcessBundleResponse{
		URI:  "/process/bundle/" + bundleIDStr,
		Root: bundle.Census.Published.Root,
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
//	@Success		200			{object}	apicommon.ProcessBundleInfo
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

	apicommon.HTTPWriteJSON(w, &apicommon.ProcessBundleInfo{
		ProcessesBundle: bundle,
		ChainID:         a.account.ChainID(),
	})
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
//	@Param			participantID	path		string	true	"Participant ID"
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

	apicommon.HTTPWriteJSON(w, nil)
}

// checkProcessBundleVotedParticipantsHandler godoc
//
//	@Summary		Check whether an org member belongs to a bundle's census and have voted
//	@Description	Look up org members by one of the allowed identification fields
//	@Description	(email, phone, memberNumber, nationalId, name, surname) and return
//	@Description	The request must also include the processID
//	@Description	hasVoted is true when the member has used (consumed) that process to
//	@Description	cast a ballot. Requires Manager/Admin role on the bundle's organization.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			bundleId	path		string										true	"Bundle ID"
//	@Param			request		body		apicommon.CheckBundleParticipantsRequest	true	"Lookup request"
//	@Success		200			{object}	apicommon.CheckBundleParticipantsResponse
//	@Failure		400			{object}	errors.Error	"Invalid input"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		403			{object}	errors.Error	"Forbidden"
//	@Failure		404			{object}	errors.Error	"Bundle not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/process/bundle/{bundleId}/participants/check [post]
func (a *API) checkProcessBundleVotedParticipantsHandler(w http.ResponseWriter, r *http.Request) {
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

	var req apicommon.CheckBundleParticipantsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	field := db.OrgMemberLookupField(req.FieldName)
	if !field.IsValid() {
		errors.ErrMalformedBody.Withf(
			"invalid fieldName: must be one of email, phone, memberNumber, nationalId",
		).Write(w)
		return
	}
	if req.Value == "" {
		errors.ErrMalformedBody.Withf("missing value").Write(w)
		return
	}
	if len(req.ProcessID) == 0 {
		errors.ErrMalformedBody.Withf("missing processID").Write(w)
		return
	}

	bundle, err := a.db.ProcessBundle(bundleID)
	if err != nil {
		if stderrors.Is(err, db.ErrNotFound) {
			errors.ErrBundleNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	if !user.HasRoleFor(bundle.OrgAddress, db.ManagerRole) &&
		!user.HasRoleFor(bundle.OrgAddress, db.AdminRole) {
		errors.ErrForbidden.Withf("user is not admin or manager of organization").Write(w)
		return
	}

	// Build the lookup value. Phone is hashed server-side because OrgMember.Phone
	// is stored as a HashedPhone.
	var lookupValue any = req.Value
	if field == db.OrgMemberLookupFieldPhone {
		org, err := a.db.Organization(bundle.OrgAddress)
		if err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		hashed, err := db.NewHashedPhone(req.Value, org)
		if err != nil {
			errors.ErrMalformedBody.Withf("invalid phone: %v", err).Write(w)
			return
		}
		if hashed.IsEmpty() {
			errors.ErrMalformedBody.Withf("invalid phone").Write(w)
			return
		}
		lookupValue = hashed
	}

	members, err := a.db.OrgMembersByField(bundle.OrgAddress, field, lookupValue)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if len(members) == 0 {
		apicommon.HTTPWriteJSON(w, apicommon.CheckBundleParticipantsResponse{
			Participants: []apicommon.CheckBundleParticipantsResponseEntry{},
		})
		return
	}

	memberIDs := make([]string, 0, len(members))
	membersByID := make(map[string]*db.OrgMember, len(members))
	for _, m := range members {
		id := m.ID.Hex()
		memberIDs = append(memberIDs, id)
		membersByID[id] = m
	}

	censusID := bundle.Census.ID.Hex()
	participants, err := a.db.CensusParticipantsByMemberIDs(censusID, memberIDs)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// Look up vote status for the requested process. A used CSP process for a
	// member indicates they have consumed the process to cast a ballot.
	// participantIDs are the member IDs that are included in the census.
	participantIDs := make([]string, 0, len(participants))
	for _, p := range participants {
		participantIDs = append(participantIDs, p.ParticipantID)
	}
	votedSet, err := a.db.MembersWithUsedCSPProcess(req.ProcessID, participantIDs)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	entries := make([]apicommon.CheckBundleParticipantsResponseEntry, 0, len(participants))
	for _, p := range participants {
		m, ok := membersByID[p.ParticipantID]
		if !ok {
			continue
		}
		entries = append(entries, apicommon.CheckBundleParticipantsResponseEntry{
			MemberID:     m.ID.Hex(),
			Name:         m.Name,
			Surname:      m.Surname,
			Email:        m.Email,
			MemberNumber: m.MemberNumber,
			HasVoted:     votedSet[m.ID.Hex()],
		})
	}

	apicommon.HTTPWriteJSON(w, apicommon.CheckBundleParticipantsResponse{Participants: entries})
}
