package api

import (
	"encoding/json"
	"net/http"
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

const (
	// CensusTypeSMSOrMail is the CSP based type of census that supports both SMS and mail.
	CensusTypeSMSOrMail = "sms_or_mail"
	CensusTypeMail      = "mail"
	CensusTypeSMS       = "sms"
)

// addParticipantsToCensusWorkers is a map of job identifiers to the progress of adding participants to a census.
// This is used to check the progress of the job.
var addParticipantsToCensusWorkers sync.Map

// createCensusHandler godoc
//
//	@Summary		Create a new census
//	@Description	Create a new census for an organization. Requires Manager/Admin role.
//	@Description	Creates either a regular census or a group-based census if GroupID is provided.
//	@Description	Validates that either AuthFields or TwoFaFields are provided and checks for duplicates or empty fields.
//	@Tags			census
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.CreateCensusRequest	true	"Census information"
//	@Success		200		{object}	apicommon.CreateCensusResponse	"Returns the created census ID"
//	@Failure		400		{object}	errors.Error					"Invalid input data or missing required fields"
//	@Failure		401		{object}	errors.Error					"Unauthorized"
//	@Failure		500		{object}	errors.Error					"Internal server error"
//	@Router			/census [post]
func (a *API) createCensusHandler(w http.ResponseWriter, r *http.Request) {
	// Parse request
	censusInfo := &apicommon.CreateCensusRequest{}
	if err := json.NewDecoder(r.Body).Decode(&censusInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}

	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(censusInfo.OrgAddress, db.ManagerRole) && !user.HasRoleFor(censusInfo.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user does not have the necessary permissions in the organization").Write(w)
		return
	}

	census := &db.Census{
		OrgAddress:  censusInfo.OrgAddress,
		AuthFields:  censusInfo.AuthFields,
		TwoFaFields: censusInfo.TwoFaFields,
		CreatedAt:   time.Now(),
	}

	// In the regular census, members will be added later so we just create the DB entry
	censusID, err := a.db.SetCensus(census)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, apicommon.CreateCensusResponse{
		ID: censusID,
	})
}

// censusInfoHandler godoc
//
//	@Summary		Get census information
//	@Description	Retrieve census information by ID. Returns census type, organization address, and creation time.
//	@Tags			census
//	@Accept			json
//	@Produce		json
//	@Param			id	path		string	true	"Census ID"
//	@Success		200	{object}	apicommon.OrganizationCensus
//	@Failure		400	{object}	errors.Error	"Invalid census ID"
//	@Failure		404	{object}	errors.Error	"Census not found"
//	@Failure		500	{object}	errors.Error	"Internal server error"
//	@Router			/census/{id} [get]
func (a *API) censusInfoHandler(w http.ResponseWriter, r *http.Request) {
	censusID := internal.HexBytes{}
	if err := censusID.ParseString(chi.URLParam(r, "id")); err != nil {
		errors.ErrMalformedURLParam.Withf("wrong census ID").Write(w)
		return
	}
	census, err := a.db.Census(censusID.String())
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	apicommon.HTTPWriteJSON(w, apicommon.OrganizationCensusFromDB(census))
}

// addCensusParticipantsHandler godoc
//
//	@Summary		Add participants to a census
//	@Description	Add multiple participants to a census. Requires Manager/Admin role.
//	@Tags			census
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string						true	"Census ID"
//	@Param			async	query		boolean						false	"Process asynchronously and return job ID"
//	@Param			request	body		apicommon.AddMembersRequest	true	"Participants to add"
//	@Success		200		{object}	apicommon.AddMembersResponse
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		404		{object}	errors.Error	"Census not found"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/census/{id} [post]
func (a *API) addCensusParticipantsHandler(w http.ResponseWriter, r *http.Request) {
	censusID := internal.HexBytes{}
	if err := censusID.ParseString(chi.URLParam(r, "id")); err != nil {
		errors.ErrMalformedURLParam.Withf("wrong census ID").Write(w)
		return
	}
	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// get the async flag
	async := r.URL.Query().Get("async") == "true" //nolint:goconst

	// retrieve census
	census, err := a.db.Census(censusID.String())
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

	// a non-group-based census cannot be modified once published
	if census.GroupID.IsZero() && len(census.Published.Root) > 0 {
		errors.ErrCensusAlreadyPublished.Write(w)
		return
	}

	// decode the participants from the request body
	members := &apicommon.AddMembersRequest{}
	if err := json.NewDecoder(r.Body).Decode(members); err != nil {
		log.Error(err)
		errors.ErrMalformedBody.Withf("missing participants").Write(w)
		return
	}
	// check if there are participants to add
	if len(members.Members) == 0 {
		apicommon.HTTPWriteJSON(w, &apicommon.AddMembersResponse{Added: 0})
		return
	}
	org, err := a.db.Organization(census.OrgAddress)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// add the org members as census participants in the database
	progressChan, err := a.db.SetBulkCensusOrgMemberParticipant(
		org,
		passwordSalt,
		censusID.String(),
		members.ToDB(),
	)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	if !async {
		// Wait for the channel to be closed (100% completion)
		var lastProgress *db.BulkCensusParticipantStatus
		for p := range progressChan {
			lastProgress = p
			// Just drain the channel until it's closed
			log.Debugw("census add participants",
				"census", censusID.String(),
				"org", census.OrgAddress,
				"progress", p.Progress,
				"added", p.Added,
				"total", p.Total)
		}
		// Return the number of participants added
		apicommon.HTTPWriteJSON(w, &apicommon.AddMembersResponse{Added: uint32(lastProgress.Added)})
		return
	}

	// if async create a new job identifier
	jobID := internal.HexBytes(util.RandomBytes(16))

	// Create persistent job record
	if err := a.db.CreateJob(jobID.String(), db.JobTypeCensusParticipants, census.OrgAddress, len(members.Members)); err != nil {
		log.Warnw("failed to create persistent job record", "error", err, "jobId", jobID.String())
		// Continue with in-memory only (fallback)
	}

	go func() {
		for p := range progressChan {
			// We need to drain the channel to avoid blocking
			addParticipantsToCensusWorkers.Store(jobID.String(), p)

			// When job completes, persist final results
			if p.Progress == 100 {
				// we pass CompleteJob an empty errors slice, because SetBulkCensusOrgMemberParticipant
				// doesn't collect errors, it only reports progress over the channel.
				if err := a.db.CompleteJob(jobID.String(), p.Added, []string{}); err != nil {
					log.Warnw("failed to persist job completion", "error", err, "jobId", jobID.String())
				}
				addParticipantsToCensusWorkers.Delete(jobID.String())
			}
		}
	}()

	apicommon.HTTPWriteJSON(w, &apicommon.AddMembersResponse{JobID: jobID})
}

// censusAddParticipantsJobStatusHandler godoc
//
//	@Summary		Check the progress of adding participants
//	@Description	Check the progress of a job to add participants to a census. Returns the progress of the job.
//	@Description	If the job is completed, the job is deleted after 60 seconds.
//	@Tags			census
//	@Accept			json
//	@Produce		json
//	@Param			jobid	path		string	true	"Job ID"
//	@Success		200		{object}	db.BulkCensusParticipantStatus
//	@Failure		400		{object}	errors.Error	"Invalid job ID"
//	@Failure		404		{object}	errors.Error	"Job not found"
//	@Router			/census/job/{jobid} [get]
func (a *API) censusAddParticipantsJobStatusHandler(w http.ResponseWriter, r *http.Request) {
	jobID := internal.HexBytes{}
	if err := jobID.ParseString(chi.URLParam(r, "jobid")); err != nil {
		errors.ErrMalformedURLParam.Withf("invalid job ID").Write(w)
		return
	}

	// First check in-memory for active jobs
	if v, ok := addParticipantsToCensusWorkers.Load(jobID.String()); ok {
		p, ok := v.(*db.BulkCensusParticipantStatus)
		if !ok {
			errors.ErrGenericInternalServerError.Withf("invalid job status type").Write(w)
			return
		}
		apicommon.HTTPWriteJSON(w, p)
		return
	}

	// If not in memory, check database for completed jobs
	job, err := a.db.Job(jobID.String())
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrJobNotFound.Withf("%s", jobID.String()).Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Withf("failed to get job: %v", err).Write(w)
		return
	}

	// Return persistent job data in the same format as BulkCensusParticipantStatus
	apicommon.HTTPWriteJSON(w, &db.BulkCensusParticipantStatus{
		Progress: 100, // Completed jobs are always 100%
		Total:    job.Total,
		Added:    job.Added,
	})
}

// publishCensusHandler godoc
//
//	@Summary		Publish a census for voting
//	@Description	Publish a census for voting. Requires Manager/Admin role. Returns published census with credentials.
//	@Tags			census
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Census ID"
//	@Success		200	{object}	apicommon.PublishedCensusResponse
//	@Failure		400	{object}	errors.Error	"Invalid census ID"
//	@Failure		401	{object}	errors.Error	"Unauthorized"
//	@Failure		404	{object}	errors.Error	"Census not found"
//	@Failure		500	{object}	errors.Error	"Internal server error"
//	@Router			/census/{id}/publish [post]
func (a *API) publishCensusHandler(w http.ResponseWriter, r *http.Request) {
	censusID := internal.HexBytes{}
	if err := censusID.ParseString(chi.URLParam(r, "id")); err != nil {
		errors.ErrMalformedURLParam.Withf("wrong census ID").Write(w)
		return
	}

	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	// retrieve census
	census, err := a.db.Census(censusID.String())
	if err != nil {
		errors.ErrCensusNotFound.Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(census.OrgAddress, db.ManagerRole) && !user.HasRoleFor(census.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user does not have the necessary permissions in the organization").Write(w)
		return
	}

	if len(census.Published.Root) > 0 {
		// if the census is already published, return the censusInfo
		apicommon.HTTPWriteJSON(w, &apicommon.PublishedCensusResponse{
			URI:  census.Published.URI,
			Root: census.Published.Root,
		})
		return
	}

	// if census.Type == CensusTypeSMSOrMail || census.Type == CenT {
	// build the census and store it
	cspSignerPubKey := a.account.PubKey // TODO: use a different key based on the censusID
	switch census.Type {
	case CensusTypeSMSOrMail, CensusTypeMail, CensusTypeSMS:
		census.Published.Root = cspSignerPubKey
		census.Published.URI = a.serverURL + "/process"
		census.Published.CreatedAt = time.Now()

	default:
		errors.ErrCensusTypeNotFound.Write(w)
		return
	}

	if _, err := a.db.SetCensus(census); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, &apicommon.PublishedCensusResponse{
		URI:  census.Published.URI,
		Root: cspSignerPubKey,
	})
}

// publishCensusGroupHandler godoc
//
//	@Summary		Publish a group-based census for voting
//	@Description	Publish a census based on a specific organization members group for voting. Requires Manager/Admin role.
//	@Description	Returns published census with credentials.
//	@Tags			census
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string								true	"Census ID"
//	@Param			groupId	path		string								true	"Group ID"
//	@Param			request	body		apicommon.PublishCensusGroupRequest	true	"Census authentication configuration"
//	@Success		200		{object}	apicommon.PublishedCensusResponse
//	@Failure		400		{object}	errors.Error	"Invalid census ID or group ID"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		404		{object}	errors.Error	"Census not found"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/census/{id}/publish/group/{groupid} [post]
func (a *API) publishCensusGroupHandler(w http.ResponseWriter, r *http.Request) {
	censusID := internal.HexBytes{}
	if err := censusID.ParseString(chi.URLParam(r, "id")); err != nil {
		errors.ErrMalformedURLParam.Withf("wrong census ID").Write(w)
		return
	}

	groupID := internal.HexBytes{}
	if err := groupID.ParseString(chi.URLParam(r, "groupid")); err != nil {
		errors.ErrMalformedURLParam.Withf("wrong group ID").Write(w)
		return
	}

	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	// retrieve census
	census, err := a.db.Census(censusID.String())
	if err != nil {
		errors.ErrCensusNotFound.Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(census.OrgAddress, db.ManagerRole) && !user.HasRoleFor(census.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user does not have the necessary permissions in the organization").Write(w)
		return
	}

	// Parse request
	publishInfo := &apicommon.PublishCensusGroupRequest{}
	if err := json.NewDecoder(r.Body).Decode(&publishInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	census.AuthFields = publishInfo.AuthFields
	census.TwoFaFields = publishInfo.TwoFaFields

	if len(census.Published.Root) > 0 {
		// if the census is already published, return the censusInfo
		apicommon.HTTPWriteJSON(w, &apicommon.PublishedCensusResponse{
			URI:  census.Published.URI,
			Root: census.Published.Root,
			Size: census.Size,
		})
		return
	}

	if err := a.db.PopulateGroupCensus(census, groupID.String()); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// build the census and store it
	cspSignerPubKey, err := a.csp.PubKey()
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("failed to get CSP public key").Write(w)
		return
	}
	var rootHex internal.HexBytes
	if err := rootHex.ParseString(cspSignerPubKey.String()); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if len(census.TwoFaFields) == 0 && len(census.AuthFields) == 0 {
		// non CSP censuses
		errors.ErrCensusTypeNotFound.Write(w)
		return
	}

	census.Published.Root = rootHex
	census.Published.URI = a.serverURL + "/process"
	census.Published.CreatedAt = time.Now()

	if _, err := a.db.SetCensus(census); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, &apicommon.PublishedCensusResponse{
		URI:  census.Published.URI,
		Root: census.Published.Root,
		Size: census.Size,
	})
}

// censusParticipantsHandler godoc
//
//	@Summary		Get census participants
//	@Description	Retrieve participants of a census by ID. Requires Manager/Admin role.
//	@Tags			census
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Census ID"
//	@Success		200	{object}	apicommon.CensusParticipantsResponse
//	@Failure		400	{object}	errors.Error	"Invalid census ID"
//	@Failure		401	{object}	errors.Error	"Unauthorized"
//	@Failure		404	{object}	errors.Error	"Census not found"
//	@Failure		500	{object}	errors.Error	"Internal server error"
//	@Router			/census/{id}/participants [get]
func (a *API) censusParticipantsHandler(w http.ResponseWriter, r *http.Request) {
	censusID := internal.HexBytes{}
	if err := censusID.ParseString(chi.URLParam(r, "id")); err != nil {
		errors.ErrMalformedURLParam.Withf("wrong census ID").Write(w)
		return
	}

	// retrieve census
	census, err := a.db.Census(censusID.String())
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrCensusNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(census.OrgAddress, db.ManagerRole) && !user.HasRoleFor(census.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user does not have the necessary permissions in the organization").Write(w)
		return
	}

	participants, err := a.db.CensusParticipants(censusID.String())
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	participantMemberIDs := make([]string, len(participants))
	for i, p := range participants {
		participantMemberIDs[i] = p.ParticipantID
	}

	apicommon.HTTPWriteJSON(w, &apicommon.CensusParticipantsResponse{
		CensusID:  censusID.String(),
		MemberIDs: participantMemberIDs,
	})
}
