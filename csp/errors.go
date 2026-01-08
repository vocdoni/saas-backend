package csp

import (
	"fmt"

	"github.com/vocdoni/saas-backend/csp/signers/saltedkey"
)

var (
	// ErrNoUserID is returned when no user ID is provided.
	ErrNoUserID = fmt.Errorf("no user ID provided for the user")
	// ErrNoBundleID is returned when no bundle ID is provided.
	ErrNoBundleID = fmt.Errorf("no bundle ID provided")
	// ErrNoProcessID is returned when no process ID is provided.
	ErrNoProcessID = fmt.Errorf("no process ID provided")
	// ErrTooManyAttempts is returned when no more SMS attempts available for a
	// user.
	ErrTooManyAttempts = fmt.Errorf("too many SMS attempts")
	// ErrUserUnknown is returned if the userID is not found in the database.
	ErrUserUnknown = fmt.Errorf("user is unknown")
	// ErrUserAlreadyVerified is returned if the user is already verified when
	// trying to verify it.
	ErrUserAlreadyVerified = fmt.Errorf("user is already verified")
	// ErrUserNotBelongsToProcess is returned if the user does not has
	// participation rights.
	ErrUserNotBelongsToProcess = fmt.Errorf("user does not belong to process")
	// ErrUserNotBelongsToBundle is returned if the user does not has
	// participation rights.
	ErrUserNotBelongsToBundle = fmt.Errorf("user does not belong to process bundle")
	// ErrInvalidAuthToken is returned if the authtoken does not match with the
	// process.
	ErrInvalidAuthToken = fmt.Errorf("invalid authentication token")
	// ErrInvalidSolution is returned if the solution does not meet the
	// requirements.
	ErrInvalidSolution = fmt.Errorf("invalid solution")
	// ErrChallengeCodeFailure is returned when the challenge code does not
	// match.
	ErrChallengeCodeFailure = fmt.Errorf("challenge code do not match")
	// ErrStorageFailure is returned when the storage service fails.
	ErrStorageFailure = fmt.Errorf("storage service failure")
	// ErrNotificationFailure is returned when the notification service fails.
	ErrNotificationFailure = fmt.Errorf("notification service failure")
	// ErrInvalidSignerType is returned when the signer type is invalid.
	ErrInvalidSignerType = fmt.Errorf("invalid signer type")
	// ErrAuthTokenNotVerified is returned when the token is not verified.
	ErrAuthTokenNotVerified = fmt.Errorf("the token is not verified")
	// ErrProcessAlreadyConsumed is returned when the user is already
	// identified in the process.
	ErrProcessAlreadyConsumed = fmt.Errorf("the user is already identified in the process")
	// ErrProcessAlreadyVoted is returned when the user has already voted in
	// the process.
	ErrProcessAlreadyVoted = fmt.Errorf("the user has already voted in the process")
	// ErrPrepareSignature is returned when the signature preparation fails.
	ErrPrepareSignature = fmt.Errorf("error preparing the signature")
	// ErrSign is returned when the signature fails.
	ErrSign = fmt.Errorf("error signing the message")
	// ErrUserAlreadySigning is returned when the user is already signing and
	// is not expected to be.
	ErrUserAlreadySigning = fmt.Errorf("the user is already signing")
	// ErrUserIsNotAlreadySigning is returned when the user is not signing and
	// is expected to be.
	ErrUserIsNotAlreadySigning = fmt.Errorf("the user is not signing")
	// ErrInvalidSalt is returned when the salt is invalid.
	ErrInvalidSalt = fmt.Errorf("invalid salt, length must be %d bytes", saltedkey.SaltSize)
)
