package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/db"
)

// createCensusHandler creates a new census for an organization.
// Requires Manager/Admin role. Returns census ID on success.
func (a *API) createCensusHandler(w http.ResponseWriter, r *http.Request) {
	// Parse request
	censusInfo := &CreateCensusRequest{}
	if err := json.NewDecoder(r.Body).Decode(&censusInfo); err != nil {
		ErrMalformedBody.Write(w)
		return
	}

	// get the user from the request context
	user, ok := userFromContext(r.Context())
	if !ok {
		ErrUnauthorized.Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(censusInfo.OrgAddress, db.ManagerRole) && !user.HasRoleFor(censusInfo.OrgAddress, db.AdminRole) {
		ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	census := &db.Census{
		Type:       censusInfo.Type,
		OrgAddress: censusInfo.OrgAddress,
		CreatedAt:  time.Now(),
	}
	censusId, err := a.db.SetCensus(census)
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	httpWriteJSON(w, censusId)
}

// censusInfoHandler retrieves census information by ID.
// Returns census type, organization address, and creation time.
func (a *API) censusInfoHandler(w http.ResponseWriter, r *http.Request) {
	censusID := chi.URLParam(r, "id")
	if censusID == "" {
		ErrMalformedURLParam.Withf("missing census ID").Write(w)
		return
	}

	census, err := a.db.Census(censusID)
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	httpWriteJSON(w, census)
}

// addParticipantsHandler adds multiple participants to a census.
// Requires Manager/Admin role. Returns number of participants added.
func (a *API) addParticipantsHandler(w http.ResponseWriter, r *http.Request) {
	censusID := chi.URLParam(r, "id")
	if censusID == "" {
		ErrMalformedURLParam.Withf("missing census ID").Write(w)
		return
	}

	participantsInfo := &AddParticipantsRequest{}
	if err := json.NewDecoder(r.Body).Decode(&participantsInfo); err != nil {
		ErrMalformedBody.Withf("missing participants").Write(w)
		return
	}

	if len(participantsInfo.Participants) == 0 {
		httpWriteJSON(w, &AddParticipantsResponse{ParticipantsNo: 0})
		return
	}

	// get the user from the request context
	user, ok := userFromContext(r.Context())
	if !ok {
		ErrUnauthorized.Write(w)
		return
	}

	// retrieve census
	census, err := a.db.Census(censusID)
	if err != nil {
		if err == db.ErrNotFound {
			ErrMalformedURLParam.Withf("census not found").Write(w)
			return
		}
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(census.OrgAddress, db.ManagerRole) && !user.HasRoleFor(census.OrgAddress, db.AdminRole) {
		ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	// add the org participants where necessary
	no, err := a.db.SetBulkCensusMembership(passwordSalt, censusID, participantsInfo.Participants)
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	// attach them to the census

	if len(participantsInfo.Participants) != int(no.UpsertedCount) {
		ErrInternalStorageError.Withf("not all participants were added").Write(w)
		return
	}
	httpWriteJSON(w, int(no.UpsertedCount))
}

// publishCensusHandler publishes a census for voting.
// Requires Manager/Admin role. Returns published census with credentials.
func (a *API) publishCensusHandler(w http.ResponseWriter, r *http.Request) {
	censusID := chi.URLParam(r, "id")
	if censusID == "" {
		ErrMalformedURLParam.Withf("missing census ID").Write(w)
		return
	}

	// get the user from the request context
	user, ok := userFromContext(r.Context())
	if !ok {
		ErrUnauthorized.Write(w)
		return
	}

	census, err := a.db.Census(censusID)
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(census.OrgAddress, db.ManagerRole) && !user.HasRoleFor(census.OrgAddress, db.AdminRole) {
		ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	var pubCensus *db.PublishedCensus
	switch census.Type {
	case "sms_or_mail":
		// TODO send sms or mail
		pubCensus = &db.PublishedCensus{
			Census: *census,
			URI:    a.serverURL + "/csp/",
			Root:   a.account.PubKey,
		}
	}

	if err := a.db.SetPublishedCensus(pubCensus); err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	httpWriteJSON(w, pubCensus)
}
