// Package handlers provides HTTP handlers for the CSP
// API endpoints, managing authentication, token verification, and cryptographic
// signing operations for process bundles in the Vocdoni voting platform.
package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/dvote/vochain/state"
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
//	@Description	- Step 0: The user sends the member number and contact information (email or phone).
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

// checkCensusParticipant checks if the member is in the census
func (c *CSPHandlers) checkCensusParticipant(w http.ResponseWriter, censusID string, userID string) bool {
	// Get census information
	census, err := c.mainDB.Census(censusID)
	if err != nil {
		if err == db.ErrNotFound {
			return false
		}
		return false
	}
	if _, _, err := c.mainDB.CensusParticipantByMemberNumber(censusID, userID, census.OrgAddress); err != nil {
		if err == db.ErrNotFound {
			errors.ErrUnauthorized.Withf("member not found in the census").Write(w)
			return false
		}
		log.Warnw("error getting census participant", "error", err)
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return false
	}
	return true
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
func (c *CSPHandlers) signAndRespond(w http.ResponseWriter, authToken, address, processID internal.HexBytes) {
	log.Debugw("new CSP sign request", "address", address, "procId", processID)

	signature, err := c.csp.Sign(authToken, address, processID, signers.SignerTypeECDSASalted)
	if err != nil {
		errors.ErrUnauthorized.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, &AuthResponse{Signature: signature})
}

// BundleSignHandler godoc
//
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

	// Find the process in the bundle
	processID, found := findProcessInBundle(bundle, req.ProcessID)
	if !found {
		errors.ErrUnauthorized.Withf("process not found in bundle").Write(w)
		return
	}

	// Check if the member is in the census
	if !c.checkCensusParticipant(w, bundle.Census.ID.Hex(), string(auth.UserID)) {
		return
	}

	// Parse the address from the payload
	address, ok := parseAddress(w, req.Payload)
	if !ok {
		return
	}

	// Sign the request and send the response
	c.signAndRespond(w, req.AuthToken, *address, processID)
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
//	@Failure		400			{object}	errors.Error	"Invalid input data"
//	@Failure		401			{object}	errors.Error	"Unauthorized or invalid token"
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
	// check if the process has been consumed and return error if not
	if !cspProcess.Consumed {
		errors.ErrUserNoVoted.Write(w)
		return
	}
	// return the address used to sign the process and the nullifier
	apicommon.HTTPWriteJSON(w, &ConsumedAddressResponse{
		Address:   cspProcess.ConsumedAddress,
		Nullifier: state.GenerateNullifier(common.BytesToAddress(cspProcess.ConsumedAddress), *processID),
		At:        cspProcess.ConsumedAt,
	})
}

// validateMemberNumber checks if the member number is provided
func validateMemberNumber(memberNumber string) error {
	if len(memberNumber) == 0 {
		return errors.ErrInvalidUserData.Withf("member number not provided")
	}
	return nil
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

// validatePhone validates and sanitizes the phone number if provided
func validatePhone(phone *string) error {
	if len(*phone) == 0 {
		return nil
	}

	sanitizedPhone, err := internal.SanitizeAndVerifyPhoneNumber(*phone)
	if err != nil {
		log.Warnw("invalid phone number format", "phone", *phone, "error", err)
		return errors.ErrInvalidUserData.Withf("invalid phone number format")
	}
	*phone = sanitizedPhone
	return nil
}

// validateAuthRequest validates the authentication request data
func validateAuthRequest(req *AuthRequest) error {
	// Check request member number
	err := validateMemberNumber(req.ParticipantID)
	if err != nil {
		return err
	}

	// Check request contact information
	err = validateContactInfo(req.Email, req.Phone)
	if err != nil {
		return err
	}

	// Validate email if provided
	err = validateEmail(req.Email)
	if err != nil {
		return err
	}

	// Validate phone if provided
	return validatePhone(&req.Phone)
}

// getCensusAndMember retrieves the census and member information
func (c *CSPHandlers) getCensusAndMember(censusID string, memberNumber string) (*db.Census, *db.OrgMember, error) {
	// Get census information
	census, err := c.mainDB.Census(censusID)
	if err != nil {
		if err == db.ErrNotFound {
			return nil, nil, errors.ErrCensusNotFound
		}
		return nil, nil, errors.ErrGenericInternalServerError.WithErr(err)
	}

	// Check the member is a census participant
	member, _, err := c.mainDB.CensusParticipantByMemberNumber(censusID, memberNumber, census.OrgAddress)
	if err != nil {
		if err == db.ErrNotFound {
			return nil, nil, errors.ErrUnauthorized.Withf("member not found in the census")
		}
		return nil, nil, errors.ErrGenericInternalServerError.WithErr(err)
	}

	return census, member, nil
}

// verifyPassword checks if the provided password matches the member's hashed password
func (c *CSPHandlers) verifyPassword(password string, hashedPass []byte) error {
	if password != "" && !bytes.Equal(internal.HashPassword(c.csp.PasswordSalt, password), hashedPass) {
		return fmt.Errorf("invalid user data")
	}
	return nil
}

// verifyEmail checks if the provided email matches the member's stored email
func verifyEmail(email string, storedEmail string) error {
	if !strings.EqualFold(email, storedEmail) {
		return errors.ErrUnauthorized.Withf("invalid user email")
	}
	return nil
}

// verifyPhone checks if the provided phone matches the member's hashed phone
func verifyPhone(orgAddress string, phone string, hashedPhone []byte) error {
	if !bytes.Equal(internal.HashOrgData(orgAddress, phone), hashedPhone) {
		return errors.ErrUnauthorized.Withf("invalid user phone")
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
	orgAddress string,
	phone string,
	hashedPhone []byte,
) (string, notifications.ChallengeType, error) {
	if err := verifyPhone(orgAddress, phone, hashedPhone); err != nil {
		return "", "", err
	}
	return phone, notifications.SMSChallenge, nil
}

// determineContactMethod determines the contact method based on the census type and request data
func determineContactMethod(
	census *db.Census,
	req *AuthRequest,
	member *db.OrgMember,
) (string, notifications.ChallengeType, error) {
	switch census.Type {
	case db.CensusTypeMail:
		return handleEmailContact(req.Email, member.Email)

	case db.CensusTypeSMS:
		return handlePhoneContact(census.OrgAddress, req.Phone, member.HashedPhone)

	case db.CensusTypeSMSorMail:
		if req.Email != "" {
			return handleEmailContact(req.Email, member.Email)
		}

		if req.Phone != "" {
			return handlePhoneContact(census.OrgAddress, req.Phone, member.HashedPhone)
		}
	}

	return "", "", errors.ErrNotSupported.Withf("invalid census type")
}

// authFirstStep is the first step of the authentication process. It receives
// the request, the bundle ID and the census ID as parameters. It checks the
// request data (member number, email and phone) against the census data.
// If the data is valid, it generates a token with the bundle ID, the
// member number as the user ID, the contact information as the
// destination and the challenge type. It returns the token and an error if
// any. It sends the challenge to the user (email or SMS) to verify the user
// token in the second step.
func (c *CSPHandlers) authFirstStep(
	r *http.Request,
	bundleID internal.HexBytes,
	censusID string,
) (internal.HexBytes, error) {
	// Parse and validate request
	var req AuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, errors.ErrMalformedBody.Withf("invalid JSON request")
	}

	if err := validateAuthRequest(&req); err != nil {
		return nil, err
	}

	// Get census and member information
	census, member, err := c.getCensusAndMember(censusID, req.ParticipantID)
	if err != nil {
		return nil, err
	}

	// Verify password if provided
	if err := c.verifyPassword(req.Password, member.HashedPass); err != nil {
		return nil, err
	}

	// Determine contact method based on census type
	toDestinations, challengeType, err := determineContactMethod(census, &req, member)
	if err != nil {
		return nil, err
	}

	// Generate the token
	return c.csp.BundleAuthToken(bundleID, internal.HexBytes(member.MemberNumber), toDestinations, challengeType)
}

// authSecondStep is the second step of the authentication process. It
// receives the request and checks the token and the challenge solution
// against the server data. If the data is valid, it returns the token and
// an error if any. It the solution is valid, the token is marked as verified
// and returned to the user. The user can use the token to sign the bundle
// processes.
func (c *CSPHandlers) authSecondStep(r *http.Request) (internal.HexBytes, error) {
	var req AuthChallengeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, errors.ErrMalformedBody.Withf("invalid JSON request")
	}
	err := c.csp.VerifyBundleAuthToken(req.AuthToken, req.AuthData[0])
	if err == nil {
		return req.AuthToken, nil
	}

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
