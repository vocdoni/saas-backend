// Package csp implements the Census Service Provider functionality
package csp

import (
	"crypto/subtle"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
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
	orgName, orgLogo string, orgAddress common.Address,
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
	if lastToken != nil {
		remainingTime := c.notificationCoolDownTime - time.Since(lastToken.CreatedAt)
		if remainingTime > 0 {
			log.Warnw("cooldown time not reached",
				"userID", uID,
				"bundleID", bID,
				"lastToken", lastToken.Token)
			return nil, errors.ErrAttemptCoolDownTime.WithData(map[string]any{"coolDownTime": remainingTime.Milliseconds()})
		}
	}
	// generate a new token, secret and code
	token, secret, code := c.generateToken()
	// create the new token
	if err := c.Storage.SetCSPAuth(token, uID, bID, secret); err != nil {
		log.Warnw("error setting new token",
			"userID", uID,
			"bundleID", bID,
			"token", token,
			"error", err)
		return nil, ErrStorageFailure
	}
	log.Debugw("new auth token stored",
		"userID", uID,
		"bundleID", bID,
		"token", token)
	// compose the notification challenge, advertising the OTP validity window
	// (not the notification cooldown) as the code's expiry time
	remainingTimeN := c.otpExpiry.String()
	orgInfo := notifications.OrganizationInfo{
		Address: orgAddress,
		Name:    orgName,
		Logo:    orgLogo,
	}
	ch, err := notifications.NewNotificationChallenge(ctype, lang, uID, bID, to, code, orgInfo, remainingTimeN)
	if err != nil {
		log.Warnw("error composing notification challenge",
			"userID", uID,
			"bundleID", bID,
			"token", token,
			"error", err)
		return nil, ErrNotificationFailure
	}
	// push the challenge to the queue to be sent
	if err := c.notifyQueue.Push(ch); err != nil {
		log.Warnw("error pushing notification challenge",
			"userID", uID,
			"bundleID", bID,
			"token", token,
			"error", err)
		return nil, ErrNotificationFailure
	}
	return token, nil
}

func (c *CSP) ResendChallenge(token internal.HexBytes, to string,
	ctype notifications.ChallengeType, lang string,
	orgName, orgLogo string, orgAddress common.Address,
) error {
	// check the input parameters
	if len(token) == 0 {
		return ErrInvalidAuthToken
	}
	if to == "" || ctype == "" {
		return errors.ErrInvalidData.Withf("missing challenge destination or type")
	}

	// get the user data from the token
	authTokenData, err := c.Storage.CSPAuth(token)
	if err != nil {
		log.Warnw("error getting user data by token",
			"token", token,
			"error", err)
		return ErrInvalidAuthToken
	}

	remainingTime := c.otpExpiry - time.Since(authTokenData.CreatedAt)
	// an already-verified token is always reported as such, regardless of age,
	// so it never masquerades as merely expired
	if authTokenData.Verified {
		coolDown := remainingTime.Seconds()
		if coolDown < 0 {
			coolDown = 0
		}
		return errors.ErrUserAlreadyVerified.WithData(map[string]any{"coolDownTime": coolDown})
	}
	if remainingTime <= 0 {
		log.Warnw("resend requested but OTP has expired",
			"userID", authTokenData.UserID,
			"bundleID", authTokenData.BundleID,
			"token", authTokenData.Token)
		return ErrTokenExpired
	}
	// reject unverified tokens without a stored challenge secret (legacy rows or
	// auth-only tokens): regenerating a code from an empty secret would yield a
	// guessable value, so such tokens are discarded. ErrTokenExpired prompts the
	// client to restart the OTP flow rather than treating it as a hard failure.
	if authTokenData.Secret == "" {
		log.Warnw("resend requested for token without challenge secret",
			"userID", authTokenData.UserID,
			"bundleID", authTokenData.BundleID,
			"token", token)
		return ErrTokenExpired
	}
	// compose the notification challenge
	remainingTimeN := remainingTime.String()
	orgInfo := notifications.OrganizationInfo{
		Address: orgAddress,
		Name:    orgName,
		Logo:    orgLogo,
	}
	code, err := c.regenerateTokenCode(authTokenData.Secret)
	if err != nil {
		log.Warnw("error regenerating token code",
			"userID", authTokenData.UserID,
			"bundleID", authTokenData.BundleID,
			"token", token,
			"error", err)
		return ErrChallengeCodeFailure
	}
	ch, err := notifications.NewNotificationChallenge(
		ctype,
		lang,
		authTokenData.UserID,
		authTokenData.BundleID,
		to,
		code,
		orgInfo,
		remainingTimeN,
	)
	if err != nil {
		log.Warnw("error composing notification challenge",
			"userID", authTokenData.UserID,
			"bundleID", authTokenData.BundleID,
			"token", token,
			"error", err)
		return ErrNotificationFailure
	}
	// push the challenge to the queue to be sent
	if err := c.notifyQueue.Push(ch); err != nil {
		log.Warnw("error pushing notification challenge",
			"userID", authTokenData.UserID,
			"bundleID", authTokenData.BundleID,
			"token", token,
			"error", err)
		return ErrNotificationFailure
	}
	return nil
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
	// reject tokens without a stored challenge secret. This covers legacy rows
	// created before per-token secrets existed, auth-only tokens, and tokens
	// already verified (whose secret was wiped): in all cases the OTP would be
	// derived from an empty secret and thus trivially guessable, so the token is
	// not OTP-verifiable. ErrTokenExpired prompts the client to restart the OTP
	// flow rather than treating it as a hard failure.
	if authTokenData.Secret == "" {
		log.Warnw("verification attempted for token without challenge secret",
			"userID", authTokenData.UserID,
			"bundleID", authTokenData.BundleID,
			"token", token)
		return ErrTokenExpired
	}
	// reject if the OTP window has passed
	if time.Since(authTokenData.CreatedAt) > c.otpExpiry {
		log.Warnw("OTP expired",
			"userID", authTokenData.UserID,
			"bundleID", authTokenData.BundleID,
			"token", token)
		return ErrTokenExpired
	}
	// reject tokens that exhausted the maximum number of attempts
	if authTokenData.Attempts >= MaxChallengeAttempts {
		log.Warnw("too many challenge attempts",
			"userID", authTokenData.UserID,
			"bundleID", authTokenData.BundleID,
			"token", token,
			"attempts", authTokenData.Attempts)
		return ErrTooManyAttempts
	}
	// verify the solution, and if the solution is not correct, atomically record
	// the failed attempt while enforcing the cap
	if !c.verifySolution(authTokenData.Secret, solution) {
		log.Warnw("challenge code does not match",
			"userID", authTokenData.UserID,
			"bundleID", authTokenData.BundleID,
			"token", token)
		recorded, err := c.Storage.IncrementCSPAuthAttempts(token, MaxChallengeAttempts)
		if err != nil {
			// fail closed: if we cannot persist the attempt, do not allow the
			// verification to proceed, otherwise attempt limiting could be
			// bypassed while the database is unhealthy
			log.Warnw("error incrementing challenge attempts",
				"token", token,
				"error", err)
			return ErrStorageFailure
		}
		if !recorded {
			// a concurrent request already pushed attempts to the cap
			return ErrTooManyAttempts
		}
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

// generateToken generates a new authentication token with a random OTP secret.
// It returns the bearer token, the base32 OTP secret, and the 6-digit code.
func (*CSP) generateToken() (bToken internal.HexBytes, secret, code string) {
	secret = gotp.RandomSecret(16)
	code = gotp.NewDefaultHOTP(secret).At(0)
	// a uuid.UUID is a [16]byte; slice its bytes directly to avoid the fallible
	// MarshalBinary call (and the panic path it would require).
	id := uuid.New()
	return internal.HexBytes(id[:]), secret, code
}

// regenerateTokenCode recomputes the OTP code for a stored secret (used for resend).
func (*CSP) regenerateTokenCode(secret string) (string, error) {
	return gotp.NewDefaultHOTP(secret).At(0), nil
}

// verifySolution checks whether solution matches the HOTP code for secret. The
// comparison is constant-time to avoid leaking the code through timing.
func (*CSP) verifySolution(secret, solution string) bool {
	code := gotp.NewDefaultHOTP(secret).At(0)
	return subtle.ConstantTimeCompare([]byte(code), []byte(solution)) == 1
}

// createAuthOnlyToken creates a pre-verified token for auth-only censuses
// that don't require challenge verification. It generates a token and immediately
// marks it as verified.
func (c *CSP) createAuthOnlyToken(bID, uID internal.HexBytes) (internal.HexBytes, error) {
	// generate a new token (we don't need the code for auth-only)
	bToken, err := uuid.New().MarshalBinary()
	if err != nil {
		log.Warnw("error marshalling token",
			"error", err,
			"userID", uID,
			"bundleID", bID)
		return nil, ErrInvalidAuthToken
	}

	// create the new token (auth-only tokens need no OTP secret)
	if err := c.Storage.SetCSPAuth(bToken, uID, bID, ""); err != nil {
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
