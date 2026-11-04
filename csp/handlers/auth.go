package handlers

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/log"
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

// handleEmailContact verifies the email and returns the appropriate contact method.
// A mismatch returns the bare ErrCSPAuthFailed: adding the reason would let a caller
// distinguish it from an unknown participant.
func handleEmailContact(
	email string,
	storedEmail string,
) (string, notifications.ChallengeType, error) {
	if !strings.EqualFold(email, storedEmail) {
		return "", "", errors.ErrCSPAuthFailed
	}
	return email, notifications.EmailChallenge, nil
}

// voterPhone is a phone number a voter submitted, normalized (the SMS destination) and hashed (to
// compare with the stored member phone).
type voterPhone struct {
	normalized string
	hashed     db.HashedPhone
}

// parseVoterPhone normalizes and hashes a submitted phone number, returning nil for an empty one.
// The hash is deliberately expensive (Argon2), so step 0 computes it once, before the participant
// lookup, and reuses it for the contact check: hashing it only for a found participant would make
// a found participant measurably slower to answer than an unknown one. It runs in the service-wide
// argon2 slots for ctx, the voter's request, and gives up with internal.ErrHashingBusy when none
// frees up in time (see phoneParseFailed).
func parseVoterPhone(ctx context.Context, phone string, org *db.Organization) (*voterPhone, error) {
	if phone == "" {
		return nil, nil
	}
	normalized, err := internal.SanitizeAndVerifyPhoneNumber(phone, org.Country)
	if err != nil {
		return nil, err
	}
	hashed, err := db.NewHashedPhoneContext(ctx, normalized, org)
	if err != nil {
		return nil, err
	}
	return &voterPhone{normalized: normalized, hashed: hashed}, nil
}

// phoneParseFailed is the answer to a parseVoterPhone error: ErrServerBusy, a 503 to retry later,
// when no argon2 slot freed up in time; otherwise the phone itself is invalid.
func phoneParseFailed(err error) errors.Error {
	if errors.Is(err, internal.ErrHashingBusy) {
		return errors.ErrServerBusy.WithErr(err)
	}
	return errors.ErrInvalidData.WithErr(err)
}

// handlePhoneContact verifies the phone and returns the appropriate contact method.
// A missing or mismatched phone returns the bare ErrCSPAuthFailed: adding the
// reason would let a caller distinguish it from an unknown participant.
func handlePhoneContact(
	phone *voterPhone,
	memberHashedPhone db.HashedPhone,
) (string, notifications.ChallengeType, error) {
	if phone == nil || !memberHashedPhone.Matches(phone.hashed) {
		return "", "", errors.ErrCSPAuthFailed
	}
	return phone.normalized, notifications.SMSChallenge, nil
}

// determineContactMethod determines the contact method based on the census type and request data.
// phone is the request's phone as parseVoterPhone returns it.
func determineContactMethod(
	census *db.Census,
	req *AuthRequest,
	phone *voterPhone,
	member *db.OrgMember,
) (string, notifications.ChallengeType, error) {
	switch census.Type {
	case db.CensusTypeMail:
		return handleEmailContact(req.Email, member.Email)

	case db.CensusTypeSMS:
		return handlePhoneContact(phone, member.Phone)

	case db.CensusTypeSMSorMail:
		if req.Email != "" {
			return handleEmailContact(req.Email, member.Email)
		}

		if req.Phone != "" {
			return handlePhoneContact(phone, member.Phone)
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

// authAttemptKeys derives the keys of the failed-attempt budgets a step-0 request spends. The
// submission key covers every login and contact field submitted, so failures with a wrong contact
// never spend the budget of the voter's own submission. The voter key covers the login fields
// alone, and is set only when the census has both login and contact fields: it is the coarser
// bound on trying many contacts for one voter. A census with only one kind of field has nothing
// coarser to key on and uses the submission key alone. The member password takes part in neither
// key, so every wrong password tried for one voter spends the same budget.
func authAttemptKeys(census *db.Census, member *db.OrgMember) (submission, voter []byte) {
	submission = db.HashAuthTwoFaFields(*member, census.AuthFields, census.TwoFaFields)
	if len(census.AuthFields) > 0 && len(census.TwoFaFields) > 0 {
		voter = db.HashAuthTwoFaFields(*member, census.AuthFields, nil)
	}
	return submission, voter
}

// memberPasswordMatches reports whether the client hash a voter sent matches the member's stored
// password, hashing it as db.HashMemberPassword does and comparing the argon2 hashes in constant
// time. A member without a stored password never matches. The hash runs in the service-wide
// argon2 slots shared with the account passwords (internal.HashPasswordContext): when none frees
// up in time, or the request is cancelled first, it gives up with internal.ErrHashingBusy.
func (c *CSPHandlers) memberPasswordMatches(ctx context.Context, clientHash string, member *db.OrgMember) (bool, error) {
	computed, err := c.hashPassword(ctx, c.csp.PasswordSalt, member.ID, clientHash)
	if err != nil {
		return false, err
	}
	return len(member.HashedPass) > 0 && subtle.ConstantTimeCompare(computed, member.HashedPass) == 1, nil
}

// unknownParticipant answers a login whose participant or member was not found. password is the
// request's client hash, set exactly when the census uses the password (authFirstStep only reads it
// then, and refuses it empty). With one, it first runs a password check against no stored password,
// the work a wrong password costs, so the answer takes as long whether the other fields name a member
// or not and the response time does not list the census. A failed check answers like it does for a
// member.
func (c *CSPHandlers) unknownParticipant(ctx context.Context, password string, notFound error) error {
	if password != "" {
		if _, err := c.memberPasswordMatches(ctx, password, &db.OrgMember{}); err != nil {
			return passwordCheckFailed(err)
		}
	}
	return notFound
}

// passwordCheckFailed is the answer to a password check that could not run. No argon2 slot freeing
// up in time (or the request ending first) is ErrServerBusy, a 503 to retry later; it is not
// ErrCSPAuthFailed, so it spends none of the failed-attempt budget.
func passwordCheckFailed(err error) error {
	if errors.Is(err, internal.ErrHashingBusy) {
		return errors.ErrServerBusy.WithErr(err)
	}
	return errors.ErrGenericInternalServerError.WithErr(err)
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
// generated from the provided fields. An unknown participant and a found
// participant whose contact data does not match both return the same
// ErrCSPAuthFailed, so a caller cannot probe the census contents; every such
// failure also spends the hourly failed-attempt budgets of the submission and of
// the claimed voter (see authAttemptKeys), and once one is exhausted step 0
// refuses further such attempts until the hour ends with
// ErrVerificationMaxAttempts, whether or not they match a participant. If found, it
// determines the appropriate contact method (email, SMS, or none for auth-only
// censuses) based on the census type and provided contact information. Finally,
// it generates and returns an authentication token that will be used in the
// second step. If the cooldown period between authentication attempts has not
// elapsed, the underlying AuthToken call may return ErrAttemptCoolDownTime.
//
// When the census auth fields include the member password, the request must carry it as the client
// hash. It takes no part in the lookup: once the other fields have found the participant, it is
// compared with the member's stored password before anything else about the member is revealed, and
// a mismatch answers exactly like an unknown participant (ErrCSPAuthFailed, spending the budget). An
// unknown participant pays for a password check too (see unknownParticipant), so the two cannot be
// told apart by their timing either. When no argon2 slot frees up in time, both answer
// ErrServerBusy, which spends no budget.
func (c *CSPHandlers) authFirstStep(
	r *http.Request,
	anchorID internal.HexBytes,
	censusID string,
) (token internal.HexBytes, err error) {
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
	// before any budget is claimed or challenge sent: the token could not be blind-signed anyway
	if c.anonymousCensusRefused(census) {
		return nil, errors.ErrInvalidData.With(csp.AnonymousVotingUnavailable)
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

	// the phone only takes part in the login hash and the contact check when it is a 2FA field (a
	// census only sends SMS then), and hashing it is deliberately expensive, so a phone sent to a
	// census that does not use it is ignored instead of hashed. When used, it is hashed once here,
	// before the lookup, for both purposes (see parseVoterPhone).
	var phone *voterPhone
	var hashedPhone db.HashedPhone
	if census.TwoFaFields.Contains(db.OrgMemberTwoFaFieldPhone) {
		// hashed before the failed-attempt budget is claimed, so a busy answer spends none of it
		if phone, err = parseVoterPhone(r.Context(), req.Phone, org); err != nil {
			return nil, phoneParseFailed(err)
		}
		if phone != nil {
			hashedPhone = phone.hashed
		}
	}

	// the password is only read when the census asks for it; an empty one fails below like any other
	// missing login field
	usesPassword := slices.Contains(census.AuthFields, db.OrgMemberAuthFieldsPassword)
	password := ""
	if usesPassword {
		password = req.Password
		if password != "" && !internal.ValidMemberPasswordHash(password) {
			return nil, errors.ErrInvalidUserData.Withf("invalid password format")
		}
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
		Phone:        hashedPhone,
	}).Normalized()

	// a voter must supply every login field the census requires: an empty value would otherwise hash
	// like a member stored without it, letting that member in on the remaining fields alone. The
	// password is checked apart, since the request carries it as a client hash and inputMember has
	// no field for that (HashedPass is the stored argon2 hash).
	lookupFields := slices.DeleteFunc(slices.Clone(census.AuthFields), func(f db.OrgMemberAuthField) bool {
		return f == db.OrgMemberAuthFieldsPassword
	})
	if inputMember.MissingLoginData(lookupFields, census.TwoFaFields) || (usesPassword && password == "") {
		return nil, errors.ErrInvalidUserData.Withf("missing required auth data")
	}

	// reserve one unit of the hourly failed-attempt budgets before checking the submission, so
	// concurrent attempts cannot all slip under the cap; it is given back unless the attempt fails
	// on the submitted data. The keys are what the caller submits, not the participant it may match,
	// so a submission matching nobody runs out exactly like one matching a participant (an
	// exhausted budget answers 40017 with retryAfter, the seconds until the hour resets)
	submissionKey, voterKey := authAttemptKeys(census, inputMember)
	claim, err := c.csp.ClaimAuthAttempt(anchorID, submissionKey, voterKey)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !errors.Is(err, errors.ErrCSPAuthFailed) {
			c.csp.ReleaseAuthAttempt(claim)
		}
	}()

	// Check the participant is in the census
	censusParticipant, err := c.mainDB.CensusParticipantByLoginHash(*census, *inputMember)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, c.unknownParticipant(r.Context(), password, errors.ErrCSPAuthFailed)
		}
		return nil, errors.ErrGenericInternalServerError.WithErr(err)
	}

	// Fetch the corresponding org member using the participant ID (which is the ObjectID hex string)
	orgMember, err := c.mainDB.OrgMember(census.OrgAddress, censusParticipant.ParticipantID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			// logged rather than put in the body, which must match the unknown-participant answer exactly
			log.Warnw("census participant without org member", "participantID", censusParticipant.ParticipantID, "error", err)
			return nil, c.unknownParticipant(r.Context(), password, errors.ErrCSPAuthFailed)
		}
		return nil, errors.ErrGenericInternalServerError.WithErr(err)
	}

	// the password goes first: the weight and contact checks below answer differently, so running
	// them before it would tell a caller without the password things about the member. A mismatch is
	// an ErrCSPAuthFailed, which keeps the reserved failed-attempt unit spent.
	if usesPassword {
		matches, err := c.memberPasswordMatches(r.Context(), password, orgMember)
		if err != nil {
			return nil, passwordCheckFailed(err)
		}
		if !matches {
			return nil, errors.ErrCSPAuthFailed
		}
	}
	uID := internal.HexBytesFromString(orgMember.ID.Hex())

	// Determine contact method based on census type. A mismatch is an ErrCSPAuthFailed, which keeps
	// the reserved failed-attempt unit spent.
	toDestinations, challengeType, err := determineContactMethod(census, &req, phone, orgMember)
	if err != nil {
		return nil, err
	}

	// only now that the contact matched: a distinct error before it would tell whoever merely
	// knows a member's login fields that they name a (zero-weight) participant
	if census.Weighted && orgMember.Weight == 0 {
		return nil, errors.ErrZeroWeightVoter
	}

	name, logo := orgNameAndLogo(org)

	// Generate the token
	return c.csp.AuthToken(
		anchorID,
		uID,
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
