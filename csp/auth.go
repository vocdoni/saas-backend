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

// challengeDelivery is where and how a challenge code is sent.
type challengeDelivery struct {
	to    string
	ctype notifications.ChallengeType
	lang  string
	org   notifications.OrganizationInfo
}

// AuthToken method generates a new authentication token for a user, anchored to
// a voting process, and sends its challenge code.
//
// While the user's previous code is still usable (unverified, not expired and
// not locked by failed attempts), it is reused rather than replaced: the new
// token answers the same challenge, so every code the user received keeps
// working. The code is re-sent only once the notification cooldown has passed;
// inside it the token is returned without sending anything. Each call still
// returns a distinct token, so whoever only knows the user's login data never
// holds the token the user verifies.
//
// Once the previous code is no longer usable, a new challenge is created, still
// subject to the cooldown since the last send. It returns the token as HexBytes.
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
	delivery := challengeDelivery{
		to:    to,
		ctype: ctype,
		lang:  lang,
		org:   notifications.OrganizationInfo{Address: orgAddress, Name: orgName, Logo: orgLogo},
	}

	// get last token for the user and anchor, and the challenge it answers
	var challenge *db.CSPAuth
	lastToken, err := c.Storage.LastCSPAuth(uID, anchorID)
	if err != nil && err != db.ErrTokenNotFound {
		log.Warnw("error getting last token",
			"userID", uID,
			"anchorID", anchorID,
			"error", err)
		return nil, ErrStorageFailure
	}
	if lastToken != nil {
		if challenge, err = c.challengeOf(lastToken); err != nil && err != db.ErrTokenNotFound {
			return nil, ErrStorageFailure
		}
	}

	if challenge != nil && !lastToken.Verified && c.challengeUsable(challenge) {
		token := newAuthToken()
		if err := c.Storage.SetCSPAuthForChallenge(token, challenge); err != nil {
			log.Warnw("error setting token for pending challenge",
				"userID", uID,
				"anchorID", anchorID,
				"error", err)
			return nil, ErrStorageFailure
		}
		if _, err := c.resendChallengeCode(challenge, delivery); err != nil {
			return nil, err
		}
		return token, nil
	}

	// check if the last code was sent less than the cooldown time ago
	if challenge != nil {
		remainingTime := c.notificationCoolDownTime - time.Since(challenge.LastSent())
		if remainingTime > 0 {
			log.Warnw("cooldown time not reached",
				"userID", uID,
				"anchorID", anchorID,
				"lastToken", lastToken.Token)
			return nil, errors.ErrAttemptCoolDownTime.WithData(map[string]any{"coolDownTime": remainingTime.Milliseconds()})
		}
	}
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
	if err := c.pushChallengeCode(uID, anchorID, code, time.Now().Add(c.notificationTTL), delivery); err != nil {
		return nil, err
	}
	return token, nil
}

// ResendChallenge re-sends the challenge code a pending token answers, at most
// once per notification cooldown: inside it, it fails with
// ErrAttemptCoolDownTime carrying the remaining milliseconds.
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
	challenge, err := c.challengeOf(authTokenData)
	if err != nil {
		if err == db.ErrTokenNotFound {
			return ErrTokenExpired
		}
		return ErrStorageFailure
	}

	remainingTime := c.notificationTTL - time.Since(challenge.CreatedAt)
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
	// reject tokens whose challenge has no stored secret (legacy rows, auth-only
	// tokens, or a challenge already solved through another token): regenerating
	// a code from an empty secret would yield a guessable value. Reject as well a
	// challenge locked by failed attempts, whose code can no longer be accepted.
	// ErrTokenExpired prompts the client to restart the OTP flow rather than
	// treating it as a hard failure.
	if challenge.Secret == "" || challenge.Attempts >= MaxChallengeAttempts {
		log.Warnw("resend requested for token without usable challenge",
			"userID", authTokenData.UserID,
			"anchorID", authTokenData.AnchorID,
			"token", token,
			"attempts", challenge.Attempts)
		return ErrTokenExpired
	}
	sent, err := c.resendChallengeCode(challenge, challengeDelivery{
		to:    to,
		ctype: ctype,
		lang:  lang,
		org:   notifications.OrganizationInfo{Address: orgAddress, Name: orgName, Logo: orgLogo},
	})
	if err != nil {
		return err
	}
	if !sent {
		coolDown := max(c.notificationCoolDownTime-time.Since(challenge.LastSent()), time.Millisecond)
		return errors.ErrAttemptCoolDownTime.WithData(map[string]any{"coolDownTime": coolDown.Milliseconds()})
	}
	return nil
}

// challengeOf returns the row holding the OTP challenge the given token answers,
// which is the token itself unless it was issued for an existing challenge. It
// returns db.ErrTokenNotFound if that row no longer exists.
func (c *CSP) challengeOf(authToken *db.CSPAuth) (*db.CSPAuth, error) {
	if len(authToken.ChallengeID) == 0 {
		return authToken, nil
	}
	challenge, err := c.Storage.CSPAuth(authToken.ChallengeID)
	if err != nil && err != db.ErrTokenNotFound {
		log.Warnw("error getting challenge of token",
			"token", authToken.Token,
			"challenge", authToken.ChallengeID,
			"error", err)
	}
	return challenge, err
}

// challengeUsable reports whether the challenge's code can still be accepted: it
// has a secret (not yet solved), is not expired and is not locked by attempts.
func (c *CSP) challengeUsable(challenge *db.CSPAuth) bool {
	return challenge.Secret != "" &&
		challenge.Attempts < MaxChallengeAttempts &&
		time.Since(challenge.CreatedAt) < c.notificationTTL
}

// resendChallengeCode sends the code of an existing challenge again, unless it
// was sent less than the notification cooldown ago, in which case it sends
// nothing and returns sent=false.
func (c *CSP) resendChallengeCode(challenge *db.CSPAuth, delivery challengeDelivery) (sent bool, err error) {
	claimed, err := c.Storage.ClaimCSPAuthSend(challenge.Token, c.notificationCoolDownTime)
	if err != nil {
		log.Warnw("error recording challenge send",
			"userID", challenge.UserID,
			"anchorID", challenge.AnchorID,
			"challenge", challenge.Token,
			"error", err)
		return false, ErrStorageFailure
	}
	if !claimed {
		return false, nil
	}
	code, err := c.regenerateTokenCode(challenge.Secret)
	if err != nil {
		log.Warnw("error regenerating token code",
			"userID", challenge.UserID,
			"anchorID", challenge.AnchorID,
			"challenge", challenge.Token,
			"error", err)
		return false, ErrChallengeCodeFailure
	}
	expiresAt := challenge.CreatedAt.Add(c.notificationTTL)
	if err := c.pushChallengeCode(challenge.UserID, challenge.AnchorID, code, expiresAt, delivery); err != nil {
		return false, err
	}
	return true, nil
}

// pushChallengeCode composes the notification carrying a challenge code,
// advertising the time left until the code expires, and pushes it to the queue
// to be sent.
func (c *CSP) pushChallengeCode(uID, anchorID internal.HexBytes, code string,
	expiresAt time.Time, delivery challengeDelivery,
) error {
	ch, err := notifications.NewNotificationChallenge(delivery.ctype, delivery.lang, uID, anchorID,
		delivery.to, code, delivery.org, time.Until(expiresAt).Round(time.Second).String())
	if err != nil {
		log.Warnw("error composing notification challenge",
			"userID", uID,
			"anchorID", anchorID,
			"error", err)
		return ErrNotificationFailure
	}
	ch.ExpiresAt = expiresAt
	if err := c.pushChallenge(ch); err != nil {
		log.Warnw("error pushing notification challenge",
			"userID", uID,
			"anchorID", anchorID,
			"error", err)
		return ErrNotificationFailure
	}
	return nil
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
	challenge, err := c.challengeOf(authTokenData)
	if err != nil {
		if err == db.ErrTokenNotFound {
			return ErrTokenExpired
		}
		return ErrStorageFailure
	}
	// reject tokens whose challenge has no stored secret. This covers legacy rows
	// created before per-token secrets existed, auth-only tokens, and challenges
	// already solved (whose secret was wiped), through this token or another one
	// sharing the challenge: in all cases the OTP would be derived from an empty
	// secret and thus trivially guessable, so the token is not OTP-verifiable. An
	// already-verified token is rejected the same way. ErrTokenExpired prompts
	// the client to restart the OTP flow rather than treating it as a hard failure.
	if authTokenData.Verified || challenge.Secret == "" {
		log.Warnw("verification attempted for token without challenge secret",
			"userID", authTokenData.UserID,
			"anchorID", authTokenData.AnchorID,
			"token", token)
		return ErrTokenExpired
	}
	// reject if the OTP window has passed
	if time.Since(challenge.CreatedAt) > c.notificationTTL {
		log.Warnw("OTP expired",
			"userID", authTokenData.UserID,
			"anchorID", authTokenData.AnchorID,
			"token", token)
		return ErrTokenExpired
	}
	// reject tokens whose challenge exhausted the maximum number of attempts,
	// which are counted per challenge so issuing more tokens grants no more
	if challenge.Attempts >= MaxChallengeAttempts {
		log.Warnw("too many challenge attempts",
			"userID", authTokenData.UserID,
			"anchorID", authTokenData.AnchorID,
			"token", token,
			"attempts", challenge.Attempts)
		return ErrTooManyAttempts
	}
	// verify the solution, and if the solution is not correct, atomically record
	// the failed attempt while enforcing the cap
	if !c.verifySolution(challenge.Secret, solution) {
		log.Warnw("challenge code does not match",
			"userID", authTokenData.UserID,
			"anchorID", authTokenData.AnchorID,
			"token", token)
		recorded, err := c.Storage.IncrementCSPAuthAttempts(challenge.Token, MaxChallengeAttempts)
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
		if err == db.ErrChallengeConsumed {
			// a concurrent verification solved the challenge first
			return ErrTokenExpired
		}
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
	return newAuthToken(), secret, code
}

// newAuthToken returns a new random bearer token.
func newAuthToken() internal.HexBytes {
	// a uuid.UUID is a [16]byte; slice its bytes directly to avoid the fallible
	// MarshalBinary call (and the panic path it would require).
	id := uuid.New()
	return internal.HexBytes(id[:])
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
