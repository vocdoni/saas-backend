// Package csp implements the Census Service Provider functionality
package csp

import (
	"crypto/sha256"
	"encoding/base32"
	"time"

	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/xlzd/gotp"
	"go.vocdoni.io/dvote/log"
)

// BundleAuthToken method generates a new authentication token for a user in
// a process of a bundle. It generates a new token, secret and code from the
// attempt number. It composes the notification challenge and pushes it to
// the queue to be sent. It returns the token as HexBytes.
func (c *CSP) BundleAuthToken(bID, uID internal.HexBytes, to string,
	ctype notifications.ChallengeType, lang string,
	orgName, orgLogo string,
) (internal.HexBytes, error) {
	// check the input parameters
	if len(bID) == 0 {
		return nil, ErrNoBundleID
	}
	if len(uID) == 0 {
		return nil, ErrNoUserID
	}

	// For auth-only cases (no challenge type and no destination), create a pre-verified token
	if to == "" && ctype == "" {
		return c.createAuthOnlyToken(bID, uID)
	}

	// get last token for the user and bundle
	lastToken, err := c.Storage.LastCSPAuth(uID, bID)
	if err != nil && err != db.ErrTokenNotFound {
		log.Warnw("error getting last token",
			"userID", uID,
			"bundleID", bID,
			"error", err)
		return nil, ErrStorageFailure
	}
	// check if the last token was created less than the cooldown time
	if lastToken != nil && time.Since(lastToken.CreatedAt) < c.notificationCoolDownTime {
		log.Warnw("cooldown time not reached",
			"userID", uID,
			"bundleID", bID)
		return nil, ErrAttemptCoolDownTime
	}
	// generate a new token, secret and code from the attempt number
	token, code, err := c.generateToken(uID, bID)
	if err != nil {
		return nil, err
	}
	// create the new token
	if err := c.Storage.SetCSPAuth(token, uID, bID); err != nil {
		log.Warnw("error setting new token",
			"userID", uID,
			"bundleID", bID,
			"error", err)
		return nil, ErrStorageFailure
	}
	log.Debugw("new auth token stored",
		"userID", uID,
		"bundleID", bID,
		"token", token)
	// compose the notification challenge
	remainingTime := c.notificationCoolDownTime.String()
	ch, err := notifications.NewNotificationChallenge(ctype, lang, uID, bID, to, code, orgName, orgLogo, remainingTime)
	if err != nil {
		log.Warnw("error composing notification challenge",
			"userID", uID,
			"bundleID", bID,
			"error", err)
		return nil, ErrNotificationFailure
	}
	// push the challenge to the queue to be sent
	if err := c.notifyQueue.Push(ch); err != nil {
		log.Warnw("error pushing notification challenge",
			"userID", uID,
			"bundleID", bID,
			"error", err)
		return nil, ErrNotificationFailure
	}
	return token, nil
}

// VerifyBundleAuthToken method verifies the authentication token for a user
// in a process of a bundle. It gets the user data from the token and checks
// if the process is already consumed. It checks if the process is related to
// the user and if the token matches. It verifies the solution and updates the
// user data in the storage. It returns an error if the process is already
// consumed, if the process is not related to the user, if the token does not
// match, if the solution is not correct or if there is an error updating the
// user data.
func (c *CSP) VerifyBundleAuthToken(token internal.HexBytes, solution string) error {
	if len(token) == 0 {
		return ErrInvalidAuthToken
	}
	if len(solution) == 0 {
		return ErrInvalidSolution
	}
	// get the user data from the token
	authTokenData, err := c.Storage.CSPAuth(token)
	if err != nil {
		log.Warnw("error getting user data by token",
			"token", token,
			"error", err)
		return ErrInvalidAuthToken
	}
	// verify the solution, and if the solution is not correct, return an error
	if !c.verifySolution(authTokenData.UserID, authTokenData.BundleID, solution) {
		log.Warnw("challenge code do not match",
			"userID", authTokenData.UserID,
			"bundleID", authTokenData.BundleID,
			"token", token,
			"solution", solution)
		return ErrChallengeCodeFailure
	}
	// set the token as verified
	if err := c.Storage.VerifyCSPAuth(token); err != nil {
		log.Warnw("error verifying token",
			"userID", authTokenData.UserID,
			"bundleID", authTokenData.BundleID,
			"token", token,
			"error", err)
		return ErrStorageFailure
	}
	return nil
}

// generateToken method generates a new authentication token for a user in a
// process. It checks if the process is already consumed for this user. It
// generates a new challenge secret, challenge token and OTP code for the
// secret and the attempt number. It returns the token, the secret and the
// code respectively.
func (*CSP) generateToken(uID, bID internal.HexBytes) (
	internal.HexBytes, string, error,
) {
	// generate a new challenge secret and challenge token
	secret := otpSecret(uID, bID)
	// generate the OTP code for the secret and the attempt number
	otp := gotp.NewDefaultHOTP(secret)
	code := otp.At(0)
	// generate a new token and convert it to HexBytes
	bToken, err := uuid.New().MarshalBinary()
	if err != nil {
		log.Warnw("error marshalling token",
			"error", err,
			"userID", uID,
			"bundleID", bID)
		return nil, "", ErrInvalidAuthToken
	}
	return bToken, code, nil
}

// verifySolution method verifies the solution for a user process. It generates
// the OTP code for the process secret and the attempt number and compares it
// with the solution. It returns true if the solution is correct, false
// otherwise.
func (*CSP) verifySolution(uID, bID internal.HexBytes, solution string) bool {
	secret := otpSecret(uID, bID)
	// generate the OTP code for the secret and the attempt number
	otp := gotp.NewDefaultHOTP(secret)
	code := otp.At(0)
	// compare the generated code with the solution
	return code == solution
}

// createAuthOnlyToken creates a pre-verified token for auth-only censuses
// that don't require challenge verification. It generates a token and immediately
// marks it as verified.
func (c *CSP) createAuthOnlyToken(bID, uID internal.HexBytes) (internal.HexBytes, error) {
	// get last token for the user and bundle
	lastToken, err := c.Storage.LastCSPAuth(uID, bID)
	if err != nil && err != db.ErrTokenNotFound {
		log.Warnw("error getting last token",
			"userID", uID,
			"bundleID", bID,
			"error", err)
		return nil, ErrStorageFailure
	}
	// check if the last token was created less than the cooldown time
	if lastToken != nil && time.Since(lastToken.CreatedAt) < c.notificationCoolDownTime {
		log.Warnw("cooldown time not reached",
			"userID", uID,
			"bundleID", bID)
		return nil, ErrAttemptCoolDownTime
	}

	// generate a new token (we don't need the code for auth-only)
	bToken, err := uuid.New().MarshalBinary()
	if err != nil {
		log.Warnw("error marshalling token",
			"error", err,
			"userID", uID,
			"bundleID", bID)
		return nil, ErrInvalidAuthToken
	}

	// create the new token
	if err := c.Storage.SetCSPAuth(bToken, uID, bID); err != nil {
		log.Warnw("error setting new token",
			"userID", uID,
			"bundleID", bID,
			"error", err)
		return nil, ErrStorageFailure
	}

	// immediately verify the token since no challenge is needed
	if err := c.Storage.VerifyCSPAuth(bToken); err != nil {
		log.Warnw("error verifying auth-only token",
			"userID", uID,
			"bundleID", bID,
			"token", bToken,
			"error", err)
		return nil, ErrStorageFailure
	}

	log.Debugw("new auth-only token created and verified",
		"userID", uID,
		"bundleID", bID,
		"token", bToken)

	return bToken, nil
}

// otpSecret method generates a new OTP secret for a user and a bundle. The
// secret is generated by hashing the user ID and the bundle ID with SHA-256.
// It returns the secret as HexBytes.
func otpSecret(uID, bID internal.HexBytes) string {
	hash := sha256.Sum256(append(uID, bID...))
	// encode the secret in base32 and return it
	return base32.StdEncoding.EncodeToString(hash[:])
}
