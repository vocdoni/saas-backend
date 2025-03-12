package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/csp/signers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/log"
)

type cspHandlers struct {
	csp    *csp.CSP
	mainDB *db.MongoStorage
}

func New(c *csp.CSP, db *db.MongoStorage) *cspHandlers {
	return &cspHandlers{
		csp:    c,
		mainDB: db,
	}
}

func (c *cspHandlers) BundleAuthHandler(w http.ResponseWriter, r *http.Request) {
	// get the bundle ID from the URL parameters
	bundleID := new(internal.HexBytes)
	if err := bundleID.ParseString(chi.URLParam(r, "bundleId")); err != nil {
		errors.ErrMalformedURLParam.Withf("invalid bundle ID").Write(w)
		return
	}
	// get the step from the URL parameters
	stepString := chi.URLParam(r, "step")
	step, err := strconv.Atoi(stepString)
	if err != nil || (step != 0 && step != 1) {
		errors.ErrMalformedURLParam.Withf("wrong step ID").Write(w)
		return
	}
	// check if the bundle exists
	bundle, err := c.mainDB.ProcessBundle(*bundleID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("bundle not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	// check if the bundle has processes
	if len(bundle.Processes) == 0 {
		errors.ErrInvalidOrganizationData.Withf("bundle has no processes").Write(w)
		return
	}
	// switch between the steps
	switch step {
	case 0:
		authToken, err := c.authFirstStep(r, *bundleID, bundle.Census.ID.Hex())
		if err != nil {
			apiErr := (err.(errors.Error))
			apiErr.Write(w)
			return
		}
		httpWriteJSON(w, &AuthResponse{AuthToken: authToken})
		return
	case 1:
		authToken, err := c.authSecondStep(r)
		if err != nil {
			apiErr := (err.(errors.Error))
			apiErr.Write(w)
			return
		}
		httpWriteJSON(w, &AuthResponse{AuthToken: authToken})
		return
	}
}

func (c *cspHandlers) BundleSignHandler(w http.ResponseWriter, r *http.Request) {
	// get the bundle ID from the URL parameters
	bundleID := new(internal.HexBytes)
	if err := bundleID.ParseString(chi.URLParam(r, "bundleId")); err != nil {
		errors.ErrMalformedURLParam.Withf("invalid bundle ID").Write(w)
		return
	}
	// get the bundle ID from the main database
	bundle, err := c.mainDB.ProcessBundle(*bundleID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("bundle not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	// check if the bundle has processes
	if len(bundle.Processes) == 0 {
		errors.ErrInvalidOrganizationData.Withf("bundle has no processes").Write(w)
		return
	}
	// parse the request from the body
	var req SignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	// check that the request contains the auth
	if req.AuthToken == nil {
		errors.ErrUnauthorized.Withf("missing auth token").Write(w)
		return
	}
	// check that the received process is part of the bundle processes
	var processId internal.HexBytes
	for _, pID := range bundle.Processes {
		if bytes.Equal(pID, req.ProcessID) {
			// process found
			processId = pID
			break
		}
	}
	// check if the process is found in the bundle
	if len(processId) == 0 {
		errors.ErrUnauthorized.Withf("process not found in bundle").Write(w)
		return
	}
	// get the address from the request payload
	address := new(internal.HexBytes)
	if err = address.ParseString(req.Payload); err != nil {
		errors.ErrMalformedBody.WithErr(err).Write(w)
		return
	}
	log.Debugw("new CSP sign request",
		"address", address,
		"procId", processId)
	// sign the request
	signature, err := c.csp.Sign(req.AuthToken, *address, processId, signers.SignerTypeEthereum)
	if err != nil {
		errors.ErrUnauthorized.WithErr(err).Write(w)
		return
	}
	httpWriteJSON(w, &AuthResponse{Signature: signature})
}

func (c *cspHandlers) authFirstStep(r *http.Request, bundleID internal.HexBytes, censusID string) (internal.HexBytes, error) {
	var req AuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, errors.ErrMalformedBody.Withf("invalid JSON request")
	}
	// check request participant number
	if len(req.ParticipantNo) == 0 {
		return nil, errors.ErrInvalidUserData.Withf("participant number not provided")
	}
	// check request contact information
	if len(req.Email) == 0 && len(req.Phone) == 0 {
		return nil, errors.ErrInvalidUserData.Withf("no contact information provided (email or phone)")
	} else if len(req.Email) > 0 && !internal.ValidEmail(req.Email) {
		return nil, errors.ErrInvalidUserData.Withf("invalid email format")
	} else if len(req.Phone) > 0 {
		var err error
		if req.Phone, err = internal.SanitizeAndVerifyPhoneNumber(req.Phone); err != nil {
			log.Warnw("invalid phone number format", "phone", req.Phone, "error", err)
			return nil, errors.ErrInvalidUserData.Withf("invalid phone number format")
		}
	}
	// get census information
	census, err := c.mainDB.Census(censusID)
	if err != nil {
		if err == db.ErrNotFound {
			return nil, errors.ErrCensusNotFound
		}
		return nil, errors.ErrGenericInternalServerError.WithErr(err)
	}
	// check the census membership of the participant
	if _, err := c.mainDB.CensusMembership(censusID, req.ParticipantNo); err != nil {
		if err == db.ErrNotFound {
			return nil, errors.ErrUnauthorized.Withf("participant not found in the census")
		}
		return nil, errors.ErrGenericInternalServerError.WithErr(err)
	}
	// get participant information
	participant, err := c.mainDB.OrgParticipantByNo(census.OrgAddress, req.ParticipantNo)
	if err != nil {
		return nil, errors.ErrCensusParticipantNotFound
	}
	// check the password
	if req.Password != "" && !bytes.Equal(internal.HashPassword(c.csp.PasswordSalt, req.Password), participant.HashedPass) {
		return nil, fmt.Errorf("invalid user data")
	}
	// check the census type and parse the destination and notification type
	// accordingly
	var toDestinations string
	var challengeType notifications.ChallengeType
	switch census.Type {
	case db.CensusTypeMail:
		if !bytes.Equal(internal.HashOrgData(census.OrgAddress, req.Email), participant.HashedEmail) {
			return nil, errors.ErrUnauthorized.Withf("invalid user email")
		}
		toDestinations = req.Email
		challengeType = notifications.EmailChallenge
	case db.CensusTypeSMS:
		if !bytes.Equal(internal.HashOrgData(census.OrgAddress, req.Phone), participant.HashedPhone) {
			return nil, errors.ErrUnauthorized.Withf("invalid user phone")
		}
		toDestinations = req.Phone
		challengeType = notifications.SMSChallenge
	case db.CensusTypeSMSorMail:
		if req.Email != "" {
			if !bytes.Equal(internal.HashOrgData(census.OrgAddress, req.Email), participant.HashedEmail) {
				return nil, errors.ErrUnauthorized.Withf("invalid user email")
			}
			toDestinations = req.Email
			challengeType = notifications.EmailChallenge
		} else if req.Phone != "" {
			if !bytes.Equal(internal.HashOrgData(census.OrgAddress, req.Phone), participant.HashedPhone) {
				return nil, errors.ErrUnauthorized.Withf("invalid user phone")
			}
			toDestinations = req.Phone
			challengeType = notifications.SMSChallenge
		}
	default:
		return nil, errors.ErrNotSupported.Withf("invalid census type")
	}
	// parse the user ID and generate the token
	userID := new(internal.HexBytes).SetString(participant.ID.Hex())
	return c.csp.BundleAuthToken(bundleID, *userID, toDestinations, challengeType)
}

func (c *cspHandlers) authSecondStep(r *http.Request) (internal.HexBytes, error) {
	var req AuthChallengeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, errors.ErrMalformedBody.Withf("invalid JSON request")
	}
	if err := c.csp.VerifyBundleAuthToken(req.AuthToken, req.AuthData[0]); err != nil {
		switch err {
		case csp.ErrInvalidAuthToken, csp.ErrInvalidSolution, csp.ErrChallengeCodeFailure:
			return nil, errors.ErrUnauthorized.WithErr(err)
		case csp.ErrUserUnknown, csp.ErrUserNotBelongsToBundle:
			return nil, errors.ErrUserNotFound.WithErr(err)
		case csp.ErrStorageFailure:
			return nil, errors.ErrInternalStorageError.WithErr(err)
		default:
			return nil, errors.ErrGenericInternalServerError.WithErr(err)
		}
	}
	return req.AuthToken, nil
}
