package api

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/util"
)

// createProcessHandler creates a new voting process.
// Requires Manager/Admin role. Returns 201 on success.
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

	pubCensus, err := a.db.PublishedCensus(util.TrimHex(processInfo.PublishedCensusRoot), processInfo.PublishedCensusURI)
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
		OrgAddress:      pubCensus.Census.OrgAddress,
	}

	if err := a.db.SetProcess(process); err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	w.WriteHeader(http.StatusCreated)
}

// processInfoHandler retrieves voting process information by ID.
// Returns process details including census and metadata.
func (a *API) processInfoHandler(w http.ResponseWriter, r *http.Request) {
	processID := chi.URLParam(r, "processId")
	if len(processID) == 0 {
		ErrMalformedURLParam.Withf("missing process ID").Write(w)
		return
	}

	process, err := a.db.Process([]byte(processID))
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	httpWriteJSON(w, process)
}

// processAuthHandler validates participant authentication for a voting process.
// Supports email, phone, or password authentication.
func (a *API) processAuthHandler(w http.ResponseWriter, r *http.Request) {
	processID := chi.URLParam(r, "processId")
	if len(processID) == 0 {
		ErrMalformedURLParam.Withf("missing process ID").Write(w)
		return
	}

	var req InitiateAuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ErrMalformedBody.Write(w)
		return
	}
	if len(req.ParticipantNo) == 0 {
		ErrMalformedBody.Withf("missing participant number").Write(w)
		return
	}

	// retrieve process info
	process, err := a.db.Process([]byte(util.TrimHex(processID)))
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if process.PublishedCensus.Census.OrgAddress != process.OrgAddress {
		ErrInvalidOrganizationData.Write(w)
		return
	}
	// retrieve memership info
	if _, err = a.db.CensusMembership(process.PublishedCensus.Census.ID.Hex(), req.ParticipantNo); err != nil {
		ErrUnauthorized.Withf("participant not found in census").Write(w)
		return
	}
	// retrieve participant info
	participant, err := a.db.OrgParticipantByNo(process.OrgAddress, req.ParticipantNo)
	if err != nil {
		ErrUnauthorized.Withf("participant not found").Write(w)
		return
	}

	// verify participant info
	if req.Email != "" && !bytes.Equal(internal.HashOrgData(process.OrgAddress, req.Email), participant.HashedEmail) {
		ErrUnauthorized.Withf("invalid user data").Write(w)
		return
	}
	if req.Phone != "" && !bytes.Equal(internal.HashOrgData(process.OrgAddress, req.Phone), participant.HashedPhone) {
		ErrUnauthorized.Withf("invalid user data").Write(w)
		return
	}
	if req.Password != "" && !bytes.Equal(internal.HashPassword(passwordSalt, req.Password), participant.HashedPass) {
		ErrUnauthorized.Withf("invalid user data").Write(w)
		return
	}

	httpWriteJSON(w, map[string]bool{"ok": true})
}

// // initiateAuthHandler starts 2FA process and returns auth token.
// // Not currently implemented.
// func (a *API) initiateAuthHandler(w http.ResponseWriter, r *http.Request) {
// 	processID := chi.URLParam(r, "processId")

// 	var req InitiateAuthRequest
// 	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
// 		ErrMalformedJSON.Write(w)
// 		return
// 	}

// 	token, err := a.db.CreateAuthToken([]byte(processID), req.ParticipantNo)
// 	if err != nil {
// 		ErrGenericInternalServerError.WithError(err).Write(w)
// 		return
// 	}

// 	// TODO: Send 2FA code via email/SMS based on census type

// 	httpWriteJSON(w, map[string]string{"token": token.Token})
// }

// // verifyAuthCodeHandler validates 2FA code.
// // Not currently implemented.
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

// // generateProofHandler creates blind signature proof for voting.
// // Not currently implemented.
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
