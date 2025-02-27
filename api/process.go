package api

import (
	"bytes"
	"crypto"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications"
	"github.com/vocdoni/saas-backend/twofactor"
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
	processID = util.TrimHex(processID)

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
	orgParticipants, err := a.db.OrgParticipantsMemberships(pubCensus.Census.OrgAddress, pubCensus.Census.ID.Hex(), processID)
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	type participant struct {
		ParticipantNo string
		Email         string
		Phone         string
		ElectionID    string
	}
	if pubCensus.Census.Type == db.CensusTypeSMSorMail || pubCensus.Census.Type == db.CensusTypeMail || pubCensus.Census.Type == db.CensusTypeSMS {
		if err := a.twofactor.AddProcess(process.ID, pubCensus.Census.Type, orgParticipants); err != nil {
			ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
	}

	httpWriteOK(w)
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
			ErrUnauthorized.WithErr(errors.New(authResp.Error)).Write(w)
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
		ErrUnauthorized.WithErr(errors.New(signResp.Error)).Write(w)
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

	// TODO enable only password censuses
	censusType := process.PublishedCensus.Census.Type
	if censusType != db.CensusTypeMail && censusType != db.CensusTypeSMSorMail &&
		censusType != db.CensusTypeSMS {
		return &uuid.Nil, ErrInvalidOrganizationData.Withf("invalid census type")
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

	// first verify password
	if req.Password != "" && !bytes.Equal(internal.HashPassword(passwordSalt, req.Password), participant.HashedPass) {
		return &uuid.Nil, ErrUnauthorized.Withf("invalid user data")
	}

	// create sha of participantNo
	sha := crypto.SHA256.New()
	sha.Write([]byte(participant.ParticipantNo))
	participantNoHash := sha.Sum(nil)
	var authResp twofactor.AuthResponse
	if censusType == db.CensusTypeMail || censusType == db.CensusTypeSMSorMail {
		if req.Email == "" {
			return &uuid.Nil, ErrUnauthorized.Withf("missing email")
		}
		if !bytes.Equal(internal.HashOrgData(process.OrgAddress, req.Email), participant.HashedEmail) {
			return &uuid.Nil, ErrUnauthorized.Withf("invalid user data")
		}
		authResp = a.twofactor.InitiateAuth(processId, participantNoHash, req.Email, notifications.Email)
	} else if censusType == db.CensusTypeSMS || censusType == db.CensusTypeSMSorMail {
		if req.Phone == "" {
			return &uuid.Nil, ErrUnauthorized.Withf("missing phone")
		}
		if !bytes.Equal(internal.HashOrgData(process.OrgAddress, req.Phone), participant.HashedPhone) {
			return &uuid.Nil, ErrUnauthorized.Withf("invalid user data")
		}
		authResp = a.twofactor.InitiateAuth(processId, participantNoHash, req.Phone, notifications.SMS)
	} else {
		return &uuid.Nil, ErrUnauthorized.Withf("invalid census type")
	}

	if !authResp.Success {
		return &uuid.Nil, errors.New(authResp.Error)
	}
	if authResp.AuthToken == nil {
		return &uuid.Nil, fmt.Errorf("auth token is nil")
	}
	return authResp.AuthToken, nil
}
