package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/util"
)

type AuthRequest struct {
	AuthToken *uuid.UUID `json:"authToken,omitempty"`
	AuthData  []string   `json:"authData,omitempty"` // reserved for the auth handler
}

type SignRequest struct {
	TokenR  []byte `json:"tokenR,omitempty"`
	Address string `json:"address,omitempty"`
	Payload []byte `json:"payload,omitempty"`
}

type twofactorResponse struct {
	AuthToken *uuid.UUID `json:"authToken,omitempty"`
	TokenR    []byte     `json:"tokenR,omitempty"`
	Signature []byte     `json:"signature,omitempty"`
}

// getSubscriptionsHandler handles the request to get the subscriptions of an organization.
// It returns the list of subscriptions with their information.
func (a *API) twofactorAuthHandler(w http.ResponseWriter, r *http.Request) {
	urlProcessId := chi.URLParam(r, "processId")
	if len(urlProcessId) == 0 {
		ErrMalformedURLParam.Withf("missing process ID").Write(w)
		return
	}

	stepString := chi.URLParam(r, "step")
	step, err := strconv.Atoi(stepString)
	if err != nil || (step != 0 && step != 1) {
		ErrMalformedURLParam.Withf("wrong step ID").Write(w)
		return
	}
	processId := []byte(util.TrimHex(urlProcessId))
	switch step {
	case 0:
		authToken, err := a.initiateAuthRequest(r, processId)
		if err != nil {
			ErrUnauthorized.WithErr(err).Write(w)
			return
		}
		httpWriteJSON(w, &twofactorResponse{AuthToken: authToken})
		return
	case 1:
		var req AuthRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			ErrMalformedBody.Write(w)
			return
		}
		authResp := a.twofactor.Auth(processId, req.AuthToken, req.AuthData)
		if !authResp.Success {
			ErrUnauthorized.Withf(authResp.Error).Write(w)
			return
		}
		httpWriteOK(w)
		return
	}
}

// getSubscriptionsHandler handles the request to get the subscriptions of an organization.
// It returns the list of subscriptions with their information.
func (a *API) twofactorSignHandler(w http.ResponseWriter, r *http.Request) {
	urlProcessId := chi.URLParam(r, "processId")
	if len(urlProcessId) == 0 {
		ErrMalformedURLParam.Withf("missing process ID").Write(w)
		return
	}
	processId := []byte(util.TrimHex(urlProcessId))

	var req SignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ErrMalformedBody.Write(w)
		return
	}
	signResp := a.twofactor.Sign(processId, req.Payload, req.TokenR, req.Address)
	if !signResp.Success {
		ErrUnauthorized.Withf(signResp.Error).Write(w)
		return
	}
	httpWriteJSON(w, &twofactorResponse{Signature: signResp.Signature})
}

func (a *API) initiateAuthRequest(r *http.Request, processId []byte) (*uuid.UUID, error) {
	var req InitiateAuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return &uuid.Nil, ErrMalformedBody
	}
	if len(req.ParticipantNo) == 0 {
		return &uuid.Nil, ErrMalformedBody.Withf("missing participant number")
	}

	// retrieve process info
	process, err := a.db.Process(processId)
	if err != nil {
		return &uuid.Nil, ErrGenericInternalServerError.WithErr(err)
	}
	if process.PublishedCensus.Census.OrgAddress != process.OrgAddress {
		return &uuid.Nil, ErrInvalidOrganizationData
	}
	// retrieve memership info
	if _, err = a.db.CensusMembership(process.PublishedCensus.Census.ID.Hex(), req.ParticipantNo); err != nil {
		return &uuid.Nil, ErrUnauthorized.Withf("participant not found in census")
	}
	// retrieve participant info
	participant, err := a.db.OrgParticipantByNo(process.OrgAddress, req.ParticipantNo)
	if err != nil {
		return &uuid.Nil, ErrUnauthorized.Withf("participant not found")
	}

	// verify participant info
	if req.Email != "" && !bytes.Equal(internal.HashOrgData(process.OrgAddress, req.Email), participant.HashedEmail) {
		return &uuid.Nil, ErrUnauthorized.Withf("invalid user data")
	}
	if req.Phone != "" && !bytes.Equal(internal.HashOrgData(process.OrgAddress, req.Phone), participant.HashedPhone) {
		return &uuid.Nil, ErrUnauthorized.Withf("invalid user data")
	}
	if req.Password != "" && !bytes.Equal(internal.HashPassword(passwordSalt, req.Password), participant.HashedPass) {
		return &uuid.Nil, ErrUnauthorized.Withf("invalid user data")
	}
	authResp := a.twofactor.InitiateAuth(processId, participant.ParticipantNo)
	if !authResp.Success {
		return &uuid.Nil, fmt.Errorf(authResp.Error)
	}
	if authResp.AuthToken == nil {
		return &uuid.Nil, fmt.Errorf("auth token is nil")
	}
	return authResp.AuthToken, nil
}
