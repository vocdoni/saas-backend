package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/db"
)

// Request/Response types

// CreateCensusRequest represents the request payload for creating a new census.
// It specifies the type of census and the organization address it belongs to.
type CreateCensusRequest struct {
	Type       db.CensusType `json:"type"`
	OrgAddress string        `json:"orgAddress"`
}

// AddParticipantsRequest represents the request payload for adding participants to a census.
// It contains an array of CensusParticipant objects to be added to the census.
type AddParticipantsRequest struct {
	Participants []db.CensusParticipant `json:"participants"`
}

// AddParticipantsResponse represents the response payload after adding participants.
// It contains the number of participants successfully added to the census.
type AddParticipantsResponse struct {
	ParticipantsNo uint32 `json:"participantsNo"`
}

// InitiateAuthRequest represents the request payload for initiating authentication.
// It contains the participant ID and either an email or phone number for 2FA.
type InitiateAuthRequest struct {
	ParticipantID string `json:"participantId"`
	Email         string `json:"email,omitempty"`
	Phone         string `json:"phone,omitempty"`
}

// VerifyAuthRequest represents the request payload for verifying authentication.
// It contains the authentication token and verification code.
type VerifyAuthRequest struct {
	Token string `json:"token"`
	Code  string `json:"code"`
}

// GenerateProofRequest represents the request payload for generating a proof.
// It contains the authentication token and blinded address for proof generation.
type GenerateProofRequest struct {
	Token          string `json:"token"`
	BlindedAddress []byte `json:"blindedAddress"`
}

// createCensusHandler handles the creation of a new census.
// It requires either Manager or Admin role for the specified organization.
// The handler expects a CreateCensusRequest in the request body and returns
// the census ID on success. Returns 401 if unauthorized or 500 on internal errors.
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

// censusInfoHandler retrieves information about a specific census.
// It expects a census ID in the URL path and returns the complete census
// information. Returns 400 if the census ID is missing or 500 on internal errors.
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

// addParticipantsHandler handles bulk addition of participants to a census.
// It requires Manager or Admin role for the organization owning the census.
// Expects a census ID in the URL path and an AddParticipantsRequest in the body.
// Returns the number of participants successfully added or appropriate error responses.
func (a *API) addParticipantsHandler(w http.ResponseWriter, r *http.Request) {
	censusID := chi.URLParam(r, "id")
	if censusID == "" {
		ErrMalformedURLParam.Withf("missing census ID").Write(w)
		return
	}

	participantsInfo := &AddParticipantsRequest{}
	if err := json.NewDecoder(r.Body).Decode(&participantsInfo); err != nil {
		ErrMalformedBody.Withf("missing participants").Write(w)
		httpWriteJSON(w, &AddParticipantsResponse{ParticipantsNo: 0})
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

	no, err := a.db.BulkUpsertCensusParticipants(census.OrgAddress, census.ID.Hex(), passwordSalt, participantsInfo.Participants)
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if len(participantsInfo.Participants) != int(no.UpsertedCount) {
		ErrInternalStorageError.Withf("not all participants were added").Write(w)
		return
	}
	httpWriteJSON(w, int(no.UpsertedCount))
}

// publishCensusHandler handles the publication of a census, making it available for voting.
// It requires Manager or Admin role for the organization owning the census.
// Expects a census ID in the URL path and creates a PublishedCensus with the necessary
// credentials. Returns the published census details or appropriate error responses.
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

// publishedCensusInfoHandler retrieves information about a published census.
// It expects a census ID in the URL path and returns the complete published
// census information. Returns 400 if the census ID is missing or 500 on internal errors.
func (a *API) publishedCensusInfoHandler(w http.ResponseWriter, r *http.Request) {
	censusID := chi.URLParam(r, "id")
	if censusID == "" {
		ErrMalformedURLParam.Withf("missing census ID").Write(w)
		return
	}

	pubCensus, err := a.db.PublishedCensus(censusID)
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	httpWriteJSON(w, pubCensus)
}
