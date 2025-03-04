package api

import (
	"bytes"
	"encoding/hex"
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
		ErrMalformedURLParam.Withf("missing process ID").Write(w)
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

	pubCensus, err := a.db.PublishedCensus(
		util.TrimHex(processInfo.PublishedCensusRoot),
		processInfo.PublishedCensusURI,
		processInfo.CensusID,
	)
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(pubCensus.Census.OrgAddress, db.ManagerRole) && !user.HasRoleFor(pubCensus.Census.OrgAddress, db.AdminRole) {
		ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	id := internal.HexBytes{}
	err = id.FromString(processID)
	if err != nil {
		ErrMalformedURLParam.Withf("invalid process ID").Write(w)
		return
	}

	process := &db.Process{
		ID:              id,
		PublishedCensus: *pubCensus,
		Metadata:        processInfo.Metadata,
		OrgAddress:      pubCensus.Census.OrgAddress,
	}

	if err := a.db.SetProcess(process); err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	orgParticipants, err := a.db.OrgParticipantsMemberships(
		pubCensus.Census.OrgAddress,
		pubCensus.Census.ID.Hex(),
		"",
		[]internal.HexBytes{id},
	)
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	switch pubCensus.Census.Type {
	case db.CensusTypeSMSorMail, db.CensusTypeMail, db.CensusTypeSMS:
		if err := a.twofactor.AddProcess(pubCensus.Census.Type, orgParticipants); err != nil {
			ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
	default:
		ErrNotSupported.Withf("census type not supported").Write(w)
		return
	}
	if pubCensus.Census.Type == db.CensusTypeSMSorMail ||
		pubCensus.Census.Type == db.CensusTypeMail ||
		pubCensus.Census.Type == db.CensusTypeSMS {
		if err := a.twofactor.AddProcess(pubCensus.Census.Type, orgParticipants); err != nil {
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
	TokenR     internal.HexBytes `json:"tokenR"`
	AuthToken  *uuid.UUID        `json:"authToken"`
	Address    string            `json:"address,omitempty"`
	Payload    string            `json:"payload,omitempty"`
	ElectionId internal.HexBytes `json:"electionId,omitempty"`
}

type twofactorResponse struct {
	AuthToken *uuid.UUID        `json:"authToken,omitempty"`
	Token     *uuid.UUID        `json:"token,omitempty"`
	Signature internal.HexBytes `json:"signature,omitempty"`
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

	switch step {
	case 0:
		authToken, err := a.initiateAuthRequest(r, urlProcessId)
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
		authResp := a.twofactor.Auth(urlProcessId, req.AuthToken, req.AuthData)
		if !authResp.Success {
			ErrUnauthorized.WithErr(errors.New(authResp.Error)).Write(w)
			return
		}
		httpWriteJSON(w, &twofactorResponse{AuthToken: authResp.AuthToken})
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
	payload, err := hex.DecodeString(util.TrimHex(req.Payload))
	if err != nil {
		ErrMalformedBody.WithErr(err).Write(w)
		return
	}
	signResp := a.twofactor.Sign(*req.AuthToken, nil, payload, processId, "", "ecdsa")
	if !signResp.Success {
		ErrUnauthorized.WithErr(errors.New(signResp.Error)).Write(w)
		return
	}
	httpWriteJSON(w, &twofactorResponse{Signature: signResp.Signature})
}

func (a *API) initiateAuthRequest(r *http.Request, processId string) (*uuid.UUID, error) {
	var req InitiateAuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, ErrMalformedBody
	}
	if len(req.ParticipantNo) == 0 {
		return nil, ErrMalformedBody.Withf("missing participant number")
	}

	if len(req.Email) == 0 && len(req.Phone) == 0 {
		return nil, ErrMalformedBody.Withf("missing auth data")
	}

	processIdBytes, err := hex.DecodeString(processId)
	if err != nil {
		return nil, ErrMalformedURLParam.Withf("invalid process ID")
	}

	// retrieve process info
	process, err := a.db.Process(processIdBytes)
	if err != nil {
		return nil, ErrGenericInternalServerError.WithErr(err)
	}
	if process.PublishedCensus.Census.OrgAddress != process.OrgAddress {
		return nil, ErrInvalidOrganizationData
	}

	// TODO enable only password censuses
	censusType := process.PublishedCensus.Census.Type
	if censusType != db.CensusTypeMail && censusType != db.CensusTypeSMSorMail &&
		censusType != db.CensusTypeSMS {
		return nil, ErrInvalidOrganizationData.Withf("invalid census type")
	}
	// retrieve memership info
	if _, err = a.db.CensusMembership(process.PublishedCensus.Census.ID.Hex(), req.ParticipantNo); err != nil {
		return nil, ErrUnauthorized.Withf("participant not found in census")
	}
	// retrieve participant info
	participant, err := a.db.OrgParticipantByNo(process.OrgAddress, req.ParticipantNo)
	if err != nil {
		return nil, ErrUnauthorized.Withf("participant not found")
	}

	// first verify password
	if req.Password != "" && !bytes.Equal(internal.HashPassword(passwordSalt, req.Password), participant.HashedPass) {
		return nil, ErrUnauthorized.Withf("invalid user data")
	}

	var authResp twofactor.AuthResponse
	switch censusType {
	case db.CensusTypeMail:
		if req.Email == "" {
			return nil, ErrUnauthorized.Withf("missing email")
		}
		if !bytes.Equal(internal.HashOrgData(process.OrgAddress, req.Email), participant.HashedEmail) {
			return nil, ErrUnauthorized.Withf("invalid user data")
		}
		authResp = a.twofactor.InitiateAuth(processId, participant.ParticipantNo, req.Email, notifications.Email)
	case db.CensusTypeSMS:
		if req.Phone == "" {
			return nil, ErrUnauthorized.Withf("missing phone")
		}
		pn, err := internal.SanitizeAndVerifyPhoneNumber(req.Phone)
		if err != nil {
			return nil, ErrUnauthorized.Withf("invalid phone number")
		}
		if !bytes.Equal(internal.HashOrgData(process.OrgAddress, pn), participant.HashedPhone) {
			return nil, ErrUnauthorized.Withf("invalid user data")
		}
		authResp = a.twofactor.InitiateAuth(processId, participant.ParticipantNo, pn, notifications.SMS)
	case db.CensusTypeSMSorMail:
		if req.Email != "" {
			if !bytes.Equal(internal.HashOrgData(process.OrgAddress, req.Email), participant.HashedEmail) {
				return nil, ErrUnauthorized.Withf("invalid user data")
			}
			authResp = a.twofactor.InitiateAuth(processId, participant.ParticipantNo, req.Email, notifications.Email)
		} else if req.Phone != "" {
			pn, err := internal.SanitizeAndVerifyPhoneNumber(req.Phone)
			if err != nil {
				return nil, ErrUnauthorized.Withf("invalid phone number")
			}
			if !bytes.Equal(internal.HashOrgData(process.OrgAddress, pn), participant.HashedPhone) {
				return nil, ErrUnauthorized.Withf("invalid user data")
			}
			authResp = a.twofactor.InitiateAuth(processId, participant.ParticipantNo, pn, notifications.SMS)
		} else {
			return nil, ErrUnauthorized.Withf("missing email or phone")
		}
	default:
		return nil, ErrUnauthorized.Withf("invalid census type")
	}

	if !authResp.Success {
		return nil, ErrUnauthorized.Withf("%s", authResp.Error)
	}
	if authResp.AuthToken == nil {
		return nil, fmt.Errorf("auth token is nil")
	}
	return authResp.AuthToken, nil
}
