package csp

import (
	"crypto/sha256"
	"time"

	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/csp/storage"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/xlzd/gotp"
	"go.vocdoni.io/dvote/log"
)

// BundleAuthToken method generates a new authentication token for a user in
// a process of a bundle. It generates a new token, secret and code from the
// attempt number. It updates the user data in the storage and indexes the
// token. It composes the notification challenge and pushes it to the queue to
// be sent. It returns the token as HexBytes.
func (c *CSP) BundleAuthToken(bID, uID internal.HexBytes, to string,
	ctype notifications.ChallengeType,
) (
	internal.HexBytes, error,
) {
	// check the input parameters
	if len(bID) == 0 {
		return nil, ErrNoBundleID
	}
	if len(uID) == 0 {
		return nil, ErrNoUserID
	}
	// get user data
	userData, err := c.storage.User(uID)
	if err != nil {
		log.Warnw("error getting user data",
			"error", err,
			"userID", uID)
		return nil, ErrUserUnknown
	}
	// get the bundle from the user data
	bundle, ok := userData.Bundles[bID.String()]
	if !ok {
		log.Warnw("bundle not found in user data",
			"bundleID", bID,
			"userID", uID)
		return nil, ErrUserNotBelongsToBundle
	}

	// generate a new token, secret and code from the attempt number
	token, code, err := c.generateToken(uID, bundle)
	if err != nil {
		return nil, err
	}
	// set the new information in the process
	now := time.Now()
	bundle.LastAttempt = &now
	// update the election and the bundle in the user data
	userData.Bundles[bID.String()] = bundle
	// update the user data in the storage and index the token
	if err := c.storage.SetUser(*userData); err != nil {
		log.Warnw("error updating user data",
			"error", err,
			"userID", uID,
			"token", token)
		return nil, ErrStorageFailure
	}
	if err := c.storage.IndexAuthToken(uID, bID, &token); err != nil {
		log.Warnw("error indexing token",
			"error", err,
			"userID", uID,
			"token", token)
		return nil, ErrStorageFailure
	}
	// compose the notification challenge
	ch, err := notifications.NewNotificationChallenge(ctype, uID, bID, to, code)
	if err != nil {
		log.Warnw("error composing notification challenge",
			"error", err,
			"userID", uID,
			"bundleID", bID)
		return nil, ErrNotificationFailure
	}
	// push the challenge to the queue to be sent
	if err := c.notifyQueue.Push(*ch); err != nil {
		log.Warnw("error pushing notification challenge",
			"error", err,
			"userID", uID,
			"bundleID", bID)
		return nil, ErrNotificationFailure
	}
	// marshal the token and return it
	bToken, err := token.MarshalText()
	if err != nil {
		log.Warnw("error marshalling token",
			"error", err,
			"userID", uID,
			"token", token)
		return nil, ErrStorageFailure
	}
	return bToken, nil
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
	uuidToken := new(uuid.UUID)
	if err := uuidToken.UnmarshalText(token); err != nil {
		log.Warnw("error unmarshalling token",
			"error", err,
			"token", token)
		return ErrInvalidAuthToken
	}
	// get the user data from the token
	authToken, userData, err := c.storage.UserAuthToken(uuidToken)
	if err != nil {
		log.Warnw("error getting user data by token",
			"error", err,
			"token", token)
		return ErrUserUnknown
	}
	// get the process from the user data
	bundle, ok := userData.Bundles[authToken.BundleID.String()]
	if !ok {
		log.Warnw("bundle not found in user data",
			"bundleID", authToken.BundleID,
			"token", token,
			"userID", userData.ID)
		return ErrUserNotBelongsToBundle
	}
	// if the process has no token or the token does not match, return an error
	if authToken.Token == nil || authToken.Token.String() != token.String() {
		log.Warnw("invalid authentication token",
			"bundleID", authToken.BundleID,
			"tokenProvided", uuidToken,
			"tokenExpected", authToken.Token,
			"userID", userData.ID)
		return ErrInvalidAuthToken
	}
	// update the last attempt to the bundle in the user data
	t := time.Now()
	bundle.LastAttempt = &t
	userData.Bundles[authToken.BundleID.String()] = bundle
	// update the user data in the storage
	if err := c.storage.SetUser(*userData); err != nil {
		log.Warnw("error updating user data",
			"error", err,
			"userID", userData.ID,
			"bundleID", authToken.BundleID)
		return ErrStorageFailure
	}
	// verify the solution, and if the solution is not correct, return an error
	if !c.verifySolution(userData.ID, authToken.BundleID, solution) {
		log.Warnw("challenge code do not match",
			"bundleID", authToken.BundleID,
			"token", token,
			"userID", userData.ID,
			"solution", solution)
		return ErrChallengeCodeFailure
	}
	// set the token as verified
	if err := c.storage.VerifyAuthToken(uuidToken); err != nil {
		log.Warnw("error verifying token",
			"error", err,
			"token", token,
			"bundleID", authToken.BundleID,
			"userID", userData.ID)
		return ErrStorageFailure
	}
	return nil
}

// generateToken method generates a new authentication token for a user in a
// process. It checks if the process is already consumed for this user, and
// if the last attempt is found, checks the cooldown time. It generates a new
// challenge secret, challenge token and OTP code for the secret and the
// attempt number. It returns the token, the secret and the code respectively.
func (c *CSP) generateToken(uID internal.HexBytes, bundle storage.BundleData) (
	uuid.UUID, string, error,
) {
	// if last attempt is found, check the cooldown time
	if bundle.LastAttempt != nil {
		elapsed := time.Since(*bundle.LastAttempt)
		if elapsed < c.notificationCoolDownTime {
			log.Warnw("attempt cooldown time not reached",
				"bundleID", bundle.ID,
				"userID", uID,
				"elapsed", elapsed,
				"cooldown", c.notificationCoolDownTime)
			return uuid.UUID{}, "", ErrAttemptCoolDownTime
		}
	}
	// generate a new challenge secret and challenge token
	secret := otpSecret(uID, bundle.ID)
	// generate the OTP code for the secret and the attempt number
	otp := gotp.NewDefaultHOTP(secret.String())
	code := otp.At(0)
	return uuid.New(), code, nil
}

// verifySolution method verifies the solution for a user process. It generates
// the OTP code for the process secret and the attempt number and compares it
// with the solution. It returns true if the solution is correct, false
// otherwise.
func (c *CSP) verifySolution(uID, bID internal.HexBytes, solution string) bool {
	secret := otpSecret(uID, bID)
	// generate the OTP code for the secret and the attempt number
	otp := gotp.NewDefaultHOTP(secret.String())
	code := otp.At(0)
	// compare the generated code with the solution
	return code == solution
}

// otpSecret method generates a new OTP secret for a user and a bundle. The
// secret is generated by hashing the user ID and the bundle ID with SHA-256.
// It returns the secret as HexBytes.
func otpSecret(uID, bID internal.HexBytes) internal.HexBytes {
	hash := sha256.Sum256(append(uID, bID...))
	return hash[:]
}
