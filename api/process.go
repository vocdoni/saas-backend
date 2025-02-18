package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/util"
)

// CreateProcessRequest represents the request payload for creating a new process.
// It requires a published census ID and optional metadata for the process.
type CreateProcessRequest struct {
	PublishedCensusID string `json:"censusID"`
	Metadata          []byte `json:"metadata"`
}

// createProcessHandler handles the creation of a new voting process.
// It requires Manager or Admin role for the organization owning the census.
// Expects a process ID in the URL path and a CreateProcessRequest in the body.
// Returns 201 on successful creation or appropriate error responses.
func (a *API) createProcessHandler(w http.ResponseWriter, r *http.Request) {
	processID := chi.URLParam(r, "processId")
	if processID == "" {
		ErrMalformedURLParam.Withf("missing census ID").Write(w)
		return
	}

	processInfo := &CreateProcessRequest{}
	if err := json.NewDecoder(r.Body).Decode(&processInfo); err != nil {
		ErrMalformedBody.Write(w)
		return
	}

	// get the user from the request context
	user, ok := userFromContext(r.Context())
	if !ok {
		ErrUnauthorized.Write(w)
		return
	}

	pubCensus, err := a.db.PublishedCensus(processInfo.PublishedCensusID)
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(pubCensus.Census.OrgAddress, db.ManagerRole) && !user.HasRoleFor(pubCensus.Census.OrgAddress, db.AdminRole) {
		ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	process := &db.Process{
		ID:              []byte(util.TrimHex(processID)),
		PublishedCensus: *pubCensus,
		Metadata:        processInfo.Metadata,
		OrgID:           pubCensus.Census.OrgAddress,
	}

	if err := a.db.SetProcess(process); err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	w.WriteHeader(http.StatusCreated)
}

// processInfoHandler retrieves information about a specific voting process.
// It expects a process ID in the URL path and returns the complete process
// information. Returns 400 if the process ID is missing or 500 on internal errors.
func (a *API) processInfoHandler(w http.ResponseWriter, r *http.Request) {
	processID := chi.URLParam(r, "processId")
	if processID == "" {
		ErrMalformedURLParam.Withf("missing process ID").Write(w)
		return
	}

	process, err := a.db.Process(processID)
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	httpWriteJSON(w, process)
}

// // initiateAuthHandler handles the initiation of the two-factor authentication process.
// // It expects a process ID in the URL path and an InitiateAuthRequest in the body.
// // Returns an authentication token for the subsequent verification step.
// func (a *API) initiateAuthHandler(w http.ResponseWriter, r *http.Request) {
// 	processID := chi.URLParam(r, "processId")

// 	var req InitiateAuthRequest
// 	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
// 		ErrMalformedJSON.Write(w)
// 		return
// 	}

// 	token, err := a.db.CreateAuthToken([]byte(processID), req.ParticipantID)
// 	if err != nil {
// 		ErrGenericInternalServerError.WithError(err).Write(w)
// 		return
// 	}

// 	// TODO: Send 2FA code via email/SMS based on census type

// 	httpWriteJSON(w, map[string]string{"token": token.Token})
// }

// // verifyAuthCodeHandler handles the verification of the two-factor authentication code.
// // It expects a VerifyAuthRequest containing the token and verification code.
// // Returns 200 on successful verification or appropriate error responses.
// func (a *API) verifyAuthCodeHandler(w http.ResponseWriter, r *http.Request) {
// 	var req VerifyAuthRequest
// 	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
// 		ErrMalformedJSON.Write(w)
// 		return
// 	}

// 	err := a.db.VerifyAuthToken(req.Token, req.Code)
// 	if err != nil {
// 		ErrGenericInternalServerError.WithError(err).Write(w)
// 		return
// 	}

// 	w.WriteHeader(http.StatusOK)
// }

// // generateProofHandler handles the generation of blind signature proofs.
// // It expects a GenerateProofRequest containing the token and blinded address.
// // Returns the generated proof or appropriate error responses.
// func (a *API) generateProofHandler(w http.ResponseWriter, r *http.Request) {
// 	var req GenerateProofRequest
// 	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
// 		ErrMalformedJSON.Write(w)
// 		return
// 	}

// 	proof, err := a.db.GenerateProof(req.Token, req.BlindedAddress)
// 	if err != nil {
// 		ErrGenericInternalServerError.WithError(err).Write(w)
// 		return
// 	}

// 	httpWriteJSON(w, map[string][]byte{"proof": proof})
// }
