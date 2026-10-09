// Package csp implements the Census Service Provider functionality
package csp

import (
	"crypto/subtle"
	"fmt"
	"math"
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

// AuthToken method generates a new authentication token for a user,
// anchored to a voting process. It generates a new token, secret and code from the
// attempt number. It composes the notification challenge and pushes it to
// the queue to be sent. It returns the token as HexBytes.
func (c *CSP) AuthToken(anchorID, uID internal.HexBytes, to string,
	ctype notifications.ChallengeType, lang string,
	orgName, orgLogo string, orgAddress common.Address,
) (internal.HexBytes, error) {
	// check the input parameters
	if len(anchorID) == 0 {
		return nil, ErrNoAnchorID
	}
	if len(uID) == 0 {
		return nil, ErrNoUserID
	}

	// For auth-only cases (no challenge type and no destination), create a pre-verified token
	if to == "" && ctype == "" {
		return c.createAuthOnlyToken(anchorID, uID)
	}

	// get last token for the user and anchor
	lastToken, err := c.Storage.LastCSPAuth(uID, anchorID)
	if err != nil && err != db.ErrTokenNotFound {
		log.Warnw("error getting last token",
			"userID", uID,
			"anchorID", anchorID,
			"error", err)
		return nil, ErrStorageFailure
	}
	// check if the last token was created less than the cooldown time
	if lastToken != nil {
		remainingTime := c.notificationCoolDownTime - time.Since(lastToken.CreatedAt)
		if remainingTime > 0 {
			log.Warnw("cooldown time not reached",
				"userID", uID,
				"anchorID", anchorID,
				"lastToken", lastToken.Token)
			return nil, errors.ErrAttemptCoolDownTime.WithData(map[string]any{"coolDownTime": remainingTime.Milliseconds()})
		}
	}
	// cap the challenges (new tokens + resends) this member can be sent for this process today:
	// the cooldown only spaces sends, so without a cap a member's contact could be flooded with
	// one email/SMS per cooldown period all day long
	sendClaim, err := c.claimDailyChallengeSend(uID, anchorID)
	if err != nil {
		return nil, err
	}
	// give the unit back unless a challenge reached the notification queue
	queued := false
	defer func() {
		if !queued {
			c.releaseChallengeSend(sendClaim)
		}
	}()
	// generate a new token, secret and code
	token, secret, code := c.generateToken()
	// create the new token
	if err := c.Storage.SetCSPAuth(token, uID, anchorID, secret); err != nil {
		log.Warnw("error setting new token",
			"userID", uID,
			"anchorID", anchorID,
			"token", token,
			"error", err)
		return nil, ErrStorageFailure
	}
	log.Debugw("new auth token stored",
		"userID", uID,
		"anchorID", anchorID,
		"token", token)
	// compose the notification challenge, advertising the OTP validity window
	// (the challenge TTL, not the notification cooldown) as the code's expiry
	orgInfo := notifications.OrganizationInfo{
		Address: orgAddress,
		Name:    orgName,
		Logo:    orgLogo,
	}
	ch, err := notifications.NewNotificationChallenge(ctype, lang, uID, anchorID, to, code, orgInfo, c.notificationTTL.String())
	if err != nil {
		log.Warnw("error composing notification challenge",
			"userID", uID,
			"anchorID", anchorID,
			"token", token,
			"error", err)
		return nil, ErrNotificationFailure
	}
	ch.ExpiresAt = time.Now().Add(c.notificationTTL)
	// push the challenge to the queue to be sent
	err = c.pushChallenge(ch)
	queued = err == nil || errors.Is(err, errChallengeStillQueued)
	if err != nil {
		log.Warnw("error pushing notification challenge",
			"userID", uID,
			"anchorID", anchorID,
			"token", token,
			"error", err)
		return nil, ErrNotificationFailure
	}
	return token, nil
}

// ClaimChallengeResend atomically spends one of the at most MaxChallengeResends resends of an
// authentication token, returning ErrVerificationMaxAttempts once they are all spent. Each resend
// sends an email or an SMS paid by the platform, and the token creation cooldown does not apply to
// them. A resend request must claim before it checks the submitted contact, so a wrong contact
// spends one too: otherwise a token holder could probe a member's other contact (say, the phone
// once they know the email) with unlimited resends.
func (c *CSP) ClaimChallengeResend(token internal.HexBytes) error {
	if len(token) == 0 {
		return ErrInvalidAuthToken
	}
	claimed, err := c.Storage.ClaimCSPAuthResend(token, MaxChallengeResends)
	if err != nil {
		log.Warnw("error recording challenge resend",
			"token", token,
			"error", err)
		return ErrStorageFailure
	}
	if !claimed {
		return errors.ErrVerificationMaxAttempts.Withf("challenge resend limit reached, request a new token")
	}
	return nil
}

// ResendChallenge sends the challenge of an unverified, unexpired authentication token again,
// counting it against the member's daily challenge budget. The caller must have spent one of the
// token's resends with ClaimChallengeResend first.
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

	remainingTime := c.notificationTTL - time.Since(authTokenData.CreatedAt)
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
			"anchorID", authTokenData.AnchorID,
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
			"anchorID", authTokenData.AnchorID,
			"token", token)
		return ErrTokenExpired
	}
	// resends count against the same daily per-member budget as new tokens
	sendClaim, err := c.claimDailyChallengeSend(authTokenData.UserID, authTokenData.AnchorID)
	if err != nil {
		return err
	}
	// give the unit back unless a challenge reached the notification queue
	queued := false
	defer func() {
		if !queued {
			c.releaseChallengeSend(sendClaim)
		}
	}()
	// compose the notification challenge
	orgInfo := notifications.OrganizationInfo{
		Address: orgAddress,
		Name:    orgName,
		Logo:    orgLogo,
	}
	code, err := c.regenerateTokenCode(authTokenData.Secret)
	if err != nil {
		log.Warnw("error regenerating token code",
			"userID", authTokenData.UserID,
			"anchorID", authTokenData.AnchorID,
			"token", token,
			"error", err)
		return ErrChallengeCodeFailure
	}
	ch, err := notifications.NewNotificationChallenge(
		ctype,
		lang,
		authTokenData.UserID,
		authTokenData.AnchorID,
		to,
		code,
		orgInfo,
		remainingTime.String(),
	)
	if err != nil {
		log.Warnw("error composing notification challenge",
			"userID", authTokenData.UserID,
			"anchorID", authTokenData.AnchorID,
			"token", token,
			"error", err)
		return ErrNotificationFailure
	}
	ch.ExpiresAt = time.Now().Add(c.notificationTTL)
	// push the challenge to the queue to be sent
	err = c.pushChallenge(ch)
	queued = err == nil || errors.Is(err, errChallengeStillQueued)
	if err != nil {
		log.Warnw("error pushing notification challenge",
			"userID", authTokenData.UserID,
			"anchorID", authTokenData.AnchorID,
			"token", token,
			"error", err)
		return ErrNotificationFailure
	}
	return nil
}

// claimDailyChallengeSend claims one unit of the member's daily challenge-send budget for the
// anchor process, counting new tokens and resends together. The counter is Mongo-backed, so it
// survives service restarts. Fails closed on storage errors.
func (c *CSP) claimDailyChallengeSend(uID, anchorID internal.HexBytes) (db.CSPCounterClaim, error) {
	res, err := c.Storage.ClaimCSPCounter(db.CSPCounterChallengeSends,
		counterKey(uID, anchorID), c.maxDailyChallengeSends)
	if err != nil {
		log.Warnw("error claiming daily challenge send",
			"userID", uID,
			"anchorID", anchorID,
			"error", err)
		return nil, ErrStorageFailure
	}
	if !res.Granted {
		return nil, counterLimitError(res, "daily challenge limit reached, try again tomorrow")
	}
	return res.Claim, nil
}

// releaseChallengeSend gives back a claimDailyChallengeSend unit when no challenge was queued, so
// a storage or composition failure does not spend the member's daily budget. Storage errors are
// logged: at worst the member loses one unit of today's budget.
func (c *CSP) releaseChallengeSend(claim db.CSPCounterClaim) {
	if err := c.Storage.ReleaseCSPCounter(claim); err != nil {
		log.Warnw("error releasing daily challenge send", "error", err)
	}
}

// counterLimitError is the 40017 a refused counter claim answers, carrying in its data the
// seconds until the counter's window resets as retryAfter.
func counterLimitError(res db.CSPCounterResult, msg string) errors.Error {
	retryAfter := int64(math.Ceil(time.Until(res.ResetAt).Seconds()))
	if retryAfter < 1 {
		retryAfter = 1
	}
	return errors.ErrVerificationMaxAttempts.With(msg).WithData(map[string]any{"retryAfter": retryAfter})
}

// counterKey joins an identity (a member id, or a login-fields hash) and an anchor (process) id
// into the key of a per-identity-per-process counter.
func counterKey(uID, anchorID internal.HexBytes) []byte {
	key := make([]byte, 0, len(uID)+len(anchorID))
	key = append(key, uID...)
	key = append(key, anchorID...)
	return key
}

// ClaimAuthAttempt reserves one unit of the current hour's failed step-0 authentication budget of
// a claimed voter identity on the anchor process, before the attempt is checked. Reserving up
// front, rather than counting failures afterwards, keeps concurrent attempts from all passing a
// not-yet-reached check: at most the budget can ever be in flight or failed. The caller must hand
// the claim to ReleaseAuthAttempt unless the attempt fails on the submitted login or contact data.
// The counter is Mongo-backed, so it survives service restarts.
//
// It returns an ErrVerificationMaxAttempts carrying retryAfter (seconds until the hour resets)
// once the budget is spent, and an ErrInternalStorageError if the counter cannot be claimed.
//
// The identity is derived from the submitted login fields, not from the participant they may
// match, so a submission that matches nobody runs out of budget exactly like one that matches a
// participant: the refusal itself cannot tell a caller who is in the census.
func (c *CSP) ClaimAuthAttempt(anchorID internal.HexBytes, identity []byte) (db.CSPCounterClaim, error) {
	res, err := c.Storage.ClaimCSPCounter(db.CSPCounterAuthFailures,
		counterKey(identity, anchorID), c.maxHourlyAuthFailures)
	if err != nil {
		return nil, errors.ErrInternalStorageError.WithErr(fmt.Errorf("claiming auth attempt: %w", err))
	}
	if !res.Granted {
		return nil, counterLimitError(res, "too many failed authentication attempts, try again later")
	}
	return res.Claim, nil
}

// ReleaseAuthAttempt gives back a ClaimAuthAttempt reservation for an attempt that did not fail on
// the submitted data. Storage errors are logged but not returned: at worst the identity loses one
// unit of the current hour's budget.
func (c *CSP) ReleaseAuthAttempt(claim db.CSPCounterClaim) {
	if err := c.Storage.ReleaseCSPCounter(claim); err != nil {
		log.Warnw("error releasing auth attempt", "error", err)
	}
}

// VerifyAuthToken method verifies the authentication token for a user. It gets the user data from the token and checks
// if the process is already consumed. It checks if the process is related to
// the user and if the token matches. It verifies the solution and updates the
// user data in the storage. It returns an error if the process is already
// consumed, if the process is not related to the user, if the token does not
// match, if the solution is not correct or if there is an error updating the
// user data.
func (c *CSP) VerifyAuthToken(token internal.HexBytes, solution string) error {
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
			"anchorID", authTokenData.AnchorID,
			"token", token)
		return ErrTokenExpired
	}
	// reject if the OTP window has passed
	if time.Since(authTokenData.CreatedAt) > c.notificationTTL {
		log.Warnw("OTP expired",
			"userID", authTokenData.UserID,
			"anchorID", authTokenData.AnchorID,
			"token", token)
		return ErrTokenExpired
	}
	// reject tokens that exhausted the maximum number of attempts
	if authTokenData.Attempts >= MaxChallengeAttempts {
		log.Warnw("too many challenge attempts",
			"userID", authTokenData.UserID,
			"anchorID", authTokenData.AnchorID,
			"token", token,
			"attempts", authTokenData.Attempts)
		return ErrTooManyAttempts
	}
	// verify the solution, and if the solution is not correct, atomically record
	// the failed attempt while enforcing the cap
	if !c.verifySolution(authTokenData.Secret, solution) {
		log.Warnw("challenge code does not match",
			"userID", authTokenData.UserID,
			"anchorID", authTokenData.AnchorID,
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
			"anchorID", authTokenData.AnchorID,
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
func (c *CSP) createAuthOnlyToken(anchorID, uID internal.HexBytes) (internal.HexBytes, error) {
	// generate a new token (we don't need the code for auth-only)
	bToken, err := uuid.New().MarshalBinary()
	if err != nil {
		log.Warnw("error marshalling token",
			"error", err,
			"userID", uID,
			"anchorID", anchorID)
		return nil, ErrInvalidAuthToken
	}

	// create the new token (auth-only tokens need no OTP secret)
	if err := c.Storage.SetCSPAuth(bToken, uID, anchorID, ""); err != nil {
		log.Warnw("error setting new token",
			"userID", uID,
			"anchorID", anchorID,
			"error", err)
		return nil, ErrStorageFailure
	}

	// immediately verify the token since no challenge is needed
	if err := c.Storage.VerifyCSPAuth(bToken); err != nil {
		log.Warnw("error verifying auth-only token",
			"userID", uID,
			"anchorID", anchorID,
			"token", bToken,
			"error", err)
		return nil, ErrStorageFailure
	}

	log.Debugw("new auth-only token created and verified",
		"userID", uID,
		"anchorID", anchorID,
		"token", bToken)

	return bToken, nil
}
