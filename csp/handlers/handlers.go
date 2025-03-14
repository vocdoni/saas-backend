package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/csp/signers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/dvote/vochain/state"
)

// cspHandlers is a struct that contains an instance of the CSP and the main
// database (where the bundle and census data is stored). It is used to handle
// the CSP API requests such as the authentication and signing of the bundle
// processes.
type cspHandlers struct {
	csp    *csp.CSP
	mainDB *db.MongoStorage
}

// New creates a new instance of the CSP handlers instance. It receives the CSP
// instance and the main database instance as parameters.
func New(c *csp.CSP, mainDB *db.MongoStorage) *cspHandlers {
	return &cspHandlers{
		csp:    c,
		mainDB: mainDB,
	}
}

// BundleAuthHandler godoc
//	@Summary		Authenticate for a process bundle
//	@Description	Handle authentication for a process bundle. There are two steps in the authentication process:
//	@Description	- Step 0: The user sends the participant number and contact information (email or phone).
//	@Description	If valid, the server sends a challenge to the user with a token.
//	@Description	- Step 1: The user sends the token and challenge solution back to the server.
//	@Description	If valid, the token is marked as verified and returned.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Param			bundleId	path		string		true	"Bundle ID"
//	@Param			step		path		string		true	"Authentication step (0 or 1)"
//	@Param			request		body		interface{}	true	"Authentication request (varies by step)"
//	@Success		200			{object}	handlers.AuthResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		404			{object}	errors.Error	"Bundle not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/process/bundle/{bundleId}/auth/{step} [post]
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
			if apiErr, ok := err.(errors.Error); ok {
				apiErr.Write(w)
				return
			}
			errors.ErrUnauthorized.WithErr(err).Write(w)
			return
		}
		apicommon.HttpWriteJSON(w, &AuthResponse{AuthToken: authToken})
		return
	case 1:
		authToken, err := c.authSecondStep(r)
		if err != nil {
			if apiErr, ok := err.(errors.Error); ok {
				apiErr.Write(w)
				return
			}
			errors.ErrUnauthorized.WithErr(err).Write(w)
			return
		}
		apicommon.HttpWriteJSON(w, &AuthResponse{AuthToken: authToken})
		return
	}
}

// BundleSignHandler godoc
//	@Summary		Sign a process in a bundle
//	@Description	Sign a process in a bundle. Requires a verified token. The server signs the address with the user data
//	@Description	and returns the signature. Once signed, the process is marked as consumed and cannot be signed again.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Param			bundleId	path		string					true	"Bundle ID"
//	@Param			request		body		handlers.SignRequest	true	"Sign request with process ID, auth token, and payload"
//	@Success		200			{object}	handlers.AuthResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data"
//	@Failure		401			{object}	errors.Error	"Unauthorized or invalid token"
//	@Failure		404			{object}	errors.Error	"Bundle not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/process/bundle/{bundleId}/sign [post]
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
	signature, err := c.csp.Sign(req.AuthToken, *address, processId, signers.SignerTypeECDSASalted)
	if err != nil {
		errors.ErrUnauthorized.WithErr(err).Write(w)
		return
	}
	apicommon.HttpWriteJSON(w, &AuthResponse{Signature: signature})
}

// ConsumedAddressHandler godoc
//	@Summary		Get the address used to sign a process
//	@Description	Get the address used to sign a process. Requires a verified token. Returns the address, nullifier,
//	@Description	and timestamp of the consumption.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Param			processId	path		string							true	"Process ID"
//	@Param			request		body		handlers.ConsumedAddressRequest	true	"Request with auth token"
//	@Success		200			{object}	handlers.ConsumedAddressResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data"
//	@Failure		401			{object}	errors.Error	"Unauthorized or invalid token"
//	@Failure		404			{object}	errors.Error	"Process not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/process/{processId}/sign-info [post]
func (c *cspHandlers) ConsumedAddressHandler(w http.ResponseWriter, r *http.Request) {
	// get the bundle ID from the URL parameters
	processID := new(internal.HexBytes)
	if err := processID.ParseString(chi.URLParam(r, "processId")); err != nil {
		errors.ErrMalformedURLParam.WithErr(csp.ErrNoBundleID).Write(w)
		return
	}
	// parse the request from the body
	var req ConsumedAddressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	// get the user data from the token
	authToken, userData, err := c.csp.Storage.UserAuthToken(req.AuthToken)
	if err != nil {
		log.Warnw("error getting user data by token",
			"error", err,
			"token", req.AuthToken)
		errors.ErrUnauthorized.WithErr(err).Write(w)
		return
	}
	// check if the token is verified
	if !authToken.Verified {
		errors.ErrUnauthorized.WithErr(csp.ErrAuthTokenNotVerified).Write(w)
		return
	}
	// get the bundle from the user data
	bundle, ok := userData.Bundles[authToken.BundleID.String()]
	if !ok {
		log.Warnw("bundle not found in user data",
			"bundleID", authToken.BundleID,
			"token", req.AuthToken,
			"userID", userData.ID)
		errors.ErrUnauthorized.WithErr(csp.ErrUserNotBelongsToBundle).Write(w)
		return
	}
	// get the process from the bundle
	process, ok := bundle.Processes[processID.String()]
	if !ok {
		log.Warnw("process not found in bundle",
			"processID", processID,
			"bundleID", authToken.BundleID,
			"token", req.AuthToken,
			"userID", userData.ID)
		errors.ErrUnauthorized.WithErr(csp.ErrUserNotBelongsToProcess).Write(w)
		return
	}
	// check if the process has been consumed and return error if not
	if !process.Consumed {
		errors.ErrUserNoVoted.Write(w)
		return
	}
	// return the address used to sign the process and the nullifier
	apicommon.HttpWriteJSON(w, &ConsumedAddressResponse{
		Address:   process.WithAddress,
		Nullifier: state.GenerateNullifier(common.BytesToAddress(process.WithAddress), process.ID),
		At:        process.At,
	})
}

// authFirstStep is the first step of the authentication process. It receives
// the request, the bundle ID and the census ID as parameters. It checks the
// request data (participant number, email and phone) against the census data.
// If the data is valid, it generates a token with the bundle ID, the
// participant number as the user ID, the contact information as the
// destination and the challenge type. It returns the token and an error if
// any. It sends the challenge to the user (email or SMS) to verify the user
// token in the second step.
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
	// generate the token with the bundle ID, the participant number as the
	// user ID, the contact information as the destination, and the challenge
	// type
	return c.csp.BundleAuthToken(bundleID, internal.HexBytes(participant.ParticipantNo), toDestinations, challengeType)
}

// authSecondStep is the second step of the authentication process. It
// receives the request and checks the token and the challenge solution
// against the server data. If the data is valid, it returns the token and
// an error if any. It the solution is valid, the token is marked as verified
// and returned to the user. The user can use the token to sign the bundle
// processes.
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
