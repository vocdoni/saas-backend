// Package csp implements the Census Service Provider functionality
package csp

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"fmt"
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
	// generate a new token, a cryptographically random per-token challenge
	// secret and the OTP code derived from it
	challenge, err := c.generateToken(uID, bID)
	if err != nil {
		return nil, err
	}
	token, code := challenge.token, challenge.code
	// create the new token storing its challenge secret and expiration
	expiresAt := time.Now().Add(db.CSPAuthTokenValidity)
	if err := c.Storage.SetCSPAuthChallenge(token, uID, bID, challenge.secret, expiresAt); err != nil {
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
	// compose the notification challenge
	remainingTimeN := c.notificationCoolDownTime.String()
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

	remainingTime := c.notificationCoolDownTime - time.Since(authTokenData.CreatedAt)
	if remainingTime.Seconds() < 0 {
		log.Warnw("resend requested but cooldown time reached",
			"userID", authTokenData.UserID,
			"bundleID", authTokenData.BundleID,
			"lastToken", authTokenData.Token)
		return ErrTokenExpired
	} else if authTokenData.Verified {
		return errors.ErrUserAlreadyVerified.WithData(map[string]any{"coolDownTime": remainingTime.Seconds()})
	}
	// compose the notification challenge
	remainingTimeN := c.notificationCoolDownTime.String()
	orgInfo := notifications.OrganizationInfo{
		Address: orgAddress,
		Name:    orgName,
		Logo:    orgLogo,
	}
	// recompute the same code from the token's stored challenge secret so the
	// resent challenge matches the original one
	code := challengeCode(authTokenData.ChallengeSecret)
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
	// reject expired challenge codes
	if !authTokenData.ExpiresAt.IsZero() && time.Now().After(authTokenData.ExpiresAt) {
		log.Warnw("challenge code expired",
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
	// verify the solution, and if the solution is not correct, count the failed
	// attempt and return an error
	if !verifySolution(authTokenData.ChallengeSecret, solution) {
		log.Warnw("challenge code do not match",
			"userID", authTokenData.UserID,
			"bundleID", authTokenData.BundleID,
			"token", token)
		if err := c.Storage.IncrementCSPAuthAttempts(token); err != nil {
			log.Warnw("error incrementing challenge attempts",
				"token", token,
				"error", err)
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

// MaxChallengeAttempts is the maximum number of failed challenge-code
// verification attempts allowed for a single authentication token before it is
// rejected.
const MaxChallengeAttempts = 5

// challengeSecretSize is the number of random bytes used to build a per-token
// challenge secret.
const challengeSecretSize = 20

// authChallenge holds a freshly generated authentication token together with
// its per-token challenge secret and the OTP code derived from it.
type authChallenge struct {
	token  internal.HexBytes
	secret string
	code   string
}

// generateToken method generates a new authentication token for a user in a
// process. It generates a cryptographically random per-token challenge secret
// and the OTP code derived from it, plus a random token. The uID and bID
// parameters are only used for logging context.
func (*CSP) generateToken(uID, bID internal.HexBytes) (authChallenge, error) {
	// generate a cryptographically random per-token challenge secret
	secret, err := generateChallengeSecret()
	if err != nil {
		log.Warnw("error generating challenge secret",
			"error", err,
			"userID", uID,
			"bundleID", bID)
		return authChallenge{}, ErrChallengeCodeFailure
	}
	// generate a new token and convert it to HexBytes
	bToken, err := uuid.New().MarshalBinary()
	if err != nil {
		log.Warnw("error marshalling token",
			"error", err,
			"userID", uID,
			"bundleID", bID)
		return authChallenge{}, ErrInvalidAuthToken
	}
	// derive the OTP code from the secret
	return authChallenge{token: bToken, secret: secret, code: challengeCode(secret)}, nil
}

// verifySolution verifies the provided solution against the OTP code derived
// from the token's stored challenge secret using a constant-time comparison. It
// returns true if the solution is correct, false otherwise.
func verifySolution(secret, solution string) bool {
	code := challengeCode(secret)
	return subtle.ConstantTimeCompare([]byte(code), []byte(solution)) == 1
}

// challengeCode derives the OTP code for the given challenge secret.
func challengeCode(secret string) string {
	return gotp.NewDefaultHOTP(secret).At(0)
}

// generateChallengeSecret returns a cryptographically random base32-encoded
// secret suitable for use as an HOTP secret.
func generateChallengeSecret() (string, error) {
	b := make([]byte, challengeSecretSize)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("could not generate random challenge secret: %w", err)
	}
	return base32.StdEncoding.EncodeToString(b), nil
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
