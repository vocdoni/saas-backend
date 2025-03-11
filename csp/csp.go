package csp

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/csp/storage"
	"github.com/vocdoni/saas-backend/csp/twofactor"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications"
	"github.com/xlzd/gotp"
	"go.mongodb.org/mongo-driver/mongo"
	"go.vocdoni.io/dvote/log"
)

// CSPConfig struct contains the configuration for the CSP service. It includes
// the database name, the MongoDB client, the notification cooldown time, the
// notification throttle time, the maximum notification attempts, the SMS
// service and the mail service.
type CSPConfig struct {
	// db stuff
	DBName      string
	MongoClient *mongo.Client
	// notification stuff
	NotificationCoolDownTime time.Duration
	NotificationThrottleTime time.Duration
	MaxNotificationAttempts  int
	SMSService               notifications.NotificationService
	MailService              notifications.NotificationService
}

// CSP struct contains the CSP service. It includes the storage, the
// notification queue, the maximum notification attempts, the notification
// throttle time and the notification cooldown time.
type CSP struct {
	storage     storage.Storage
	notifyQueue *twofactor.Queue

	maxNotificationAttempts  int
	notificationThrottleTime time.Duration
	notificationCoolDownTime time.Duration
}

// New method creates a new CSP service. It requires a CSPConfig struct with
// the configuration for the service. It returns the CSP service or an error
// if the storage fails to initialize. It initializes the storage with the
// MongoDB client and the database name provided in the configuration, and
// creates a new notification queue with the notification cooldown time, the
// notification throttle time, the SMS service and the mail service.
func New(ctx context.Context, config *CSPConfig) (*CSP, error) {
	stg := new(storage.MongoStorage)
	if err := stg.Init(&storage.MongoConfig{
		DBName: config.DBName,
		Client: config.MongoClient,
	}); err != nil {
		return nil, err
	}
	queue := twofactor.NewQueue(
		ctx,
		config.NotificationCoolDownTime,
		config.NotificationThrottleTime,
		config.SMSService,
		config.MailService,
	)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ch := <-queue.NotificationsSent:
				if ch.Success {
					var err error
					if ch.BundleID != nil {
						err = stg.SetBundleProcessAttempts(ch.UserID, ch.BundleID, ch.ProcessID, -1)
					} else {
						err = stg.SetProcessAttempts(ch.UserID, ch.ProcessID, -1)
					}
					if err != nil {
						log.Warnw("error updating bundle process attempts",
							"error", err,
							"userID", ch.UserID,
							"bundleID", ch.BundleID,
							"processID", ch.ProcessID)
					}
				} else {
					log.Warnw("challenge sending failed",
						"userID", ch.UserID,
						"processID", ch.ProcessID,
						"bundleID", ch.BundleID,
						"type", ch.Type)
				}
			}
		}
	}()
	return &CSP{
		storage:                  stg,
		notifyQueue:              queue,
		maxNotificationAttempts:  config.MaxNotificationAttempts,
		notificationThrottleTime: config.NotificationThrottleTime,
		notificationCoolDownTime: config.NotificationCoolDownTime,
	}, nil
}

// NewUserForProcesses method creates a new user data for a list of processes.
// It requires a user ID, a phone or email and a list of process IDs. It
// returns the user data created or an error if the user ID is not provided,
// if the phone or email is not provided or if there is no process ID.
func (c *CSP) NewUserForProcesses(uID internal.HexBytes, phone, mail string,
	eIDs ...internal.HexBytes,
) (*storage.UserData, error) {
	if len(uID) == 0 {
		return nil, ErrNoUserID
	}
	if len(phone) == 0 && len(mail) == 0 {
		return nil, ErrNoPhoneOrEmail
	}
	if len(eIDs) == 0 {
		return nil, ErrNoProcessID
	}
	user := &storage.UserData{
		ID:    uID,
		Phone: phone,
		Mail:  mail,
	}
	for _, eid := range eIDs {
		user.Processes[eid.String()] = storage.UserProcess{
			ID:                eid,
			RemainingAttempts: c.maxNotificationAttempts,
		}
	}
	return user, nil
}

// NewUserForBundle method creates a new user data for a bundle. It requires a
// user ID, a phone or email, a bundle ID and a list the bundle processes IDs.
// It returns the user data created or an error if the user ID is not provided,
// if the phone or email is not provided, if the bundle ID is not provided, if
// the process ID is not provided or if there is no process ID.
func (c *CSP) NewUserForBundle(uID internal.HexBytes, phone, mail string,
	bID internal.HexBytes, eIDs ...internal.HexBytes,
) (*storage.UserData, error) {
	if len(uID) == 0 {
		return nil, ErrNoUserID
	}
	if len(phone) == 0 && len(mail) == 0 {
		return nil, ErrNoPhoneOrEmail
	}
	if len(eIDs) == 0 {
		return nil, ErrNoProcessID
	}
	user := &storage.UserData{
		ID:    uID,
		Phone: phone,
		Mail:  mail,
	}
	bundle := storage.UserBundle{
		ID: bID,
	}
	for _, eid := range eIDs {
		bundle.Processes[eid.String()] = storage.UserProcess{
			ID:                eid,
			RemainingAttempts: c.maxNotificationAttempts,
		}
	}
	user.Bundles[bID.String()] = bundle
	return user, nil
}

// AddUser method registers the users to the storage. It calls the storage
// BultAddUser method with the list of users provided. The users should be
// created with the NewUserData method.
func (c *CSP) AddUsers(users []storage.UserData) error {
	return c.storage.AddUsers(users)
}

// BundleAuthToken method generates a new authentication token for a user in
// a process of a bundle. It generates a new token, secret and code from the
// attempt number. It updates the user data in the storage and indexes the
// token. It composes the notification challenge and pushes it to the queue to
// be sent. It returns the token as HexBytes.
func (c *CSP) BundleAuthToken(bID, pID, uID internal.HexBytes, to string,
	ctype twofactor.ChallengeType,
) (
	internal.HexBytes, error,
) {
	// check the input parameters
	if len(bID) == 0 {
		return nil, ErrNoBundleID
	}
	if len(pID) == 0 {
		return nil, ErrNoProcessID
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
	// get the process from the bundle
	process, ok := bundle.Processes[pID.String()]
	if !ok {
		log.Warnw("process not found in bundle",
			"processID", pID,
			"bundleID", bID,
			"userID", uID)
		return nil, ErrUserNotBelongsToProcess
	}
	// generate a new token, secret and code from the attempt number
	token, secret, code, err := c.generateToken(uID, process)
	if err != nil {
		return nil, err
	}
	// set the new information in the process
	process.AuthToken = &token
	process.ChallengeSecret = secret
	now := time.Now()
	process.LastAttempt = &now
	// update the election and the bundle in the user data
	bundle.Processes[pID.String()] = process
	userData.Bundles[bID.String()] = bundle
	// update the user data in the storage and index the token
	if err := c.storage.SetUser(*userData); err != nil {
		log.Warnw("error updating user data",
			"error", err,
			"userID", uID,
			"token", token,
			"secret", secret)
		return nil, ErrStorageFailure
	}
	if err := c.storage.IndexToken(uID, &token); err != nil {
		log.Warnw("error indexing token",
			"error", err,
			"userID", uID,
			"token", token)
		return nil, ErrStorageFailure
	}
	// compose the notification challenge
	ch, err := twofactor.NewNotificationChallenge(ctype, uID, bID, pID, to, code)
	if err != nil {
		log.Warnw("error composing notification challenge",
			"error", err,
			"userID", uID,
			"bundleID", bID,
			"processID", pID)
		return nil, ErrNotificationFailure
	}
	// push the challenge to the queue to be sent
	if err := c.notifyQueue.Push(*ch); err != nil {
		log.Warnw("error pushing notification challenge",
			"error", err,
			"userID", uID,
			"bundleID", bID,
			"processID", pID)
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

// ProcessAuthToken method generates a new authentication token for a user in a
// process. It generates a new token, secret and code from the attempt number.
// It updates the user data in the storage and indexes the token. It composes
// the notification challenge and pushes it to the queue to be sent. It returns
// the token as HexBytes.
func (c *CSP) ProcessAuthToken(pID, uID internal.HexBytes, to string,
	ctype twofactor.ChallengeType,
) (
	internal.HexBytes, error,
) {
	// check the input parameters
	if len(pID) == 0 {
		return nil, ErrNoProcessID
	}
	if len(uID) == 0 {
		return nil, ErrNoUserID
	}
	// compose the bundle user ID and get user data
	userData, err := c.storage.User(uID)
	if err != nil {
		log.Warnw("error getting user data",
			"error", err,
			"userID", uID)
		return nil, ErrUserUnknown
	}
	// get the process from the bundle
	process, ok := userData.Processes[pID.String()]
	if !ok {
		log.Warnw("process not found in user data",
			"processID", pID,
			"userID", uID)
		return nil, ErrUserNotBelongsToProcess
	}
	// generate a new token, secret and code from the attempt number
	token, secret, code, err := c.generateToken(uID, process)
	if err != nil {
		return nil, err
	}
	// set the new information in the process
	process.AuthToken = &token
	process.ChallengeSecret = secret
	now := time.Now()
	process.LastAttempt = &now
	// update the process and the bundle in the user data
	userData.Processes[pID.String()] = process
	// update the user data in the storage and index the token
	if err := c.storage.SetUser(*userData); err != nil {
		log.Warnw("error updating user data",
			"error", err,
			"userID", uID,
			"token", token,
			"secret", secret)
		return nil, ErrStorageFailure
	}
	if err := c.storage.IndexToken(uID, &token); err != nil {
		log.Warnw("error indexing token",
			"error", err,
			"userID", uID,
			"token", token)
		return nil, ErrStorageFailure
	}
	// compose the notification challenge
	ch, err := twofactor.NewNotificationChallenge(ctype, uID, nil, pID, to, code)
	if err != nil {
		log.Warnw("error composing notification challenge",
			"error", err,
			"userID", uID,
			"processID", pID)
		return nil, ErrNotificationFailure
	}
	// push the challenge to the queue to be sent
	if err := c.notifyQueue.Push(*ch); err != nil {
		log.Warnw("error pushing notification challenge",
			"error", err,
			"userID", uID,
			"processID", pID)
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

// VerifyBundleProcessAuthToken method verifies the authentication token for
// a user in a process of a bundle. It gets the user data from the token and
// checks if the process is already consumed. It checks if the process is
// related to the user and if the token matches. It verifies the solution and
// updates the user data in the storage. It returns an error if the process is
// already consumed, if the process is not related to the user, if the token
// does not match, if the solution is not correct or if there is an error
// updating the user data.
func (c *CSP) VerifyBundleProcessAuthToken(bID, pID internal.HexBytes,
	token *uuid.UUID, solution string,
) error {
	// get the user data from the token
	userData, err := c.storage.UserByToken(token)
	if err != nil {
		log.Warnw("error getting user data by token",
			"error", err,
			"token", token)
		return ErrUserUnknown
	}
	// get the process from the user data
	bundle, ok := userData.Bundles[bID.String()]
	if !ok {
		log.Warnw("bundle not found in user data",
			"bundleID", bID,
			"token", token,
			"userID", userData.ID)
		return ErrUserNotBelongsToBundle
	}
	process, ok := bundle.Processes[pID.String()]
	if !ok {
		log.Warnw("process not found in bundle",
			"bundleID", bID,
			"processID", pID,
			"token", token,
			"userID", userData.ID)
		return ErrUserNotBelongsToProcess
	}
	// check if the process is already consumed
	if process.Consumed {
		log.Warnw("process already consumed",
			"bundleID", bID,
			"processID", pID,
			"token", token,
			"userID", userData.ID)
		return ErrUserAlreadyVerified
	}
	// if the process has no token or the token does not match, return an error
	if process.AuthToken == nil || process.AuthToken.String() != token.String() {
		log.Warnw("invalid authentication token",
			"bundleID", bID,
			"processID", pID,
			"tokenProvided", token,
			"tokenExpected", process.AuthToken,
			"userID", userData.ID)
		return ErrInvalidAuthToken
	}
	// verify the solution, and if the solution is not correct, return an error
	if c.verifySolution(process, solution) {
		log.Warnw("challenge code do not match",
			"bundleID", bID,
			"processID", pID,
			"token", token,
			"userID", userData.ID,
			"secret", process.ChallengeSecret,
			"solution", solution)
		return ErrChallengeCodeFailure
	}
	// update the process in the user data
	process.Consumed = true
	bundle.Processes[pID.String()] = process
	userData.Bundles[bID.String()] = bundle
	// update the user data in the storage
	if err := c.storage.SetUser(*userData); err != nil {
		log.Warnw("error updating user data",
			"error", err,
			"userID", userData.ID,
			"bundleID", bID,
			"processID", pID)
		return ErrStorageFailure
	}
	return nil
}

// VerifyProcessAuthToken method verifies the authentication token for a user
// in a process. It gets the user data from the token and checks if the process
// is already consumed. It checks if the process is related to the user and if
// the token matches. It verifies the solution and updates the user data in the
// storage. It returns an error if the process is already consumed, if the
// process is not related to the user, if the token does not match, if the
// solution is not correct or if there is an error updating the user data.
func (c *CSP) VerifyProcessAuthToken(pID internal.HexBytes, token *uuid.UUID,
	solution string,
) error {
	// get the user data from the token
	userData, err := c.storage.UserByToken(token)
	if err != nil {
		log.Warnw("error getting user data by token",
			"error", err,
			"token", token)
		return ErrUserUnknown
	}
	// get the process from the user data
	process, ok := userData.Processes[pID.String()]
	if !ok {
		log.Warnw("process not found in user data",
			"processID", pID,
			"token", token,
			"userID", userData.ID)
		return ErrUserNotBelongsToProcess
	}
	// mark the process as consumed
	if process.Consumed {
		log.Warnw("process already consumed",
			"processID", pID,
			"token", token,
			"userID", userData.ID)
		return ErrUserAlreadyVerified
	}
	// if the process has no token or the token does not match, return an error
	if process.AuthToken == nil || process.AuthToken.String() != token.String() {
		log.Warnw("invalid authentication token",
			"processID", pID,
			"tokenProvided", token,
			"tokenExpected", process.AuthToken,
			"userID", userData.ID)
		return ErrInvalidAuthToken
	}
	// verify the solution, and if the solution is not correct, return an error
	if c.verifySolution(process, solution) {
		log.Warnw("challenge code do not match",
			"processID", pID,
			"token", token,
			"userID", userData.ID,
			"secret", process.ChallengeSecret,
			"solution", solution)
		return ErrChallengeCodeFailure
	}
	// update the process in the user data
	process.Consumed = true
	userData.Processes[pID.String()] = process
	// update the user data in the storage
	if err := c.storage.SetUser(*userData); err != nil {
		log.Warnw("error updating user data",
			"error", err,
			"userID", userData.ID,
			"processID", pID)
		return ErrStorageFailure
	}
	// if the solution is not correct, return an error
	if !process.Consumed {
		log.Warnw("challenge code do not match",
			"processID", pID,
			"token", token,
			"userID", userData.ID,
			"secret", process.ChallengeSecret,
			"solution", solution)
		return ErrChallengeCodeFailure
	}
	return nil
}

// generateToken method generates a new authentication token for a user in a
// process. It checks if the process is already consumed for this user, and
// if the last attempt is found, checks the cooldown time. It generates a new
// challenge secret, challenge token and OTP code for the secret and the
// attempt number. It returns the token, the secret and the code respectively.
func (c *CSP) generateToken(uID internal.HexBytes, process storage.UserProcess) (
	uuid.UUID, string, string, error,
) {
	// check if the process is already consumed for this user
	if process.Consumed {
		log.Warnw("process already consumed",
			"processID", process.ID,
			"userID", uID)
		return uuid.UUID{}, "", "", ErrUserAlreadyVerified
	}
	// if last attempt is found, check the cooldown time
	if process.LastAttempt != nil {
		elapsed := time.Since(*process.LastAttempt)
		if elapsed < c.notificationCoolDownTime {
			log.Warnw("attempt cooldown time not reached",
				"processID", process.ID,
				"userID", uID,
				"elapsed", elapsed,
				"cooldown", c.notificationCoolDownTime)
			return uuid.UUID{}, "", "", ErrAttemptCoolDownTime
		}
	}
	// if there are no remaining attempts, return an error
	if process.RemainingAttempts <= 0 {
		log.Warnw("too many notifications attempts",
			"processID", process.ID,
			"userID", uID)
		return uuid.UUID{}, "", "", ErrTooManyAttempts
	}
	// calculate the attempt number
	attemptNo := c.maxNotificationAttempts - process.RemainingAttempts
	// generate a new challenge secret and challenge token
	secret := gotp.RandomSecret(16)
	// generate the OTP code for the secret and the attempt number
	otp := gotp.NewDefaultHOTP(secret)
	code := otp.At(attemptNo)
	return uuid.New(), secret, code, nil
}

// verifySolution method verifies the solution for a user process. It generates
// the OTP code for the process secret and the attempt number and compares it
// with the solution. It returns true if the solution is correct, false
// otherwise.
func (c *CSP) verifySolution(process storage.UserProcess, solution string) bool {
	// calculate the attempt number
	attemptNo := c.maxNotificationAttempts - process.RemainingAttempts
	// generate the OTP code for the secret and the attempt number
	otp := gotp.NewDefaultHOTP(process.ChallengeSecret)
	code := otp.At(attemptNo)
	// compare the generated code with the solution
	return code == solution
}
