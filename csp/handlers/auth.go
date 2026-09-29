package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
)

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

// handleAuthStep handles the authentication step and writes the response
func (c *CSPHandlers) handleAuthStep(w http.ResponseWriter, r *http.Request,
	step int, anchorID internal.HexBytes, censusID string,
) {
	var authToken internal.HexBytes
	var err error

	if step == 0 {
		authToken, err = c.authFirstStep(r, anchorID, censusID)
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
// the request, the anchor (process) ID and the census ID as parameters. It checks the
// request data (participant ID, email and phone) against the census data.
// If the data is valid, it generates a token with the anchor ID, the
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
// AuthToken call may return ErrAttemptCoolDownTime.
func (c *CSPHandlers) authFirstStep(
	r *http.Request,
	anchorID internal.HexBytes,
	censusID string,
) (internal.HexBytes, error) {
	// Parse request
	var req AuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, errors.ErrMalformedBody.Withf("invalid JSON request")
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

	lang := apicommon.NotificationLang(r.Context(), org)

	// Validate request with census information
	if err := validateAuthRequest(&req, census); err != nil {
		return nil, err
	}

	phone, err := db.NewHashedPhone(req.Phone, org)
	if err != nil {
		return nil, errors.ErrInvalidData.WithErr(err)
	}

	// create an empty member and assign the input data where applicable, then
	// normalize it through the same method that normalized the member at
	// creation time. Both sides of the login-hash comparison therefore derive
	// from one definition of the canonical form, and cannot drift apart.
	inputMember := (&db.OrgMember{
		OrgAddress:   census.OrgAddress,
		Name:         req.Name,
		Surname:      req.Surname,
		MemberNumber: req.MemberNumber,
		NationalID:   req.NationalID,
		BirthDate:    req.BirthDate,
		Email:        req.Email,
		Phone:        phone,
	}).Normalized()

	// a voter must supply every login field the census requires: an empty value would otherwise hash
	// like a member stored without it, letting that member in on the remaining fields alone
	if inputMember.MissingLoginData(census.AuthFields, census.TwoFaFields) {
		return nil, errors.ErrInvalidUserData.Withf("missing required auth data")
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
		return nil, errors.ErrCensusParticipantNotFound.With("failed to get org member")
	}

	if census.Weighted && orgMember.Weight == 0 {
		return nil, errors.ErrZeroWeightVoter
	}

	// Determine contact method based on census type
	toDestinations, challengeType, err := determineContactMethod(census, org, &req, orgMember)
	if err != nil {
		return nil, err
	}

	name, logo := orgNameAndLogo(org)

	// Generate the token
	return c.csp.AuthToken(
		anchorID,
		internal.HexBytesFromString(orgMember.ID.Hex()),
		toDestinations,
		challengeType,
		lang,
		name,
		logo,
		org.Address,
	)
}

// authSecondStep is the second step of the authentication process. It
// receives the request and checks the token and the challenge solution
// against the server data. If the data is valid, it returns the token and
// an error if any. If the solution is valid, the token is marked as verified
// and returned to the user. The user can use the token to sign the
// process elections.
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

	switch err := c.csp.VerifyAuthToken(req.AuthToken, req.AuthData[0]); err {
	case nil:
		return req.AuthToken, nil
	case csp.ErrInvalidAuthToken, csp.ErrInvalidSolution, csp.ErrChallengeCodeFailure,
		csp.ErrTokenExpired, csp.ErrTooManyAttempts:
		return nil, errors.ErrUnauthorized.WithErr(err)
	case csp.ErrUserUnknown:
		return nil, errors.ErrUserNotFound.WithErr(err)
	case csp.ErrStorageFailure:
		return nil, errors.ErrInternalStorageError.WithErr(err)
	default:
		return nil, errors.ErrGenericInternalServerError.WithErr(err)
	}
}
