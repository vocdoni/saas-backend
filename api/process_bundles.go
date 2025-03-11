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
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/dvote/util"
)

// CreateProcessBundleRequest represents the request body for creating a process bundle.
// It contains the census ID and an optional array of process IDs to include in the bundle.
type CreateProcessBundleRequest struct {
	CensusID  string   `json:"censusId"`
	Processes []string `json:"processIds"` // Array of process creation requests
}

// CreateProcessBundleResponse represents the response returned after successfully creating a process bundle.
// It includes the URI to access the bundle and the census root public key.
type CreateProcessBundleResponse struct {
	URI  string `json:"uri"`  // The URI to access the bundle
	Root string `json:"root"` // The census root public key
}

// AddProcessesToBundleRequest represents the request body for adding processes to an existing bundle.
// It contains an array of process IDs to add to the bundle.
type AddProcessesToBundleRequest struct {
	Processes []string `json:"processes"` // Array of process creation requests to add
}

// createProcessBundleHandler creates a new process bundle with the specified census and optional list of processes.
// Requires Manager/Admin role for the organization that owns the census. Returns 201 on success.
// The census root will be the same as the account's public key.
func (a *API) createProcessBundleHandler(w http.ResponseWriter, r *http.Request) {
	var req CreateProcessBundleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}

	census, err := a.db.Census(req.CensusID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("census not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// Get the user from the request context
	user, ok := userFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	// Check if the user has the necessary permissions for the organization
	if !user.HasRoleFor(census.OrgAddress, db.ManagerRole) && !user.HasRoleFor(census.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of organization").Write(w)
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
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
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
			errors.ErrMalformedBody.Withf("missing process ID").Write(w)
			return
		}
		processID, err := hex.DecodeString(util.TrimHex(processReq))
		if err != nil {
			errors.ErrMalformedBody.Withf("invalid process ID").Write(w)
			return
		}

		processes = append(processes, processID)
	}

	// Find the census participants and get them associated to the bundle
	orgParticipants, err := a.db.OrgParticipantsMemberships(census.OrgAddress, census.ID.Hex(), bundleID.Hex(), processes)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// if a twofactor census add the process to the twofactor service
	if census.Type == db.CensusTypeSMSorMail ||
		census.Type == db.CensusTypeMail ||
		census.Type == db.CensusTypeSMS {
		if err := a.twofactor.AddProcess(census.Type, orgParticipants); err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
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
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	httpWriteJSON(w, CreateProcessBundleResponse{
		URI:  a.serverURL + "/process/bundle/" + bundleID.Hex(),
		Root: censusRoot,
	})
}

// updateProcessBundleHandler adds additional processes to an existing bundle.
// Requires Manager/Admin role for the organization that owns the bundle. Returns 200 on success.
// The processes array in the request must not be empty.
func (a *API) updateProcessBundleHandler(w http.ResponseWriter, r *http.Request) {
	bundleIDStr := chi.URLParam(r, "bundleId")
	if bundleIDStr == "" {
		errors.ErrMalformedURLParam.Withf("missing bundle ID").Write(w)
		return
	}

	bundleID, err := primitive.ObjectIDFromHex(bundleIDStr)
	if err != nil {
		errors.ErrMalformedURLParam.Withf("invalid bundle ID").Write(w)
		return
	}

	var req AddProcessesToBundleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}

	// Get the user from the request context
	user, ok := userFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	// Get the existing bundle
	bundle, err := a.db.ProcessBundle(bundleID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("bundle not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	if len(req.Processes) == 0 {
		httpWriteJSON(w, CreateProcessBundleResponse{
			URI:  "/process/bundle/" + bundleIDStr,
			Root: bundle.CensusRoot,
		})
		return
	}

	// Check if the user has the necessary permissions for the organization
	if !user.HasRoleFor(bundle.OrgAddress, db.ManagerRole) && !user.HasRoleFor(bundle.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of organization").Write(w)
		return
	}

	// Get the census for this bundle
	census, err := a.db.Census(bundle.Census.ID.Hex())
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("census not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// Collect all processes to add
	var processesToAdd []internal.HexBytes

	for _, processReq := range req.Processes {
		if len(processReq) == 0 {
			errors.ErrMalformedBody.Withf("missing process ID").Write(w)
			return
		}
		processID, err := hex.DecodeString(util.TrimHex(processReq))
		if err != nil {
			errors.ErrMalformedBody.Withf("invalid process ID").Write(w)
			return
		}

		processesToAdd = append(processesToAdd, processID)
	}

	// Find the census participants
	orgParticipants, err := a.db.OrgParticipantsMemberships(census.OrgAddress, census.ID.Hex(), bundleIDStr, processesToAdd)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// if a twofactor census add the process to the twofactor service
	if census.Type == db.CensusTypeSMSorMail ||
		census.Type == db.CensusTypeMail ||
		census.Type == db.CensusTypeSMS {
		if err := a.twofactor.AddProcess(census.Type, orgParticipants); err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
	}

	// Add processes to the bundle
	if err := a.db.AddProcessesToBundle(bundleID, processesToAdd); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	httpWriteJSON(w, CreateProcessBundleResponse{
		URI:  "/process/bundle/" + bundleIDStr,
		Root: bundle.CensusRoot,
	})
}

// processBundleInfoHandler retrieves process bundle information by ID.
// Returns bundle details including the associated census, census root, organization address, and list of processes.
func (a *API) processBundleInfoHandler(w http.ResponseWriter, r *http.Request) {
	bundleIDStr := chi.URLParam(r, "bundleId")
	if bundleIDStr == "" {
		errors.ErrMalformedURLParam.Withf("missing bundle ID").Write(w)
		return
	}

	bundleID, err := primitive.ObjectIDFromHex(bundleIDStr)
	if err != nil {
		errors.ErrMalformedURLParam.Withf("invalid bundle ID").Write(w)
		return
	}

	bundle, err := a.db.ProcessBundle(bundleID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("bundle not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	httpWriteJSON(w, bundle)
}

// processBundleParticipantInfoHandler retrieves process information for a participant in a process bundle.
// Returns process details including the census and metadata.
func (a *API) processBundleParticipantInfoHandler(w http.ResponseWriter, r *http.Request) {
	bundleIDStr := chi.URLParam(r, "bundleId")
	if bundleIDStr == "" {
		errors.ErrMalformedURLParam.Withf("missing bundle ID").Write(w)
		return
	}

	bundleID, err := primitive.ObjectIDFromHex(bundleIDStr)
	if err != nil {
		errors.ErrMalformedURLParam.Withf("invalid bundle ID").Write(w)
		return
	}

	_, err = a.db.ProcessBundle(bundleID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("bundle not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	participantID := chi.URLParam(r, "participantId")
	if participantID == "" {
		errors.ErrMalformedURLParam.Withf("missing participant ID").Write(w)
		return
	}

	elections := a.twofactor.Indexer(participantID, bundleIDStr, "")
	if len(elections) == 0 {
		httpWriteJSON(w, []twofactor.Election{})
		return
	}

	httpWriteJSON(w, elections)
}

// processBundleAuthHandler handles the two-step authentication process for voters participating in a bundle of processes.
// Similar to twofactorAuthHandler but for bundles. Step 0 initiates the authentication process and returns an auth token.
// Step 1 completes the authentication by providing the verification code or other authentication data.
func (a *API) processBundleAuthHandler(w http.ResponseWriter, r *http.Request) {
	bundleIDStr := chi.URLParam(r, "bundleId")
	if bundleIDStr == "" {
		errors.ErrMalformedURLParam.Withf("missing bundle ID").Write(w)
		return
	}

	bundleID, err := primitive.ObjectIDFromHex(bundleIDStr)
	if err != nil {
		errors.ErrMalformedURLParam.Withf("invalid bundle ID").Write(w)
		return
	}

	stepString := chi.URLParam(r, "step")
	step, err := strconv.Atoi(stepString)
	if err != nil || (step != 0 && step != 1) {
		errors.ErrMalformedURLParam.Withf("wrong step ID").Write(w)
		return
	}

	bundle, err := a.db.ProcessBundle(bundleID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("bundle not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	if len(bundle.Processes) == 0 {
		errors.ErrInvalidOrganizationData.Withf("bundle has no processes").Write(w)
		return
	}

	switch step {
	case 0:
		authToken, err := a.initiateBundleAuthRequest(r, bundleID.Hex(), bundle.Census.ID.Hex())
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
		authResp := a.twofactor.Auth(bundleID.Hex(), req.AuthToken, req.AuthData)
		if !authResp.Success {
			errors.ErrUnauthorized.WithErr(stderrors.New(authResp.Error)).Write(w)
			return
		}
		httpWriteJSON(w, &twofactorResponse{AuthToken: authResp.AuthToken})
		return
	}
}

// processBundleSignHandler handles the signing of a payload for a process bundle using two-factor authentication.
// Similar to twofactorSignHandler but for bundles. Requires a valid tokenR obtained from the process bundle authentication.
// The signing uses the first process in the bundle for the signature.
func (a *API) processBundleSignHandler(w http.ResponseWriter, r *http.Request) {
	bundleIDStr := chi.URLParam(r, "bundleId")
	if bundleIDStr == "" {
		errors.ErrMalformedURLParam.Withf("missing bundle ID").Write(w)
		return
	}

	bundleID, err := primitive.ObjectIDFromHex(bundleIDStr)
	if err != nil {
		errors.ErrMalformedURLParam.Withf("invalid bundle ID").Write(w)
		return
	}

	bundle, err := a.db.ProcessBundle(bundleID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("bundle not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	if len(bundle.Processes) == 0 {
		errors.ErrInvalidOrganizationData.Withf("bundle has no processes").Write(w)
		return
	}

	var req SignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}

	if req.AuthToken == nil {
		errors.ErrUnauthorized.Withf("missing auth token").Write(w)
		return
	}

	// check that the received process is part of the bundle processes
	var procId internal.HexBytes
	for _, processID := range bundle.Processes {
		if bytes.Equal(processID, req.ElectionId) {
			// process found
			procId = processID
			break
		}
	}
	if len(procId) == 0 {
		errors.ErrUnauthorized.Withf("process not found in bundle").Write(w)
		return
	}

	address, err := hex.DecodeString(util.TrimHex(req.Payload))
	if err != nil {
		errors.ErrMalformedBody.WithErr(err).Write(w)
		return
	}

	log.Debugw("new CSP sign request", "address", fmt.Sprintf("%x", address), "procId", procId.String())
	signResp := a.twofactor.Sign(*req.AuthToken, req.TokenR, address, procId, bundleIDStr, "ecdsa")
	if !signResp.Success {
		errors.ErrUnauthorized.WithErr(stderrors.New(signResp.Error)).Write(w)
		return
	}
	httpWriteJSON(w, &twofactorResponse{Signature: signResp.Signature})
}

// initiateBundleAuthRequest initiates the authentication process for a bundle.
// It validates the participant's credentials against the census and returns an auth token if successful.
// Authentication can be done via email, phone number, or password depending on the census type.
func (a *API) initiateBundleAuthRequest(r *http.Request, bundleId string, censusID string) (*uuid.UUID, error) {
	var req InitiateAuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, fmt.Errorf("malformed body")
	}
	if len(req.ParticipantNo) == 0 {
		return nil, fmt.Errorf("missing participant number")
	}

	if len(req.Email) == 0 && len(req.Phone) == 0 {
		return nil, fmt.Errorf("missing auth data")
	}

	census, err := a.db.Census(censusID)
	if err != nil {
		if err == db.ErrNotFound {
			return nil, fmt.Errorf("census not found")
		}
		return nil, fmt.Errorf("internal server error")
	}

	// TODO enable only password censuses
	censusType := census.Type
	if censusType != db.CensusTypeMail && censusType != db.CensusTypeSMSorMail &&
		censusType != db.CensusTypeSMS {
		return nil, fmt.Errorf("invalid census type")
	}
	// retrieve memership info
	if _, err = a.db.CensusMembership(census.ID.Hex(), req.ParticipantNo); err != nil {
		return nil, fmt.Errorf("participant not found in census")
	}
	// retrieve participant info
	participant, err := a.db.OrgParticipantByNo(census.OrgAddress, req.ParticipantNo)
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
		if !internal.ValidEmail(req.Email) {
			return nil, fmt.Errorf("invalid email")
		}
		if !bytes.Equal(internal.HashOrgData(census.OrgAddress, req.Email), participant.HashedEmail) {
			return nil, fmt.Errorf("invalid user data")
		}
		authResp = a.twofactor.InitiateAuth(bundleId, participant.ParticipantNo, req.Email, notifications.Email)
	case db.CensusTypeSMS:
		if req.Phone == "" {
			return nil, fmt.Errorf("missing phone")
		}
		pn, err := internal.SanitizeAndVerifyPhoneNumber(req.Phone)
		if err != nil {
			return nil, fmt.Errorf("invalid phone number")
		}
		if !bytes.Equal(internal.HashOrgData(census.OrgAddress, pn), participant.HashedPhone) {
			return nil, fmt.Errorf("invalid user data")
		}
		authResp = a.twofactor.InitiateAuth(bundleId, participant.ParticipantNo, pn, notifications.SMS)
	case db.CensusTypeSMSorMail:
		if internal.ValidEmail(req.Email) {
			if !bytes.Equal(internal.HashOrgData(census.OrgAddress, req.Email), participant.HashedEmail) {
				return nil, fmt.Errorf("invalid user data")
			}
			authResp = a.twofactor.InitiateAuth(bundleId, participant.ParticipantNo, req.Email, notifications.Email)
		} else if req.Phone != "" {
			pn, err := internal.SanitizeAndVerifyPhoneNumber(req.Phone)
			if err != nil {
				return nil, fmt.Errorf("invalid phone number")
			}
			if !bytes.Equal(internal.HashOrgData(census.OrgAddress, pn), participant.HashedPhone) {
				return nil, fmt.Errorf("invalid user data")
			}
			authResp = a.twofactor.InitiateAuth(bundleId, participant.ParticipantNo, pn, notifications.SMS)
		} else {
			return nil, fmt.Errorf("missing email or phone")
		}
	default:
		return nil, fmt.Errorf("invalid census type")
	}

	if !authResp.Success {
		return nil, fmt.Errorf("authentication failed")
	}
	if authResp.AuthToken == nil {
		return nil, fmt.Errorf("auth token is nil")
	}
	return authResp.AuthToken, nil
}
