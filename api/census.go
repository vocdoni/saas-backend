package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/dvote/util"
)

type PublishedCensusResponse struct {
	URI      string `json:"uri" bson:"uri"`
	Root     string `json:"root" bson:"root"`
	CensusID string `json:"censusId" bson:"censusId"`
}

// addParticipantsToCensusWorkers is a map of job identifiers to the progress of adding participants to a census.
// This is used to check the progress of the job.
var addParticipantsToCensusWorkers sync.Map

// createCensusHandler creates a new census for an organization.
// Requires Manager/Admin role. Returns census ID on success.
func (a *API) createCensusHandler(w http.ResponseWriter, r *http.Request) {
	// Parse request
	censusInfo := &OrganizationCensus{}
	if err := json.NewDecoder(r.Body).Decode(&censusInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}

	// get the user from the request context
	user, ok := userFromContext(r.Context())
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
	httpWriteJSON(w, OrganizationCensus{
		ID:         censusID,
		Type:       census.Type,
		OrgAddress: census.OrgAddress,
	})
}

// censusInfoHandler retrieves census information by ID.
// Returns census type, organization address, and creation time.
func (a *API) censusInfoHandler(w http.ResponseWriter, r *http.Request) {
	censusID := chi.URLParam(r, "id")
	if censusID == "" {
		errors.ErrMalformedURLParam.Withf("missing census ID").Write(w)
		return
	}
	census, err := a.db.Census(censusID)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	httpWriteJSON(w, organizationCensusFromDB(census))
}

// addParticipantsHandler adds multiple participants to a census.
// Requires Manager/Admin role. Returns number of participants added.
func (a *API) addParticipantsHandler(w http.ResponseWriter, r *http.Request) {
	censusID := chi.URLParam(r, "id")
	if censusID == "" {
		errors.ErrMalformedURLParam.Withf("missing census ID").Write(w)
		return
	}
	// get the user from the request context
	user, ok := userFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// get the async flag
	async := false
	if r.URL.Query().Get("async") == "true" {
		async = true
	}
	// retrieve census
	census, err := a.db.Census(censusID)
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
	participants := &AddParticipantsRequest{}
	if err := json.NewDecoder(r.Body).Decode(participants); err != nil {
		log.Error(err)
		errors.ErrMalformedBody.Withf("missing participants").Write(w)
		return
	}
	// check if there are participants to add
	if len(participants.Participants) == 0 {
		httpWriteJSON(w, &AddParticipantsResponse{ParticipantsNo: 0})
		return
	}
	// add the org participants to the census in the database
	progressChan, err := a.db.SetBulkCensusMembership(passwordSalt, censusID, participants.dbOrgParticipants(census.OrgAddress))
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
				"census", censusID,
				"org", census.OrgAddress,
				"progress", p.Progress,
				"added", p.Added,
				"total", p.Total)
		}
		// Return the number of participants added
		httpWriteJSON(w, &AddParticipantsResponse{ParticipantsNo: uint32(lastProgress.Added)})
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

	httpWriteJSON(w, &AddParticipantsResponse{JobID: jobID})
}

// addParticipantsJobCheckHandler checks the progress of a job to add participants to a census.
// Returns the progress of the job. If the job is completed, the job is deleted.
func (a *API) addParticipantsJobCheckHandler(w http.ResponseWriter, r *http.Request) {
	jobIDstr := chi.URLParam(r, "jobid")
	if jobIDstr == "" {
		errors.ErrMalformedURLParam.Withf("missing job ID").Write(w)
		return
	}
	jobID := internal.HexBytes{}
	if err := jobID.FromString(jobIDstr); err != nil {
		errors.ErrMalformedURLParam.Withf("invalid job ID").Write(w)
		return
	}

	if v, ok := addParticipantsToCensusWorkers.Load(jobID.String()); ok {
		p := v.(*db.BulkCensusMembershipStatus)
		if p.Progress == 100 {
			addParticipantsToCensusWorkers.Delete(jobID.String())
		}
		httpWriteJSON(w, p)
		return
	}

	errors.ErrJobNotFound.Write(w)
}

// publishCensusHandler publishes a census for voting.
// Requires Manager/Admin role. Returns published census with credentials.
func (a *API) publishCensusHandler(w http.ResponseWriter, r *http.Request) {
	censusID := chi.URLParam(r, "id")
	if censusID == "" {
		errors.ErrMalformedURLParam.Withf("missing census ID").Write(w)
		return
	}

	// get the user from the request context
	user, ok := userFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	census, err := a.db.Census(censusID)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(census.OrgAddress, db.ManagerRole) && !user.HasRoleFor(census.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	var pubCensus *db.PublishedCensus
	switch census.Type {
	case "sms_or_mail":
		// TODO send sms or mail
		pubCensus = &db.PublishedCensus{
			Census: *census,
			URI:    a.serverURL + "/process",
			Root:   a.account.PubKey.String(),
		}
	default:
		errors.ErrGenericInternalServerError.WithErr(fmt.Errorf("unsupported census type")).Write(w)
		return
	}

	if err := a.db.SetPublishedCensus(pubCensus); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	httpWriteJSON(w, &PublishedCensusResponse{
		URI:  pubCensus.URI,
		Root: pubCensus.Root,
	})
}
