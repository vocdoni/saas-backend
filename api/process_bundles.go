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
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.vocdoni.io/dvote/util"
)

// CreateProcessBundleRequest represents the request body for creating a process bundle
type CreateProcessBundleRequest struct {
	CensusID  string   `json:"censusId"`
	Processes []string `json:"processIds"` // Array of process creation requests
}

type CreateProcessBundleResponse struct {
	URI  string `json:"uri"`
	Root string `json:"root"`
}

// AddProcessesToBundleRequest represents the request body for adding processes to a bundle
type AddProcessesToBundleRequest struct {
	Processes []string `json:"processes"` // Array of process creation requests to add
}

// createProcessBundleHandler creates a new process bundle.
// Requires Manager/Admin role. Returns 201 on success.
func (a *API) createProcessBundleHandler(w http.ResponseWriter, r *http.Request) {
	var req CreateProcessBundleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ErrMalformedBody.Write(w)
		return
	}

	census, err := a.db.Census(req.CensusID)
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// Get the user from the request context
	user, ok := userFromContext(r.Context())
	if !ok {
		ErrUnauthorized.Write(w)
		return
	}

	// Check if the user has the necessary permissions for the organization
	if !user.HasRoleFor(census.OrgAddress, db.ManagerRole) && !user.HasRoleFor(census.OrgAddress, db.AdminRole) {
		ErrUnauthorized.Withf("user is not admin or manager of organization").Write(w)
		return
	}

	// generate a new bundle ID
	bundleID := a.db.NewBundleID()
	// The cenus root will be the same as the account's public key
	censusRoot := a.account.PubKey.String()

	if len(req.Processes) == 0 {
		// Create the process bundle
		bundle := &db.ProcessesBundle{
			ID:         bundleID,
			CensusRoot: censusRoot,
			OrgAddress: census.OrgAddress,
			Census:     *census,
		}
		_, err = a.db.SetProcessBundle(bundle)
		if err != nil {
			ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}

		httpWriteJSON(w, CreateProcessBundleResponse{
			URI:  a.serverURL + "/process/bundle/" + bundleID.Hex(),
			Root: censusRoot,
		})
		return
	}

	// Collect all processes
	var processes []internal.HexBytes

	for _, processReq := range req.Processes {
		if len(processReq) == 0 {
			ErrMalformedBody.Withf("missing process ID").Write(w)
			return
		}
		processID, err := hex.DecodeString(util.TrimHex(processReq))
		if err != nil {
			ErrMalformedBody.Withf("invalid process ID").Write(w)
			return
		}

		processes = append(processes, processID)
	}

	// Find the census participants and get them associated to the bundle
	orgParticipants, err := a.db.OrgParticipantsMemberships(census.OrgAddress, census.ID.Hex(), bundleID.Hex())
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// if a twofactor census add the process to the twofactor service
	if census.Type == db.CensusTypeSMSorMail ||
		census.Type == db.CensusTypeMail ||
		census.Type == db.CensusTypeSMS {
		if err := a.twofactor.AddProcess(census.Type, orgParticipants); err != nil {
			ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
	}

	// Create the process bundle

	bundle := &db.ProcessesBundle{
		ID:         bundleID,
		Processes:  processes,
		CensusRoot: censusRoot,
		OrgAddress: census.OrgAddress,
		Census:     *census,
	}

	_, err = a.db.SetProcessBundle(bundle)
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	httpWriteJSON(w, CreateProcessBundleResponse{
		URI:  a.serverURL + "/process/bundle/" + bundleID.Hex(),
		Root: censusRoot,
	})
}

// updateProcessBundleHandler adds processes to an existing bundle.
// Requires Manager/Admin role. Returns 200 on success.
func (a *API) updateProcessBundleHandler(w http.ResponseWriter, r *http.Request) {
	bundleIDStr := chi.URLParam(r, "bundleId")
	if bundleIDStr == "" {
		ErrMalformedURLParam.Withf("missing bundle ID").Write(w)
		return
	}

	bundleID, err := primitive.ObjectIDFromHex(bundleIDStr)
	if err != nil {
		ErrMalformedURLParam.Withf("invalid bundle ID").Write(w)
		return
	}

	var req AddProcessesToBundleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ErrMalformedBody.Write(w)
		return
	}

	if len(req.Processes) == 0 {
		ErrMalformedBody.Withf("no processes provided").Write(w)
		return
	}

	// Get the user from the request context
	user, ok := userFromContext(r.Context())
	if !ok {
		ErrUnauthorized.Write(w)
		return
	}

	// Get the existing bundle
	bundle, err := a.db.ProcessBundle(bundleID)
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// Check if the user has the necessary permissions for the organization
	if !user.HasRoleFor(bundle.OrgAddress, db.ManagerRole) && !user.HasRoleFor(bundle.OrgAddress, db.AdminRole) {
		ErrUnauthorized.Withf("user is not admin or manager of organization").Write(w)
		return
	}

	// Get the census for this bundle
	census, err := a.db.Census(bundle.Census.ID.Hex())
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// Collect all processes to add
	var processesToAdd []internal.HexBytes

	for _, processReq := range req.Processes {
		if len(processReq) == 0 {
			ErrMalformedBody.Withf("missing process ID").Write(w)
			return
		}
		processID, err := hex.DecodeString(util.TrimHex(processReq))
		if err != nil {
			ErrMalformedBody.Withf("invalid process ID").Write(w)
			return
		}

		processesToAdd = append(processesToAdd, processID)
	}

	// Find the census participants
	orgParticipants, err := a.db.OrgParticipantsMemberships(census.OrgAddress, census.ID.Hex(), bundleIDStr)
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// if a twofactor census add the process to the twofactor service
	if census.Type == db.CensusTypeSMSorMail ||
		census.Type == db.CensusTypeMail ||
		census.Type == db.CensusTypeSMS {
		if err := a.twofactor.AddProcess(census.Type, orgParticipants); err != nil {
			ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
	}

	// Add processes to the bundle
	if err := a.db.AddProcessesToBundle(bundleID, processesToAdd); err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	httpWriteJSON(w, CreateProcessBundleResponse{
		URI:  "/process/bundle/" + bundleIDStr,
		Root: bundle.CensusRoot,
	})
}

// processBundleInfoHandler retrieves process bundle information by ID.
// Returns bundle details including all processes.
func (a *API) processBundleInfoHandler(w http.ResponseWriter, r *http.Request) {
	bundleIDStr := chi.URLParam(r, "bundleId")
	if bundleIDStr == "" {
		ErrMalformedURLParam.Withf("missing bundle ID").Write(w)
		return
	}

	bundleID, err := primitive.ObjectIDFromHex(bundleIDStr)
	if err != nil {
		ErrMalformedURLParam.Withf("invalid bundle ID").Write(w)
		return
	}

	bundle, err := a.db.ProcessBundle(bundleID)
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	httpWriteJSON(w, bundle)
}

// processBundleAuthHandler handles the authentication for a process bundle.
// Similar to twofactorAuthHandler but for bundles.
func (a *API) processBundleAuthHandler(w http.ResponseWriter, r *http.Request) {
	bundleIDStr := chi.URLParam(r, "bundleId")
	if bundleIDStr == "" {
		ErrMalformedURLParam.Withf("missing bundle ID").Write(w)
		return
	}

	bundleID, err := primitive.ObjectIDFromHex(bundleIDStr)
	if err != nil {
		ErrMalformedURLParam.Withf("invalid bundle ID").Write(w)
		return
	}

	stepString := chi.URLParam(r, "step")
	step, err := strconv.Atoi(stepString)
	if err != nil || (step != 0 && step != 1) {
		ErrMalformedURLParam.Withf("wrong step ID").Write(w)
		return
	}

	bundle, err := a.db.ProcessBundle(bundleID)
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	if len(bundle.Processes) == 0 {
		ErrInvalidOrganizationData.Withf("bundle has no processes").Write(w)
		return
	}

	// Convert the bundle ID to a hex string that is our id for the twofactor service
	hexBundle, err := hex.DecodeString(bundle.ID.Hex())
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	switch step {
	case 0:

		authToken, err := a.initiateBundleAuthRequest(r, hexBundle, bundle.Census.ID.Hex())
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
		authResp := a.twofactor.Auth(hexBundle, req.AuthToken, req.AuthData)
		if !authResp.Success {
			ErrUnauthorized.WithErr(errors.New(authResp.Error)).Write(w)
			return
		}
		httpWriteJSON(w, &twofactorResponse{TokenR: authResp.TokenR})
		return
	}
}

// processBundleSignHandler handles the signing for a process bundle.
// Similar to twofactorSignHandler but for bundles.
func (a *API) processBundleSignHandler(w http.ResponseWriter, r *http.Request) {
	bundleIDStr := chi.URLParam(r, "bundleId")
	if bundleIDStr == "" {
		ErrMalformedURLParam.Withf("missing bundle ID").Write(w)
		return
	}

	bundleID, err := primitive.ObjectIDFromHex(bundleIDStr)
	if err != nil {
		ErrMalformedURLParam.Withf("invalid bundle ID").Write(w)
		return
	}

	bundle, err := a.db.ProcessBundle(bundleID)
	if err != nil {
		ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	if len(bundle.Processes) == 0 {
		ErrInvalidOrganizationData.Withf("bundle has no processes").Write(w)
		return
	}

	// Use the first process for signing
	processID := bundle.Processes[0]

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
	signResp := a.twofactor.Sign(req.TokenR, payload, processID, "ecdsa")
	if !signResp.Success {
		ErrUnauthorized.WithErr(errors.New(signResp.Error)).Write(w)
		return
	}
	httpWriteJSON(w, &twofactorResponse{Signature: signResp.Signature})
}

func (a *API) initiateBundleAuthRequest(r *http.Request, bundleId []byte, censusID string) (*uuid.UUID, error) {
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

	census, err := a.db.Census(censusID)
	if err != nil {
		return nil, ErrGenericInternalServerError.WithErr(err)
	}

	// TODO enable only password censuses
	censusType := census.Type
	if censusType != db.CensusTypeMail && censusType != db.CensusTypeSMSorMail &&
		censusType != db.CensusTypeSMS {
		return nil, ErrInvalidOrganizationData.Withf("invalid census type")
	}
	// retrieve memership info
	if _, err = a.db.CensusMembership(census.ID.Hex(), req.ParticipantNo); err != nil {
		return nil, ErrUnauthorized.Withf("participant not found in census")
	}
	// retrieve participant info
	participant, err := a.db.OrgParticipantByNo(census.OrgAddress, req.ParticipantNo)
	if err != nil {
		return nil, ErrUnauthorized.Withf("participant not found")
	}

	// first verify password
	if req.Password != "" && !bytes.Equal(internal.HashPassword(passwordSalt, req.Password), participant.HashedPass) {
		return nil, ErrUnauthorized.Withf("invalid user data")
	}

	// create sha of participantNo
	userID := make(internal.HexBytes, hex.EncodedLen(len(participant.ParticipantNo)))
	hex.Encode(userID, []byte(participant.ParticipantNo))
	var authResp twofactor.AuthResponse
	switch censusType {
	case db.CensusTypeMail:
		if req.Email == "" {
			return nil, ErrUnauthorized.Withf("missing email")
		}
		if !bytes.Equal(internal.HashOrgData(census.OrgAddress, req.Email), participant.HashedEmail) {
			return nil, ErrUnauthorized.Withf("invalid user data")
		}
		authResp = a.twofactor.InitiateAuth(bundleId, userID, req.Email, notifications.Email)
	case db.CensusTypeSMS:
		if req.Phone == "" {
			return nil, ErrUnauthorized.Withf("missing phone")
		}
		if !bytes.Equal(internal.HashOrgData(census.OrgAddress, req.Phone), participant.HashedPhone) {
			return nil, ErrUnauthorized.Withf("invalid user data")
		}
		authResp = a.twofactor.InitiateAuth(bundleId, userID, req.Phone, notifications.SMS)
	case db.CensusTypeSMSorMail:
		if req.Email != "" {
			if !bytes.Equal(internal.HashOrgData(census.OrgAddress, req.Email), participant.HashedEmail) {
				return nil, ErrUnauthorized.Withf("invalid user data")
			}
			authResp = a.twofactor.InitiateAuth(bundleId, userID, req.Email, notifications.Email)
		} else if req.Phone != "" {
			if !bytes.Equal(internal.HashOrgData(census.OrgAddress, req.Phone), participant.HashedPhone) {
				return nil, ErrUnauthorized.Withf("invalid user data")
			}
			authResp = a.twofactor.InitiateAuth(bundleId, userID, req.Phone, notifications.SMS)
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
