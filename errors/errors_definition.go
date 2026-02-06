// Package errors provides custom error types and definitions for the application.
//
//nolint:lll
package errors

import (
	"fmt"
	"net/http"
)

// The custom Error type satisfies the error interface.
// Error() returns a human-readable description of the error.
//
// Error codes in the 40001-49999 range are the user's fault,
// and they return HTTP Status 400 or 404 (or even 204), whatever is most appropriate.
//
// Error codes 50001-59999 are the server's fault
// and they return HTTP Status 500 or 503, or something else if appropriate.
//
// The initial list of errors were more or less grouped by topic, but the list grows with time in a random fashion.
// NEVER change any of the current error codes, only append new errors after the current last 4XXX or 5XXX
// If you notice there's a gap (say, error code 4010, 4011 and 4013 exist, 4012 is missing) DON'T fill in the gap,
// that code was used in the past for some error (not anymore) and shouldn't be reused.
// There's no correlation between Code and HTTP Status,
// for example the fact that Code 4045 returns HTTP Status 404 Not Found is just a coincidence
//
// Do note that HTTPstatus 204 No Content implies the response body will be empty,
// so the Code and Message will actually be discarded, never sent to the client
var (
	// Authentication errors (401)
	ErrUnauthorized                          = Error{Code: 40001, HTTPstatus: http.StatusUnauthorized, Err: fmt.Errorf("authentication required"), LogLevel: "info"}
	ErrNonOauthAccount                       = Error{Code: 40101, HTTPstatus: http.StatusUnauthorized, Err: fmt.Errorf("account is not registered using OAuth"), LogLevel: "info"}
	ErrUserNoVerified                        = Error{Code: 40014, HTTPstatus: http.StatusUnauthorized, Err: fmt.Errorf("account email not verified"), LogLevel: "info"}
	ErrVerificationCodeExpired               = Error{Code: 40016, HTTPstatus: http.StatusUnauthorized, Err: fmt.Errorf("verification code has expired"), LogLevel: "info"}
	ErrInvitationExpired                     = Error{Code: 40019, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("invitation code has expired"), LogLevel: "info"}
	ErrInvalidLoginCredentials               = Error{Code: 40102, HTTPstatus: http.StatusUnauthorized, Err: fmt.Errorf("invalid login credentials"), LogLevel: "info"}
	ErrAttemptCoolDownTime                   = Error{Code: 40103, HTTPstatus: http.StatusUnauthorized, Err: fmt.Errorf("attempt cooldown time not reached"), LogLevel: "info"}
	ErrInvalidOAuthProvider                  = Error{Code: 40039, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("invalid or unsupported OAuth provider"), LogLevel: "info"}
	ErrOAuthUserCannotUsePasswordRecovery    = Error{Code: 40040, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("OAuth users cannot use password recovery"), LogLevel: "info"}
	ErrCannotUnlinkLastAuthMethod            = Error{Code: 40041, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("cannot unlink last authentication method"), LogLevel: "info"}
	ErrProviderAlreadyLinkedToThisAccount    = Error{Code: 40042, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("OAuth provider already linked to this account"), LogLevel: "info"}
	ErrProviderNotLinked                     = Error{Code: 40043, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("OAuth provider not linked to this account"), LogLevel: "info"}
	ErrProviderAlreadyLinkedToAnotherAccount = Error{Code: 40044, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("OAuth provider already linked to another account"), LogLevel: "info"}

	// Validation errors (400)
	ErrEmailMalformed          = Error{Code: 40002, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("invalid email format")}
	ErrPasswordTooShort        = Error{Code: 40003, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("password must be at least 8 characters")}
	ErrMalformedBody           = Error{Code: 40004, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("invalid JSON request body")}
	ErrInvalidUserData         = Error{Code: 40005, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("invalid user information provided")}
	ErrMalformedURLParam       = Error{Code: 40010, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("invalid URL parameter")}
	ErrNoOrganizationProvided  = Error{Code: 40011, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("organization address is required")}
	ErrInvalidOrganizationData = Error{Code: 40013, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("invalid organization information provided")}
	ErrUserAlreadyVerified     = Error{Code: 40015, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("user account is already verified")}
	ErrVerificationMaxAttempts = Error{Code: 40017, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("verification resend max attempts reached")}
	ErrStorageInvalidObject    = Error{Code: 40024, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("invalid storage object or parameters")}
	ErrNotSupported            = Error{Code: 40025, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("feature not supported")}
	ErrUserNoVoted             = Error{Code: 40036, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("user has not voted yet"), LogLevel: "info"}
	ErrInvalidData             = Error{Code: 40037, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("invalid data provided")}
	ErrInvalidCensusData       = Error{Code: 40030, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("invalid census data provided")}

	// Transaction errors (400)
	ErrCouldNotSignTransaction = Error{Code: 40006, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("transaction signing failed")}
	ErrInvalidTxFormat         = Error{Code: 40007, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("invalid transaction format")}
	ErrTxTypeNotAllowed        = Error{Code: 40008, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("transaction type not allowed")}

	// Not found errors (404)
	ErrOrganizationNotFound      = Error{Code: 40009, HTTPstatus: http.StatusNotFound, Err: fmt.Errorf("organization not found")}
	ErrNoOrganizations           = Error{Code: 40012, HTTPstatus: http.StatusNotFound, Err: fmt.Errorf("user has no organizations")}
	ErrUserNotFound              = Error{Code: 40018, HTTPstatus: http.StatusNotFound, Err: fmt.Errorf("user not found")}
	ErrPlanNotFound              = Error{Code: 40023, HTTPstatus: http.StatusNotFound, Err: fmt.Errorf("subscription plan not found")}
	ErrJobNotFound               = Error{Code: 40026, HTTPstatus: http.StatusNotFound, Err: fmt.Errorf("job not found")}
	ErrCensusNotFound            = Error{Code: 40027, HTTPstatus: http.StatusNotFound, Err: fmt.Errorf("census not found")}
	ErrCensusTypeNotFound        = Error{Code: 40028, HTTPstatus: http.StatusNotFound, Err: fmt.Errorf("census type not found")}
	ErrCensusParticipantNotFound = Error{Code: 40029, HTTPstatus: http.StatusNotFound, Err: fmt.Errorf("census participant not found")}
	ErrProcessNotFound           = Error{Code: 40038, HTTPstatus: http.StatusNotFound, Err: fmt.Errorf("process not found")}
	ErrGroupNotFound             = Error{Code: 40057, HTTPstatus: http.StatusNotFound, Err: fmt.Errorf("group not found")}

	// Conflict errors (409)
	ErrDuplicateConflict           = Error{Code: 40901, HTTPstatus: http.StatusConflict, Err: fmt.Errorf("resource already exists")}
	ErrUpdateWouldCreateDuplicates = Error{Code: 40902, HTTPstatus: http.StatusConflict, Err: fmt.Errorf("update would create duplicates")}

	// Subscription errors (400)
	ErrOrganizationHasNoSubscription          = Error{Code: 40020, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("organization has no subscription")}
	ErrOrganizationSubscriptionInactive       = Error{Code: 40021, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("organization subscription is not active")}
	ErrNoDefaultPlan                          = Error{Code: 40022, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("no default plan available")}
	ErrMaxDraftsReached                       = Error{Code: 40031, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("max drafts reached")}
	ErrMaxProcessesReached                    = Error{Code: 40033, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("max processes reached")}
	ErrUserHasNoAdminRole                     = Error{Code: 40032, HTTPstatus: http.StatusUnauthorized, Err: fmt.Errorf("user does not have admin role")}
	ErrProcessCensusSizeExceedsPlanLimit      = Error{Code: 40035, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("process census size exceeds plan limit")}
	ErrProcessCensusSizeExceedsEmailAllowance = Error{Code: 40046, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("process census size exceeds email allowance")}
	ErrProcessCensusSizeExceedsSMSAllowance   = Error{Code: 40047, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("process census size exceeds sms allowance")}
	ErrMaxOrganizationsReached                = Error{Code: 40048, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("user has reached maximum number of organizations")}
	// CSP errors (408)
	ErrZeroWeightVoter = Error{Code: 40801, HTTPstatus: http.StatusUnauthorized, Err: fmt.Errorf("voter weight cannot be zero")}

	// Server errors (500) - These should be used sparingly and only for true internal errors
	ErrMarshalingServerJSONFailed  = Error{Code: 50001, HTTPstatus: http.StatusInternalServerError, Err: fmt.Errorf("server error: failed to process response"), LogLevel: "error"}
	ErrGenericInternalServerError  = Error{Code: 50002, HTTPstatus: http.StatusInternalServerError, Err: fmt.Errorf("server error: operation failed"), LogLevel: "error"}
	ErrCouldNotCreateFaucetPackage = Error{Code: 50003, HTTPstatus: http.StatusInternalServerError, Err: fmt.Errorf("server error: faucet package creation failed"), LogLevel: "error"}
	ErrVochainRequestFailed        = Error{Code: 50004, HTTPstatus: http.StatusInternalServerError, Err: fmt.Errorf("server error: blockchain request failed"), LogLevel: "error"}
	ErrStripeError                 = Error{Code: 50005, HTTPstatus: http.StatusInternalServerError, Err: fmt.Errorf("server error: payment processing failed"), LogLevel: "error"}
	ErrInternalStorageError        = Error{Code: 50006, HTTPstatus: http.StatusInternalServerError, Err: fmt.Errorf("server error: storage operation failed"), LogLevel: "error"}
	ErrOAuthServerConnectionFailed = Error{Code: 50007, HTTPstatus: http.StatusInternalServerError, Err: fmt.Errorf("server error: OAuth server connection failed"), LogLevel: "error"}
	ErrStripeWebhookError          = Error{Code: 50008, HTTPstatus: http.StatusInternalServerError, Err: fmt.Errorf("server error: stripe webhook failed"), LogLevel: "error"}
)
