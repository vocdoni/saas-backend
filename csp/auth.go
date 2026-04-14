// Package csp implements the Census Service Provider functionality
package csp

import (
	"crypto/sha256"
	"encoding/base32"
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

type authNotificationMetadata struct {
	to         string
	ctype      notifications.ChallengeType
	lang       string
	orgName    string
	orgLogo    string
	orgAddress common.Address
}

// BundleAuthToken generates or reuses an active authentication token for a user
// in a bundle and sends the corresponding notification challenge.
func (c *CSP) BundleAuthToken(bID, uID internal.HexBytes, to string,
	ctype notifications.ChallengeType, lang string,
	orgName, orgLogo string, orgAddress common.Address,
) (internal.HexBytes, error) {
	if len(bID) == 0 {
		return nil, ErrNoBundleID
	}
	if len(uID) == 0 {
		return nil, ErrNoUserID
	}

	if to == "" && ctype == "" {
		return c.createAuthOnlyToken(bID, uID)
	}

	return c.bundleAuthTokenForRequest(bID, uID, authNotificationMetadata{
		to:         to,
		ctype:      ctype,
		lang:       lang,
		orgName:    orgName,
		orgLogo:    orgLogo,
		orgAddress: orgAddress,
	})
}

// ResendBundleAuthToken resends the active challenge or rotates it if the
// auth throttle window has elapsed.
func (c *CSP) ResendBundleAuthToken(token internal.HexBytes, to string,
	ctype notifications.ChallengeType, lang, orgName, orgLogo string, orgAddress common.Address,
) (internal.HexBytes, error) {
	if len(token) == 0 {
		return nil, ErrInvalidAuthToken
	}
	auth, err := c.Storage.CSPAuth(token)
	if err != nil {
		return nil, ErrInvalidAuthToken
	}
	if auth.Verified || !auth.InvalidatedAt.IsZero() || len(auth.SupersededBy) > 0 || len(auth.ChallengeNonce) == 0 {
		return nil, ErrInvalidAuthToken
	}
	return c.bundleAuthTokenForExisting(auth, authNotificationMetadata{
		to:         to,
		ctype:      ctype,
		lang:       lang,
		orgName:    orgName,
		orgLogo:    orgLogo,
		orgAddress: orgAddress,
	})
}

// VerifyBundleAuthToken verifies the authentication token challenge solution
// and marks the token as verified.
func (c *CSP) VerifyBundleAuthToken(token internal.HexBytes, solution string) error {
	if len(token) == 0 {
		return ErrInvalidAuthToken
	}
	if len(solution) == 0 {
		return ErrInvalidSolution
	}
	authTokenData, err := c.Storage.CSPAuth(token)
	if err != nil {
		log.Warnw("error getting user data by token", "token", token, "error", err)
		return ErrInvalidAuthToken
	}
	if !authTokenData.InvalidatedAt.IsZero() || len(authTokenData.SupersededBy) > 0 {
		return ErrInvalidAuthToken
	}
	if !c.verifySolution(authTokenData.UserID, authTokenData.BundleID, authTokenData.ChallengeNonce, solution) {
		log.Warnw("challenge code do not match",
			"userID", authTokenData.UserID,
			"bundleID", authTokenData.BundleID,
			"token", token,
			"solution", solution)
		return ErrChallengeCodeFailure
	}
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

func (c *CSP) bundleAuthTokenForRequest(bID, uID internal.HexBytes, metadata authNotificationMetadata) (internal.HexBytes, error) {
	auth, err := c.Storage.LastActiveCSPAuth(uID, bID)
	if err != nil && err != db.ErrTokenNotFound {
		log.Warnw("error getting active token", "userID", uID, "bundleID", bID, "error", err)
		return nil, ErrStorageFailure
	}
	if err == db.ErrTokenNotFound {
		return c.createAndSendAuthToken(bID, uID, metadata)
	}
	return c.bundleAuthTokenForExisting(auth, metadata)
}

func (c *CSP) bundleAuthTokenForExisting(auth *db.CSPAuth, metadata authNotificationMetadata) (internal.HexBytes, error) {
	if time.Since(auth.LastSentAt) < c.notificationCoolDownTime {
		return nil, errors.ErrAttemptCoolDownTime
	}
	if time.Since(auth.CreatedAt) >= c.authThrottleTime {
		return c.rotateAndSendAuthToken(auth, metadata)
	}
	if err := c.sendNotificationChallenge(auth, metadata); err != nil {
		return nil, err
	}
	return auth.Token, nil
}

func (c *CSP) createAndSendAuthToken(bID, uID internal.HexBytes, metadata authNotificationMetadata) (internal.HexBytes, error) {
	token, challengeNonce, err := c.generateToken(uID, bID)
	if err != nil {
		return nil, err
	}
	if err := c.Storage.SetCSPAuthChallenge(token, uID, bID, challengeNonce); err != nil {
		log.Warnw("error setting new token", "userID", uID, "bundleID", bID, "token", token, "error", err)
		return nil, ErrStorageFailure
	}
	auth, err := c.Storage.CSPAuth(token)
	if err != nil {
		return nil, ErrStorageFailure
	}
	if err := c.sendNotificationChallenge(auth, metadata); err != nil {
		return nil, err
	}
	return token, nil
}

func (c *CSP) rotateAndSendAuthToken(auth *db.CSPAuth, metadata authNotificationMetadata) (internal.HexBytes, error) {
	newToken, challengeNonce, err := c.generateToken(auth.UserID, auth.BundleID)
	if err != nil {
		return nil, err
	}
	if err := c.Storage.SetCSPAuthChallenge(newToken, auth.UserID, auth.BundleID, challengeNonce); err != nil {
		return nil, ErrStorageFailure
	}
	if err := c.Storage.InvalidateCSPAuth(auth.Token, newToken); err != nil {
		return nil, ErrStorageFailure
	}
	newAuth, err := c.Storage.CSPAuth(newToken)
	if err != nil {
		return nil, ErrStorageFailure
	}
	if err := c.sendNotificationChallenge(newAuth, metadata); err != nil {
		return nil, err
	}
	return newToken, nil
}

func (c *CSP) sendNotificationChallenge(auth *db.CSPAuth, metadata authNotificationMetadata) error {
	code := c.authCode(auth)
	orgInfo := notifications.OrganizationInfo{
		Address: metadata.orgAddress,
		Name:    metadata.orgName,
		Logo:    metadata.orgLogo,
	}
	ch, err := notifications.NewNotificationChallenge(
		metadata.ctype,
		metadata.lang,
		auth.UserID,
		auth.BundleID,
		metadata.to,
		code,
		orgInfo,
		c.notificationCoolDownTime.String(),
	)
	if err != nil {
		log.Warnw("error composing notification challenge",
			"userID", auth.UserID,
			"bundleID", auth.BundleID,
			"token", auth.Token,
			"error", err)
		return ErrNotificationFailure
	}
	if err := c.notifyQueue.Push(ch); err != nil {
		log.Warnw("error pushing notification challenge",
			"userID", auth.UserID,
			"bundleID", auth.BundleID,
			"token", auth.Token,
			"error", err)
		return ErrNotificationFailure
	}
	if err := c.Storage.TouchCSPAuthNotification(auth.Token); err != nil {
		log.Warnw("error touching auth notification timestamp", "token", auth.Token, "error", err)
		return ErrStorageFailure
	}
	return nil
}

// generateToken generates a new authentication token plus token-specific
// challenge nonce so each token has its own OTP.
func (*CSP) generateToken(uID, bID internal.HexBytes) (internal.HexBytes, internal.HexBytes, error) {
	bToken, err := uuid.New().MarshalBinary()
	if err != nil {
		log.Warnw("error marshalling token", "error", err, "userID", uID, "bundleID", bID)
		return nil, nil, ErrInvalidAuthToken
	}
	challengeNonce, err := uuid.New().MarshalBinary()
	if err != nil {
		log.Warnw("error marshalling challenge nonce", "error", err, "userID", uID, "bundleID", bID)
		return nil, nil, ErrInvalidAuthToken
	}
	return bToken, challengeNonce, nil
}

func (c *CSP) authCode(auth *db.CSPAuth) string {
	return authCode(auth.UserID, auth.BundleID, auth.ChallengeNonce)
}

func authCode(uID, bID, challengeNonce internal.HexBytes) string {
	secret := otpSecret(uID, bID, challengeNonce)
	otp := gotp.NewDefaultHOTP(secret)
	return otp.At(0)
}

// verifySolution verifies the challenge response against the token-specific
// secret.
func (*CSP) verifySolution(uID, bID, challengeNonce internal.HexBytes, solution string) bool {
	return authCode(uID, bID, challengeNonce) == solution
}

// createAuthOnlyToken creates a pre-verified token for auth-only censuses.
func (c *CSP) createAuthOnlyToken(bID, uID internal.HexBytes) (internal.HexBytes, error) {
	bToken, err := uuid.New().MarshalBinary()
	if err != nil {
		log.Warnw("error marshalling token", "error", err, "userID", uID, "bundleID", bID)
		return nil, ErrInvalidAuthToken
	}
	if err := c.Storage.SetCSPAuth(bToken, uID, bID); err != nil {
		log.Warnw("error setting new token", "userID", uID, "bundleID", bID, "error", err)
		return nil, ErrStorageFailure
	}
	if err := c.Storage.VerifyCSPAuth(bToken); err != nil {
		log.Warnw("error verifying auth-only token", "userID", uID, "bundleID", bID, "token", bToken, "error", err)
		return nil, ErrStorageFailure
	}
	log.Debugw("new auth-only token created and verified", "userID", uID, "bundleID", bID, "token", bToken)
	return bToken, nil
}

// otpSecret generates the token-specific OTP secret for a user and bundle.
func otpSecret(uID, bID, challengeNonce internal.HexBytes) string {
	hash := sha256.Sum256(append(append(uID, bID...), challengeNonce...))
	return base32.StdEncoding.EncodeToString(hash[:])
}
