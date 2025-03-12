package api

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications"
	"github.com/vocdoni/saas-backend/twofactor"
	"go.vocdoni.io/dvote/util"
)

// createProcessHandler creates a new voting process.
// Requires Manager/Admin role. Returns 201 on success.
func (a *API) createProcessHandler(w http.ResponseWriter, r *http.Request) {
	processID := internal.HexBytes{}
	if err := processID.FromString(chi.URLParam(r, "processId")); err != nil {
		errors.ErrMalformedURLParam.Withf("missing process ID").Write(w)
		return
	}

	processInfo := &CreateProcessRequest{}
	if err := json.NewDecoder(r.Body).Decode(&processInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}

	if processInfo.PublishedCensusRoot == nil || processInfo.CensusID == nil {
		errors.ErrMalformedBody.Withf("missing published census root or ID").Write(w)
		return
	}

	// get the user from the request context
	user, ok := userFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	pubCensus, err := a.db.PublishedCensus(
		processInfo.PublishedCensusRoot.String(),
		processInfo.PublishedCensusURI,
		processInfo.CensusID.String(),
	)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("published census not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(pubCensus.Census.OrgAddress, db.ManagerRole) && !user.HasRoleFor(pubCensus.Census.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	// check that the process does not exist
	if _, err := a.db.Process(processID); err == nil {
		errors.ErrDuplicateConflict.Withf("process already exists").Write(w)
		return
	}

	// get the participants
	orgParticipants, err := a.db.OrgParticipantsMemberships(
		pubCensus.Census.OrgAddress,
		pubCensus.Census.ID.Hex(),
		"",
		[]internal.HexBytes{processID},
	)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// check the census type
	switch pubCensus.Census.Type {
	case db.CensusTypeSMSorMail, db.CensusTypeMail, db.CensusTypeSMS:
		if err := a.twofactor.AddProcess(pubCensus.Census.Type, orgParticipants); err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
	default:
		errors.ErrNotSupported.Withf("census type not supported").Write(w)
		return
	}

	// finally create the process
	process := &db.Process{
		ID:              processID,
		PublishedCensus: *pubCensus,
		Metadata:        processInfo.Metadata,
		OrgAddress:      pubCensus.Census.OrgAddress,
	}

	if err := a.db.SetProcess(process); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	httpWriteOK(w)
}

// processInfoHandler retrieves voting process information by ID.
// Returns process details including census and metadata.
func (a *API) processInfoHandler(w http.ResponseWriter, r *http.Request) {
	processID := chi.URLParam(r, "processId")
	if len(processID) == 0 {
		errors.ErrMalformedURLParam.Withf("missing process ID").Write(w)
		return
	}

	process, err := a.db.Process([]byte(processID))
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("process not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	httpWriteJSON(w, process)
}

// getSubscriptionsHandler handles the request to get the subscriptions of an organization.
// It returns the list of subscriptions with their information.
func (a *API) twofactorAuthHandler(w http.ResponseWriter, r *http.Request) {
	processID := internal.HexBytes{}
	if err := processID.FromString(chi.URLParam(r, "processId")); err != nil {
		errors.ErrMalformedURLParam.Withf("wrong process ID").Write(w)
		return
	}

	stepString := chi.URLParam(r, "step")
	step, err := strconv.Atoi(stepString)
	if err != nil || (step != 0 && step != 1) {
		errors.ErrMalformedURLParam.Withf("wrong step ID").Write(w)
		return
	}

	switch step {
	case 0:
		var req InitiateAuthRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			errors.ErrMalformedBody.Withf("missing auth data").Write(w)
			return
		}
		if len(req.ParticipantNo) == 0 {
			errors.ErrMalformedBody.Withf("missing participant number").Write(w)
			return
		}
		if len(req.Email) == 0 && len(req.Phone) == 0 {
			errors.ErrMalformedBody.Withf("missing email or phone").Write(w)
			return
		}
		authToken, err := a.initiateAuthRequest(&req, processID)
		if err != nil {
			errors.ErrUnauthorized.WithErr(err).Write(w)
			return
		}
		httpWriteJSON(w, &twofactorResponse{AuthToken: authToken})
		return
	case 1:
		var req AuthRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			errors.ErrMalformedBody.Write(w)
			return
		}
		authResp := a.twofactor.Auth(processID.String(), req.AuthToken, req.AuthData)
		if !authResp.Success {
			errors.ErrUnauthorized.WithErr(stderrors.New(authResp.Error)).Write(w)
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
		errors.ErrMalformedURLParam.Withf("missing process ID").Write(w)
		return
	}
	processId := []byte(util.TrimHex(urlProcessId))

	var req SignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	payload, err := hex.DecodeString(util.TrimHex(req.Payload))
	if err != nil {
		errors.ErrMalformedBody.WithErr(err).Write(w)
		return
	}
	signResp := a.twofactor.Sign(*req.AuthToken, nil, payload, processId, "", "ecdsa")
	if !signResp.Success {
		errors.ErrUnauthorized.WithErr(stderrors.New(signResp.Error)).Write(w)
		return
	}
	httpWriteJSON(w, &twofactorResponse{Signature: signResp.Signature})
}

func (a *API) initiateAuthRequest(req *InitiateAuthRequest, processID internal.HexBytes) (*uuid.UUID, error) {
	// retrieve process info
	process, err := a.db.Process(processID)
	if err != nil {
		if err == db.ErrNotFound {
			return nil, fmt.Errorf("process not found")
		}
		return nil, fmt.Errorf("internal server error: %v", err)
	}
	if process.PublishedCensus.Census.OrgAddress != process.OrgAddress {
		return nil, fmt.Errorf("invalid organization data")
	}

	// TODO enable only password censuses
	censusType := process.PublishedCensus.Census.Type
	if censusType != db.CensusTypeMail && censusType != db.CensusTypeSMSorMail &&
		censusType != db.CensusTypeSMS {
		return nil, fmt.Errorf("invalid census type")
	}
	// retrieve membership info
	if _, err = a.db.CensusMembership(process.PublishedCensus.Census.ID.Hex(), req.ParticipantNo); err != nil {
		return nil, fmt.Errorf("participant not found")
	}
	// retrieve participant info
	participant, err := a.db.OrgParticipantByNo(process.OrgAddress, req.ParticipantNo)
	if err != nil {
		return nil, fmt.Errorf("participant not found")
	}

	// first verify password
	if req.Password != "" && !bytes.Equal(internal.HashPassword(passwordSalt, req.Password), participant.HashedPass) {
		return nil, fmt.Errorf("invalid user data")
	}

	var authResp twofactor.AuthResponse
	switch censusType {
	case db.CensusTypeMail:
		if req.Email == "" {
			return nil, fmt.Errorf("missing email")
		}
		if !bytes.Equal(internal.HashOrgData(process.OrgAddress, req.Email), participant.HashedEmail) {
			return nil, fmt.Errorf("invalid user data")
		}
		authResp = a.twofactor.InitiateAuth(processID.String(), participant.ParticipantNo, req.Email, notifications.Email)
	case db.CensusTypeSMS:
		if req.Phone == "" {
			return nil, fmt.Errorf("missing phone")
		}
		pn, err := internal.SanitizeAndVerifyPhoneNumber(req.Phone)
		if err != nil {
			return nil, fmt.Errorf("invalid phone number")
		}
		if !bytes.Equal(internal.HashOrgData(process.OrgAddress, pn), participant.HashedPhone) {
			return nil, fmt.Errorf("invalid user data")
		}
		authResp = a.twofactor.InitiateAuth(processID.String(), participant.ParticipantNo, pn, notifications.SMS)
	case db.CensusTypeSMSorMail:
		if req.Email != "" {
			if !bytes.Equal(internal.HashOrgData(process.OrgAddress, req.Email), participant.HashedEmail) {
				return nil, fmt.Errorf("invalid user data")
			}
			authResp = a.twofactor.InitiateAuth(processID.String(), participant.ParticipantNo, req.Email, notifications.Email)
		} else if req.Phone != "" {
			pn, err := internal.SanitizeAndVerifyPhoneNumber(req.Phone)
			if err != nil {
				return nil, fmt.Errorf("invalid phone number")
			}
			if !bytes.Equal(internal.HashOrgData(process.OrgAddress, pn), participant.HashedPhone) {
				return nil, fmt.Errorf("invalid user data")
			}
			authResp = a.twofactor.InitiateAuth(processID.String(), participant.ParticipantNo, pn, notifications.SMS)
		} else {
			return nil, fmt.Errorf("missing email or phone")
		}
	default:
		return nil, fmt.Errorf("invalid census type")
	}

	if !authResp.Success {
		return nil, fmt.Errorf("unauthorized: %s", authResp.Error)
	}
	if authResp.AuthToken == nil {
		return nil, fmt.Errorf("internal server error: auth token is nil")
	}
	return authResp.AuthToken, nil
}
