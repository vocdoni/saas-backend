// Package handlers provides HTTP handlers for the CSP
// API endpoints, managing authentication, token verification, and cryptographic
// signing operations for process bundles in the Vocdoni voting platform.
package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/csp/signers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/dvote/vochain/state"
)

const (
	DefaultOrgName = "Vocdoni"
	DefaultOrgLogo = "https://tomato-giant-grasshopper-196.mypinata.cloud/ipfs/" +
		"bafkreifqyu5m5as4gvcirlog5j267um24q7y4ri6r3svhsi7fda24676ny"
)

// CSPHandlers is a struct that contains an instance of the CSP and the main
// database (where the bundle and census data is stored). It is used to handle
// the CSP API requests such as the authentication and signing of the bundle
// processes.
type CSPHandlers struct {
	csp    *csp.CSP
	mainDB *db.MongoStorage
}

// New creates a new instance of the CSP handlers instance. It receives the CSP
// instance and the main database instance as parameters.
func New(c *csp.CSP, mainDB *db.MongoStorage) *CSPHandlers {
	return &CSPHandlers{
		csp:    c,
		mainDB: mainDB,
	}
}

// parseBundleID parses the bundle ID from the URL parameters
func parseBundleID(w http.ResponseWriter, r *http.Request) (*internal.HexBytes, bool) {
	bundleID := new(internal.HexBytes)
	if err := bundleID.ParseString(chi.URLParam(r, "bundleId")); err != nil {
		errors.ErrMalformedURLParam.Withf("invalid bundle ID").Write(w)
		return nil, false
	}
	return bundleID, true
}

// parseAuthStep parses the authentication step from the URL parameters
func parseAuthStep(w http.ResponseWriter, r *http.Request) (int, bool) {
	stepString := chi.URLParam(r, "step")
	step, err := strconv.Atoi(stepString)
	if err != nil || (step != 0 && step != 1) {
		errors.ErrMalformedURLParam.Withf("wrong step ID").Write(w)
		return 0, false
	}
	return step, true
}

// getBundle retrieves the bundle from the database
func (c *CSPHandlers) getBundle(w http.ResponseWriter, bundleID internal.HexBytes) (*db.ProcessesBundle, bool) {
	bundle, err := c.mainDB.ProcessBundle(bundleID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("bundle not found").Write(w)
			return nil, false
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return nil, false
	}

	// Check if the bundle has processes
	if len(bundle.Processes) == 0 {
		errors.ErrInvalidOrganizationData.Withf("bundle has no processes").Write(w)
		return nil, false
	}

	return bundle, true
}

// handleAuthStep handles the authentication step and writes the response
func (c *CSPHandlers) handleAuthStep(w http.ResponseWriter, r *http.Request,
	step int, bundleID internal.HexBytes, censusID string,
) {
	var authToken internal.HexBytes
	var err error

	if step == 0 {
		authToken, err = c.authFirstStep(r, bundleID, censusID)
	} else {
		authToken, err = c.authSecondStep(r)
	}

	if err != nil {
		if apiErr, ok := err.(errors.Error); ok {
			apiErr.Write(w)
			return
		}
		errors.ErrUnauthorized.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, &AuthResponse{AuthToken: authToken})
}

// BundleAuthHandler godoc
//
//	@Summary		Authenticate for a process bundle
//	@Description	Handle authentication for a process bundle. There are two steps in the authentication process:
//	@Description	- Step 0: The user sends the participant ID and contact information (email or phone).
//	@Description	If valid, the server sends a challenge to the user with a token.
//	@Description	- Step 1: The user sends the token and challenge solution back to the server.
//	@Description	If valid, the token is marked as verified and returned.
//	@Description	For auth-only censuses, verification may not require a challenge solution.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Param			bundleId	path		string		true	"Bundle ID"
//	@Param			step		path		string		true	"Authentication step (0 or 1)"
//	@Param			request		body		interface{}	true	"Authentication request (varies by step)"
//	@Success		200			{object}	handlers.AuthResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data"
//	@Failure		401			{object}	errors.Error	"Unauthorized, cooldown time not reached (ErrAttemptCoolDownTime), or invalid challenge"
//	@Failure		404			{object}	errors.Error	"Bundle not found, census not found, organization not found"
//	@Failure		404			{object}	errors.Error	"census participant not found (ErrCensusParticipantNotFound)"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/process/bundle/{bundleId}/auth/{step} [post]
func (c *CSPHandlers) BundleAuthHandler(w http.ResponseWriter, r *http.Request) {
	// Parse the bundle ID and authentication step
	bundleID, ok := parseBundleID(w, r)
	if !ok {
		return
	}

	step, ok := parseAuthStep(w, r)
	if !ok {
		return
	}

	// Get the bundle
	bundle, ok := c.getBundle(w, *bundleID)
	if !ok {
		return
	}

	// Handle the authentication step
	c.handleAuthStep(w, r, step, *bundleID, bundle.Census.ID.Hex())
}

// parseSignRequest parses the sign request from the request body
func parseSignRequest(w http.ResponseWriter, r *http.Request) (*SignRequest, bool) {
	var req SignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return nil, false
	}

	// Check that the request contains the auth token
	if req.AuthToken == nil {
		errors.ErrUnauthorized.Withf("missing auth token").Write(w)
		return nil, false
	}

	return &req, true
}

// getAuthInfo retrieves the authentication information from the token
func (c *CSPHandlers) getAuthInfo(w http.ResponseWriter, authToken internal.HexBytes) (*db.CSPAuth, bool) {
	auth, err := c.csp.Storage.CSPAuth(authToken)
	if err != nil {
		errors.ErrUnauthorized.WithErr(err).Write(w)
		return nil, false
	}
	return auth, true
}

// findProcessInBundle checks if the process is part of the bundle
func findProcessInBundle(bundle *db.ProcessesBundle, processID internal.HexBytes) (internal.HexBytes, bool) {
	for _, pID := range bundle.Processes {
		if bytes.Equal(pID, processID) {
			return pID, true
		}
	}
	return nil, false
}

// parseAddress parses the address from the payload
func parseAddress(w http.ResponseWriter, payload string) (*internal.HexBytes, bool) {
	address := new(internal.HexBytes)
	if err := address.ParseString(payload); err != nil {
		errors.ErrMalformedBody.WithErr(err).Write(w)
		return nil, false
	}
	return address, true
}

// signAndRespond signs the request and sends the response
func (c *CSPHandlers) signAndRespond(w http.ResponseWriter, authToken, address, processID, weight internal.HexBytes) {
	log.Debugw("new CSP sign request", "address", address, "procId", processID, "weight", weight)

	signature, err := c.csp.Sign(authToken, address, processID, weight, signers.SignerTypeECDSASalted)
	if err != nil {
		errors.ErrUnauthorized.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, &AuthResponse{Signature: signature, Weight: weight})
}

// BundleSignHandler godoc
//
//	@Summary		Sign a process in a bundle
//	@Description	Sign a process in a bundle. Requires a verified token. The server signs the address with the user data
//	@Description	and returns the signature. Once signed, the process is marked as consumed and cannot be signed again.
//	@Description	The signing process includes verifying that the participant is in the census, that the process is part of
//	@Description	the bundle, and that the authentication token is valid and verified.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Param			bundleId	path		string					true	"Bundle ID"
//	@Param			request		body		handlers.SignRequest	true	"Sign request with process ID, auth token, and payload (address)"
//	@Success		200			{object}	handlers.AuthResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data"
//	@Failure		401			{object}	errors.Error	"Unauthorized, invalid token, or token not verified (ErrAuthTokenNotVerified)"
//	@Failure		404			{object}	errors.Error	"Bundle not found, process not in bundle, or user not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/process/bundle/{bundleId}/sign [post]
func (c *CSPHandlers) BundleSignHandler(w http.ResponseWriter, r *http.Request) {
	// Parse the bundle ID
	bundleID, ok := parseBundleID(w, r)
	if !ok {
		return
	}

	// Get the bundle
	bundle, ok := c.getBundle(w, *bundleID)
	if !ok {
		return
	}

	// Parse the sign request
	req, ok := parseSignRequest(w, r)
	if !ok {
		return
	}

	// Get the authentication information
	auth, ok := c.getAuthInfo(w, req.AuthToken)
	if !ok {
		return
	}
	if !auth.Verified {
		errors.ErrUnauthorized.WithErr(csp.ErrAuthTokenNotVerified).Write(w)
		return
	}

	// Find the process in the bundle
	processID, found := findProcessInBundle(bundle, req.ProcessID)
	if !found {
		errors.ErrUnauthorized.Withf("process not found in bundle").Write(w)
		return
	}

	oid, err := primitive.ObjectIDFromHex(auth.UserID.String())
	if err != nil {
		errors.ErrUnauthorized.WithErr(fmt.Errorf("invalid user ID in token: %w", err)).Write(w)
		return
	}
	member, err := c.mainDB.OrgMember(bundle.OrgAddress, oid.Hex())
	if err != nil {
		errors.ErrUserNotFound.WithErr(err).Write(w)
		return
	}

	census, err := c.mainDB.Census(bundle.Census.ID.Hex())
	if err != nil {
		errors.ErrCensusNotFound.WithErr(err).Write(w)
		return
	}

	// default weight to 1 if not set
	weight := uint64(1)
	if census.Weighted {
		weight = member.Weight
	}

	// // Check if the participant is in the census
	// if !c.checkCensusParticipant(w, bundle.Census.ID.Hex(), string(auth.UserID)) {
	// 	return
	// }

	// Parse the address from the payload
	address, ok := parseAddress(w, req.Payload)
	if !ok {
		return
	}
	// Sign the request and send the response
	c.signAndRespond(w, req.AuthToken, *address, processID, big.NewInt(int64(weight)).Bytes())
}

// UserWeightHandler godoc
//
//	@Summary		Get user weight for a bundle
//	@Description	Get the weight of a user for a given bundle. Requires a verified token.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Param			bundleId	path		string						true	"Bundle ID"
//	@Param			request		body		handlers.UserWeightRequest	true	"Request with auth token"
//	@Success		200			{object}	handlers.UserWeightResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data"
//	@Failure		401			{object}	errors.Error	"Unauthorized, invalid token, token not verified (ErrAuthTokenNotVerified)"
//	@Failure		401			{object}	errors.Error	"token not belonging to bundle"
//	@Failure		404			{object}	errors.Error	"Bundle not found, user not found, or census not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/process/bundle/{bundleId}/weight [post]
func (c *CSPHandlers) UserWeightHandler(w http.ResponseWriter, r *http.Request) {
	// get the bundle ID from the URL parameters
	bundleID, ok := parseBundleID(w, r)
	if !ok {
		return
	}
	// parse the request from the body
	var req UserWeightRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	// get the user data from the token
	auth, ok := c.getAuthInfo(w, req.AuthToken)
	if !ok {
		return
	}
	if !bytes.Equal(*bundleID, auth.BundleID) {
		errors.ErrUnauthorized.Withf("token does not belong to the bundle").Write(w)
		return
	}
	// check if the token is verified
	if !auth.Verified {
		errors.ErrUnauthorized.WithErr(csp.ErrAuthTokenNotVerified).Write(w)
		return
	}

	bundle, ok := c.getBundle(w, *bundleID)
	if !ok {
		return
	}
	oid, err := primitive.ObjectIDFromHex(auth.UserID.String())
	if err != nil {
		errors.ErrUnauthorized.WithErr(fmt.Errorf("invalid user ID in token: %w", err)).Write(w)
		return
	}
	member, err := c.mainDB.OrgMember(bundle.OrgAddress, oid.Hex())
	if err != nil {
		errors.ErrUserNotFound.WithErr(err).Write(w)
		return
	}
	census, err := c.mainDB.Census(bundle.Census.ID.Hex())
	if err != nil {
		errors.ErrCensusNotFound.WithErr(err).Write(w)
		return
	}

	// default weight to 1 if not set
	weight := uint64(1)
	if census.Weighted {
		weight = member.Weight
	}

	// return the user weight for the bundle
	apicommon.HTTPWriteJSON(w, &UserWeightResponse{
		Weight: internal.HexBytes(big.NewInt(int64(weight)).Bytes()),
	})
}

// ConsumedAddressHandler godoc
//
//	@Summary		Get the address used to sign a process
//	@Description	Get the address used to sign a process. Requires a verified token. Returns the address, nullifier,
//	@Description	and timestamp of the consumption.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Param			processId	path		string							true	"Process ID"
//	@Param			request		body		handlers.ConsumedAddressRequest	true	"Request with auth token"
//	@Success		200			{object}	handlers.ConsumedAddressResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data or user has not voted (ErrUserNoVoted)"
//	@Failure		401			{object}	errors.Error	"Unauthorized, invalid token, or token not verified (ErrAuthTokenNotVerified)"
//	@Failure		404			{object}	errors.Error	"Process not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/process/{processId}/sign-info [post]
func (c *CSPHandlers) ConsumedAddressHandler(w http.ResponseWriter, r *http.Request) {
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
	auth, err := c.csp.Storage.CSPAuth(req.AuthToken)
	if err != nil {
		log.Warnw("error getting user data by token",
			"error", err,
			"token", req.AuthToken)
		errors.ErrUnauthorized.WithErr(err).Write(w)
		return
	}
	// check if the token is verified
	if !auth.Verified {
		errors.ErrUnauthorized.WithErr(csp.ErrAuthTokenNotVerified).Write(w)
		return
	}
	cspProcess, err := c.csp.Storage.CSPProcess(auth.Token, *processID)
	if err != nil {
		log.Warnw("error getting user data by token",
			"error", err,
			"token", req.AuthToken)
		errors.ErrUnauthorized.WithErr(err).Write(w)
		return
	}
	// check if the address has voted at least one time and return error if not
	if !cspProcess.Used {
		errors.ErrUserNoVoted.Write(w)
		return
	}
	// return the address used to sign the process and the nullifier
	apicommon.HTTPWriteJSON(w, &ConsumedAddressResponse{
		Address:   cspProcess.UsedAddress,
		Nullifier: state.GenerateNullifier(common.BytesToAddress(cspProcess.UsedAddress), *processID),
		At:        cspProcess.UsedAt,
	})
}

// validateContactInfo checks if at least one contact method is provided
func validateContactInfo(email, phone string) error {
	if len(email) == 0 && len(phone) == 0 {
		return errors.ErrInvalidUserData.Withf("no contact information provided (email or phone)")
	}
	return nil
}

// validateEmail validates the email format if provided
func validateEmail(email string) error {
	if len(email) > 0 && !internal.ValidEmail(email) {
		return errors.ErrInvalidUserData.Withf("invalid email format")
	}
	return nil
}

// validateAuthRequest validates the authentication request data
func validateAuthRequest(req *AuthRequest, census *db.Census) error {
	// Check request participant ID
	// TODO: Add correct validations

	// Only require contact information if the census has two-factor fields
	if len(census.TwoFaFields) > 0 {
		return validateContactInfo(req.Email, req.Phone)
	}

	// Validate email if provided
	return validateEmail(req.Email)
}

// verifyEmail checks if the provided email matches the member's stored email
func verifyEmail(email string, storedEmail string) error {
	if !strings.EqualFold(email, storedEmail) {
		return errors.ErrUnauthorized.Withf("invalid user email")
	}
	return nil
}

// handleEmailContact verifies the email and returns the appropriate contact method
func handleEmailContact(
	email string,
	storedEmail string,
) (string, notifications.ChallengeType, error) {
	if err := verifyEmail(email, storedEmail); err != nil {
		return "", "", err
	}
	return email, notifications.EmailChallenge, nil
}

// handlePhoneContact verifies the phone and returns the appropriate contact method
func handlePhoneContact(
	org *db.Organization,
	phone string,
	memberHashedPhone db.HashedPhone,
) (string, notifications.ChallengeType, error) {
	normalized, err := internal.SanitizeAndVerifyPhoneNumber(phone, org.Country)
	if err != nil {
		return "", "", err
	}

	hashedPhone, err := db.NewHashedPhone(normalized, org)
	if err != nil {
		return "", "", err
	}

	if !memberHashedPhone.Matches(hashedPhone) {
		return "", "", errors.ErrUnauthorized.Withf("user phone doesn't match")
	}
	return normalized, notifications.SMSChallenge, nil
}

// determineContactMethod determines the contact method based on the census type and request data
func determineContactMethod(
	census *db.Census,
	org *db.Organization,
	req *AuthRequest,
	member *db.OrgMember,
) (string, notifications.ChallengeType, error) {
	switch census.Type {
	case db.CensusTypeMail:
		return handleEmailContact(req.Email, member.Email)

	case db.CensusTypeSMS:
		return handlePhoneContact(org, req.Phone, member.Phone)

	case db.CensusTypeSMSorMail:
		if req.Email != "" {
			return handleEmailContact(req.Email, member.Email)
		}

		if req.Phone != "" {
			return handlePhoneContact(org, req.Phone, member.Phone)
		}

		// If neither email nor phone is provided for SMS or Mail census
		return "", "", errors.ErrInvalidUserData.Withf("no valid contact method provided")
	case db.CensusTypeAuthOnly:
		// For auth-only censuses, no contact method or challenge is needed
		return "", "", nil
	default:
		return "", "", errors.ErrNotSupported.Withf("invalid census type")
	}
}

// authFirstStep is the first step of the authentication process. It receives
// the request, the bundle ID and the census ID as parameters. It checks the
// request data (participant ID, email and phone) against the census data.
// If the data is valid, it generates a token with the bundle ID, the
// participant ID as the user ID, the contact information as the
// destination and the challenge type. It returns the token and an error if
// any. It sends the challenge to the user (email or SMS) to verify the user
// token in the second step.
//
// The function first validates the request data against census information,
// then attempts to find the participant in the census using the login hash
// generated from the provided fields. If the participant is not found in the
// census, it returns ErrCensusParticipantNotFound. If found, it determines the
// appropriate contact method (email, SMS, or none for auth-only censuses) based
// on the census type and provided contact information. Finally, it generates and
// returns an authentication token that will be used in the second step. If the
// cooldown period between authentication attempts has not elapsed, the underlying
// BundleAuthToken call may return ErrAttemptCoolDownTime.
func (c *CSPHandlers) authFirstStep(
	r *http.Request,
	bundleID internal.HexBytes,
	censusID string,
) (internal.HexBytes, error) {
	// Parse request
	var req AuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, errors.ErrMalformedBody.Withf("invalid JSON request")
	}

	lang := apicommon.DefaultLang
	if l, ok := r.Context().Value(apicommon.LangMetadataKey).(string); ok && l != "" {
		lang = l
	}

	// Get census and org information first (needed for validation)
	census, err := c.mainDB.Census(censusID)
	if err != nil {
		if err == db.ErrNotFound {
			return nil, errors.ErrCensusNotFound
		}
		return nil, errors.ErrGenericInternalServerError.WithErr(err)
	}

	org, err := c.mainDB.Organization(census.OrgAddress)
	if err != nil {
		if err == db.ErrNotFound {
			return nil, errors.ErrOrganizationNotFound
		}
		return nil, errors.ErrGenericInternalServerError.WithErr(err)
	}

	// Validate request with census information
	if err := validateAuthRequest(&req, census); err != nil {
		return nil, err
	}

	phone, err := db.NewHashedPhone(req.Phone, org)
	if err != nil {
		return nil, errors.ErrInvalidData.WithErr(err)
	}

	// create an empty member and assign the input data where applicable
	inputMember := &db.OrgMember{
		OrgAddress:   census.OrgAddress,
		Name:         req.Name,
		Surname:      req.Surname,
		MemberNumber: req.MemberNumber,
		NationalID:   req.NationalID,
		BirthDate:    req.BirthDate,
		Email:        req.Email,
		Phone:        phone,
	}

	// Check the participant is in the census
	censusParticipant, err := c.mainDB.CensusParticipantByLoginHash(*census, *inputMember)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, errors.ErrCensusParticipantNotFound
		}
		return nil, errors.ErrGenericInternalServerError.WithErr(err)
	}

	// Fetch the corresponding org member using the participant ID (which is the ObjectID hex string)
	orgMember, err := c.mainDB.OrgMember(census.OrgAddress, censusParticipant.ParticipantID)
	if err != nil {
		return nil, fmt.Errorf("failed to get org member: %w", err)
	}

	if census.Weighted && orgMember.Weight == 0 {
		return nil, errors.ErrZeroWeightVoter
	}

	// Determine contact method based on census type
	toDestinations, challengeType, err := determineContactMethod(census, org, &req, orgMember)
	if err != nil {
		return nil, err
	}

	name := DefaultOrgName
	logo := DefaultOrgLogo

	if n, ok := org.Meta["name"].(string); ok {
		name = n
		// if name is found then retrieve the logo as well
		if l, ok := org.Meta["logo"].(string); ok {
			logo = l
		}
	}

	// Generate the token
	return c.csp.BundleAuthToken(
		bundleID,
		internal.HexBytesFromString(orgMember.ID.Hex()),
		toDestinations,
		challengeType,
		lang,
		name,
		logo,
	)
}

// authSecondStep is the second step of the authentication process. It
// receives the request and checks the token and the challenge solution
// against the server data. If the data is valid, it returns the token and
// an error if any. If the solution is valid, the token is marked as verified
// and returned to the user. The user can use the token to sign the bundle
// processes.
//
// For auth-only tokens that don't require challenge verification, the function
// checks if the token is already verified. Otherwise, it verifies the challenge
// solution provided in AuthData against the stored challenge. It handles various
// error cases such as invalid tokens, incorrect solutions, and storage failures.
func (c *CSPHandlers) authSecondStep(r *http.Request) (internal.HexBytes, error) {
	var req AuthChallengeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, errors.ErrMalformedBody.Withf("invalid JSON request")
	}

	// For tokens that require challenge verification, check if AuthData is provided
	if len(req.AuthData) == 0 {
		// Check if this is an auth-only token that's already verified
		auth, err := c.csp.Storage.CSPAuth(req.AuthToken)
		if err != nil {
			return nil, errors.ErrUnauthorized.WithErr(err)
		}

		// Only allow pre-verified tokens if they're from auth-only censuses
		if auth.Verified {
			return req.AuthToken, nil
		}

		return nil, errors.ErrInvalidUserData.Withf("challenge solution required")
	}

	switch err := c.csp.VerifyBundleAuthToken(req.AuthToken, req.AuthData[0]); err {
	case nil:
		return req.AuthToken, nil
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
