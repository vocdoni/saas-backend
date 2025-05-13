package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/dvote/util"
)

// addParticipantsToOrgWorkers is a map of job identifiers to the progress of adding participants to a census.
// This is used to check the progress of the job.
var addParticipantsToOrgWorkers sync.Map

// organizationParticipantsHandler godoc
//
//	@Summary		Get organization participants
//	@Description	Retrieve all participants of an organization with pagination support
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address		path		string	true	"Organization address"
//	@Param			page		query		integer	false	"Page number (default: 1)"
//	@Param			pageSize	query		integer	false	"Number of items per page (default: 10)"
//	@Success		200			{object}	apicommon.OrganizationParticipantsResponse
//	@Failure		400			{object}	errors.Error	"Invalid input"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{address}/participants [get]
func (a *API) organizationParticipantsHandler(w http.ResponseWriter, r *http.Request) {
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
		if pageSizeVal, err := strconv.Atoi(pageSizeStr); err == nil && pageSizeVal > 0 {
			pageSize = pageSizeVal
		}
	}

	// retrieve the orgParticipants with pagination
	participants, err := a.db.OrgParticipants(org.Address, page, pageSize)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not get org participants: %v", err).Write(w)
		return
	}

	// convert the orgParticipants to the response format
	participantsResponse := make([]apicommon.OrgParticipant, 0, len(participants))
	for _, p := range participants {
		participantsResponse = append(participantsResponse, apicommon.OrgParticipantFromDb(p))
	}

	apicommon.HTTPWriteJSON(w, &apicommon.OrganizationParticipantsResponse{
		Participants: participantsResponse,
	})
}

// addOrganizationParticipantsHandler godoc
//
//	@Summary		Add participants to an organization
//	@Description	Add multiple participants to an organization. Requires Manager/Admin role.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string								true	"Organization address"
//	@Param			async	query		boolean								false	"Process asynchronously and return job ID"
//	@Param			request	body		apicommon.AddParticipantsRequest	true	"Participants to add"
//	@Success		200		{object}	apicommon.AddParticipantsResponse
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{address}/participants [post]
func (a *API) addOrganizationParticipantsHandler(w http.ResponseWriter, r *http.Request) {
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
	// get the async flag
	async := r.URL.Query().Get("async") == "true"

	// retrieve census
	// check the user has the necessary permissions
	if !user.HasRoleFor(org.Address, db.ManagerRole) && !user.HasRoleFor(org.Address, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}
	// decode the participants from the request body
	participants := &apicommon.AddParticipantsRequest{}
	if err := json.NewDecoder(r.Body).Decode(participants); err != nil {
		log.Error(err)
		errors.ErrMalformedBody.Withf("missing participants").Write(w)
		return
	}
	// check if there are participants to add
	if len(participants.Participants) == 0 {
		apicommon.HTTPWriteJSON(w, &apicommon.AddParticipantsResponse{ParticipantsNo: 0})
		return
	}
	// add the org participants to the database
	progressChan, err := a.db.SetBulkOrgParticipants(
		org.Address,
		passwordSalt,
		participants.DbOrgParticipants(org.Address),
	)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	if !async {
		// Wait for the channel to be closed (100% completion)
		var lastProgress *db.BulkOrgParticipantsStatus
		for p := range progressChan {
			lastProgress = p
			// Just drain the channel until it's closed
			log.Debugw("org add participants",
				"org", org.Address,
				"progress", p.Progress,
				"added", p.Added,
				"total", p.Total)
		}
		// Return the number of participants added
		apicommon.HTTPWriteJSON(w, &apicommon.AddParticipantsResponse{ParticipantsNo: uint32(lastProgress.Added)})
		return
	}

	// if async create a new job identifier
	jobID := internal.HexBytes(util.RandomBytes(16))
	go func() {
		for p := range progressChan {
			// We need to drain the channel to avoid blocking
			addParticipantsToOrgWorkers.Store(jobID.String(), p)
		}
	}()

	apicommon.HTTPWriteJSON(w, &apicommon.AddParticipantsResponse{JobID: jobID})
}

// addOrganizationParticipantsJobCheckHandler godoc
//
//	@Summary		Check the progress of adding participants
//	@Description	Check the progress of a job to add participants to an organization. Returns the progress of the job.
//	@Description	If the job is completed, the job is deleted after 60 seconds.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string	true	"Organization address"
//	@Param			jobid	path		string	true	"Job ID"
//	@Success		200		{object}	db.BulkOrgParticipantsStatus
//	@Failure		400		{object}	errors.Error	"Invalid job ID"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		404		{object}	errors.Error	"Job not found"
//	@Router			/organizations/{address}/participants/check/{jobid} [get]
func (*API) addOrganizationParticipantsJobCheckHandler(w http.ResponseWriter, r *http.Request) {
	jobID := internal.HexBytes{}
	if err := jobID.ParseString(chi.URLParam(r, "jobid")); err != nil {
		errors.ErrMalformedURLParam.Withf("invalid job ID").Write(w)
		return
	}

	if v, ok := addParticipantsToOrgWorkers.Load(jobID.String()); ok {
		p, ok := v.(*db.BulkOrgParticipantsStatus)
		if !ok {
			errors.ErrGenericInternalServerError.Withf("invalid job status type").Write(w)
			return
		}
		if p.Progress == 100 {
			go func() {
				// Schedule the deletion of the job after 60 seconds
				time.Sleep(60 * time.Second)
				addParticipantsToOrgWorkers.Delete(jobID.String())
			}()
		}
		apicommon.HTTPWriteJSON(w, p)
		return
	}

	errors.ErrJobNotFound.Withf("%s", jobID.String()).Write(w)
}

// deleteOrganizationParticipantsHandler godoc
//
//	@Summary		Delete organization participants
//	@Description	Delete multiple participants from an organization. Requires Manager/Admin role.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string								true	"Organization address"
//	@Param			request	body		apicommon.DeleteParticipantsRequest	true	"Participant IDs to delete"
//	@Success		200		{object}	apicommon.DeleteParticipantsResponse
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{address}/participants [delete]
func (a *API) deleteOrganizationParticipantsHandler(w http.ResponseWriter, r *http.Request) {
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
	// get participantIds from the request body
	participants := &apicommon.DeleteParticipantsRequest{}
	if err := json.NewDecoder(r.Body).Decode(participants); err != nil {
		errors.ErrMalformedBody.Withf("error decoding participant IDs").Write(w)
		return
	}
	// check if there are participant IDs to delete
	if len(participants.ParticipantIDs) == 0 {
		apicommon.HTTPWriteJSON(w, &apicommon.DeleteParticipantsResponse{ParticipantsNo: 0})
		return
	}
	// delete the org participants from the database
	deleted, err := a.db.DeleteOrgParticipants(org.Address, participants.ParticipantIDs)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not delete org participants: %v", err).Write(w)
		return
	}
	apicommon.HTTPWriteJSON(w, &apicommon.DeleteParticipantsResponse{ParticipantsNo: deleted})
}
