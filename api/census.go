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
)

// addParticipantsToCensusWorkers is a map of job identifiers to the progress of adding participants to a census.
// This is used to check the progress of the job.
var addParticipantsToCensusWorkers sync.Map

// createCensusHandler godoc
//
//	@Summary		Create a new census
//	@Description	Create a new census for an organization. Requires Manager/Admin role.
//	@Tags			census
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.OrganizationCensus	true	"Census information"
//	@Success		200		{object}	apicommon.OrganizationCensus
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/census [post]
func (a *API) createCensusHandler(w http.ResponseWriter, r *http.Request) {
	// Parse request
	censusInfo := &apicommon.OrganizationCensus{}
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
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	census := &db.Census{
		Type:       censusInfo.Type,
		OrgAddress: util.TrimHex(censusInfo.OrgAddress),
		CreatedAt:  time.Now(),
	}
	censusID, err := a.db.SetCensus(census)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	apicommon.HttpWriteJSON(w, apicommon.OrganizationCensus{
		ID:         censusID,
		Type:       census.Type,
		OrgAddress: census.OrgAddress,
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
	apicommon.HttpWriteJSON(w, apicommon.OrganizationCensusFromDB(census))
}

// addParticipantsHandler godoc
//
//	@Summary		Add participants to a census
//	@Description	Add multiple participants to a census. Requires Manager/Admin role.
//	@Tags			census
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string								true	"Census ID"
//	@Param			async	query		boolean								false	"Process asynchronously and return job ID"
//	@Param			request	body		apicommon.AddParticipantsRequest	true	"Participants to add"
//	@Success		200		{object}	apicommon.AddParticipantsResponse
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		404		{object}	errors.Error	"Census not found"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/census/{id} [post]
func (a *API) addParticipantsHandler(w http.ResponseWriter, r *http.Request) {
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
	async := r.URL.Query().Get("async") == "true"

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
	// decode the participants from the request body
	participants := &apicommon.AddParticipantsRequest{}
	if err := json.NewDecoder(r.Body).Decode(participants); err != nil {
		log.Error(err)
		errors.ErrMalformedBody.Withf("missing participants").Write(w)
		return
	}
	// check if there are participants to add
	if len(participants.Participants) == 0 {
		apicommon.HttpWriteJSON(w, &apicommon.AddParticipantsResponse{ParticipantsNo: 0})
		return
	}
	// add the org participants to the census in the database
	progressChan, err := a.db.SetBulkCensusMembership(
		passwordSalt,
		censusID.String(),
		participants.DbOrgParticipants(census.OrgAddress),
	)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	if !async {
		// Wait for the channel to be closed (100% completion)
		var lastProgress *db.BulkCensusMembershipStatus
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
		apicommon.HttpWriteJSON(w, &apicommon.AddParticipantsResponse{ParticipantsNo: uint32(lastProgress.Added)})
		return
	}

	// if async create a new job identifier
	jobID := internal.HexBytes(util.RandomBytes(16))
	go func() {
		for p := range progressChan {
			// We need to drain the channel to avoid blocking
			addParticipantsToCensusWorkers.Store(jobID.String(), p)
		}
	}()

	apicommon.HttpWriteJSON(w, &apicommon.AddParticipantsResponse{JobID: jobID})
}

// addParticipantsJobCheckHandler godoc
//
//	@Summary		Check the progress of adding participants
//	@Description	Check the progress of a job to add participants to a census. Returns the progress of the job.
//	@Description	If the job is completed, the job is deleted after 60 seconds.
//	@Tags			census
//	@Accept			json
//	@Produce		json
//	@Param			jobid	path		string	true	"Job ID"
//	@Success		200		{object}	db.BulkCensusMembershipStatus
//	@Failure		400		{object}	errors.Error	"Invalid job ID"
//	@Failure		404		{object}	errors.Error	"Job not found"
//	@Router			/census/check/{jobid} [get]
func (a *API) addParticipantsJobCheckHandler(w http.ResponseWriter, r *http.Request) {
	jobID := internal.HexBytes{}
	if err := jobID.ParseString(chi.URLParam(r, "jobid")); err != nil {
		errors.ErrMalformedURLParam.Withf("invalid job ID").Write(w)
		return
	}

	if v, ok := addParticipantsToCensusWorkers.Load(jobID.String()); ok {
		p := v.(*db.BulkCensusMembershipStatus)
		if p.Progress == 100 {
			go func() {
				// Schedule the deletion of the job after 60 seconds
				time.Sleep(60 * time.Second)
				addParticipantsToCensusWorkers.Delete(jobID.String())
			}()
		}
		apicommon.HttpWriteJSON(w, p)
		return
	}

	errors.ErrJobNotFound.Withf("%s", jobID.String()).Write(w)
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
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	// build the census and store it
	cspSignerPubKey := a.account.PubKey // TODO: use a different key based on the censusID
	var pubCensus *db.PublishedCensus
	switch census.Type {
	case CensusTypeSMSOrMail:
		pubCensus = &db.PublishedCensus{
			Census:    *census,
			URI:       a.serverURL + "/process",
			Root:      cspSignerPubKey.String(),
			CreatedAt: time.Now(),
		}
	default:
		errors.ErrCensusTypeNotFound.Write(w)
		return
	}

	if err := a.db.SetPublishedCensus(pubCensus); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	apicommon.HttpWriteJSON(w, &apicommon.PublishedCensusResponse{
		URI:      pubCensus.URI,
		Root:     cspSignerPubKey,
		CensusID: censusID,
	})
}
