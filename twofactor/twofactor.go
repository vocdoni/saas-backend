package twofactor

import (
	"context"
	"fmt"
	"os"
	"path"
	"sync"
	"time"

	"github.com/arnaucube/go-blindsecp256k1"
	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
	"github.com/xlzd/gotp"
	dvotedb "go.vocdoni.io/dvote/db"
	"go.vocdoni.io/dvote/db/metadb"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// The twofactor service is responsible for managing the two-factor authentication
// using any of the supported notification services. It supports authentication for
// both individual processes and process bundles, allowing users to authenticate once
// for multiple voting processes.

const (
	// DefaultMaxSMSattempts defines the default maximum number of SMS allowed attempts.
	DefaultMaxSMSattempts = 5
	// DefaultSMScoolDownTime defines the default cool down time window for sending challenges.
	DefaultSMScoolDownTime = 2 * time.Minute
	// DefaultSMSthrottleTime is the default throttle time for the SMS provider API.
	DefaultSMSthrottleTime = time.Millisecond * 500
	// DefaultSMSqueueMaxRetries is how many times to retry delivering an SMS in case upstream provider returns an error
	DefaultSMSqueueMaxRetries = 10
)

// NotifServices holds the notification services used for two-factor authentication.
type NotifServices struct {
	SMS  notifications.NotificationService // Service for sending SMS notifications
	Mail notifications.NotificationService // Service for sending email notifications
}

// TwofactorConfig contains the configuration parameters for the two-factor authentication service.
type TwofactorConfig struct {
	NotificationServices NotifServices // Services for sending notifications
	MaxAttempts          int           // Maximum number of authentication attempts allowed
	CoolDownTime         time.Duration // Time to wait between authentication attempts
	ThrottleTime         time.Duration // Time to throttle notification sending
	MaxRetries           int           // Maximum number of retries for failed notification deliveries
	PrivKey              string        // Private key for signing
	MongoURI             string        // MongoDB URI
}

// Twofactor is the main service that handles two-factor authentication for processes and process bundles.
type Twofactor struct {
	stg                  Storage          // Storage for authentication data
	notificationServices NotifServices    // Services for sending notifications
	maxAttempts          int              // Maximum number of authentication attempts allowed
	coolDownTime         time.Duration    // Time to wait between authentication attempts
	throttleTime         time.Duration    // Time to throttle notification sending
	maxRetries           int              // Maximum number of retries for failed notification deliveries
	smsQueue             *Queue           // Queue for SMS notifications
	mailQueue            *Queue           // Queue for email notifications
	otpSalt              string           // Salt for OTP generation
	Signer               *SaltedKey       // Signer for authentication tokens
	keys                 dvotedb.Database // Database for storing keys
	keysLock             sync.RWMutex     // Lock for concurrent access to keys
}

// SendChallengeFunc is the function that sends the authentication challenge to a contact (phone number or email).
type SendChallengeFunc func(contact string, challenge string) error

// MailNotification handles sending email notifications for two-factor authentication.
type MailNotification struct {
	MailNotificationService notifications.NotificationService // Service for sending email notifications
	ToAddress               string                            // Recipient email address
	Subject                 string                            // Email subject
	Body                    string                            // Email body
}

// SmsNotification handles sending SMS notifications for two-factor authentication.
type SmsNotification struct {
	SmsNotificationService notifications.NotificationService // Service for sending SMS notifications
	ToNumber               string                            // Recipient phone number
	Subject                string                            // SMS subject
	Body                   string                            // SMS body
}

// NewMailNotifcation creates a new MailNotification instance with the provided notification service.
func NewMailNotifcation(notifService notifications.NotificationService) *MailNotification {
	MailNotificationService := notifService
	return &MailNotification{MailNotificationService, "", "", ""}
}

// NewSmsNotifcation creates a new SmsNotification instance with the provided notification service.
func NewSmsNotifcation(notifService notifications.NotificationService) *SmsNotification {
	SmsNotificationService := notifService
	return &SmsNotification{SmsNotificationService, "", "", ""}
}

// SendChallenge sends an authentication challenge to the specified email address.
func (mf *MailNotification) SendChallenge(mail string, challenge string) error {
	notif, err := mailtemplates.VerifyOTPCodeNotification.ExecTemplate(struct {
		Code string
	}{challenge})
	if err != nil {
		return err
	}
	notif.ToAddress = mail
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return mf.MailNotificationService.SendNotification(ctx, notif)
}

// SendChallenge sends an authentication challenge to the specified phone number.
func (sn *SmsNotification) SendChallenge(phone string, challenge string) error {
	to, err := internal.SanitizeAndVerifyPhoneNumber(phone)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	notif, err := mailtemplates.VerifyOTPCodeNotification.ExecTemplate(struct {
		Code string
	}{challenge})
	if err != nil {
		return err
	}
	notif.ToNumber = to
	// return tf.notificationServices.Mail.SendNotification(ctx, notif)
	return sn.SmsNotificationService.SendNotification(ctx, notif)
}

// New creates and initializes a new Twofactor service with the provided configuration.
// It sets up the notification services, database, and queues for handling authentication requests.
func (tf *Twofactor) New(conf *TwofactorConfig) (*Twofactor, error) {
	if conf == nil {
		return nil, nil
	}
	if conf.NotificationServices.Mail == nil || conf.NotificationServices.SMS == nil {
		return nil, fmt.Errorf("no notification services defined")
	}
	maxAttempts := DefaultMaxSMSattempts
	if conf.MaxAttempts != 0 {
		maxAttempts = conf.MaxAttempts
	}
	coolDownTime := DefaultSMScoolDownTime
	if conf.CoolDownTime != 0 {
		coolDownTime = conf.CoolDownTime * time.Minute
	}
	throttleTime := DefaultSMSthrottleTime
	if conf.ThrottleTime != 0 {
		throttleTime = conf.ThrottleTime * time.Millisecond
	}
	maxRetries := DefaultSMSqueueMaxRetries
	if conf.MaxRetries != 0 {
		maxRetries = conf.MaxRetries
	}

	// ECDSA/Blind signer
	var err error
	if tf.Signer, err = NewSaltedKey(conf.PrivKey); err != nil {
		return nil, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot get user home directory: %v", err)
	}
	dataDir := path.Join(home, ".saas-backend")
	err = os.MkdirAll(dataDir, os.ModePerm)
	if err != nil {
		panic(fmt.Sprintf("cannot create data directory: %v", err))
	}

	tf.keys, err = metadb.New(dvotedb.TypePebble, dataDir)
	if err != nil {
		return nil, fmt.Errorf("cannot create the database: %v", err)
	}

	if conf.MongoURI != "" {
		tf.stg = new(MongoStorage)
		if err := tf.stg.Init(
			conf.MongoURI,
			maxAttempts,
			coolDownTime,
		); err != nil {
			return nil, err
		}
	} else {
		tf.stg = new(JSONstorage)
		if err := tf.stg.Init(
			path.Join(dataDir, "storage"),
			maxAttempts,
			coolDownTime,
		); err != nil {
			return nil, err
		}
	}

	tf.smsQueue = newQueue(
		coolDownTime,
		throttleTime,
		[]SendChallengeFunc{NewSmsNotifcation(conf.NotificationServices.SMS).SendChallenge},
	)
	go tf.smsQueue.run()
	go tf.queueController(tf.smsQueue)

	tf.mailQueue = newQueue(
		coolDownTime,
		throttleTime,
		[]SendChallengeFunc{NewMailNotifcation(conf.NotificationServices.Mail).SendChallenge},
	)
	go tf.mailQueue.run()
	go tf.queueController(tf.mailQueue)

	tf.notificationServices = conf.NotificationServices
	tf.maxAttempts = maxAttempts
	tf.coolDownTime = coolDownTime
	tf.throttleTime = throttleTime
	tf.maxRetries = maxRetries
	tf.otpSalt = gotp.RandomSecret(8)

	return tf, nil
}

// Init initializes the handler.
// First argument is the maximum SMS challenge attempts per user and election.
// Second is the data directory (mandatory).
// Third is the SMS cooldown time in milliseconds (optional).
// Fourth is the SMS throttle time in milliseconds (optional).
// This function is deprecated in favor of New.

// queueController handles the response queue for notification delivery.
// It processes responses from the notification service and updates the storage accordingly.
func (tf *Twofactor) queueController(queue *Queue) {
	for {
		r := <-queue.response
		if r.success {
			if err := tf.stg.SetAttempts(r.userID, r.electionID, -1); err != nil {
				log.Warnw("challenge cannot be sent", "error", err)
			} else {
				log.Infow("challenge successfully sent", "challenge", r.String(), "userID", r.userID.String())
			}
		} else {
			log.Warnw("challenge sending failed", "challenge", r.String())
		}
	}
}

// Indexer takes a unique user identifier and returns the list of processIDs where
// the user is eligible for participation. This includes both individual processes and
// process bundles. This is a helper function that might not be implemented in all cases.
func (tf *Twofactor) Indexer(participantId, bundleId, electionId string) []Election {
	if len(participantId) == 0 {
		log.Warnw("no participant ID provided")
		return nil
	}

	// create userID either based on bundleId or electionId
	var userID internal.HexBytes
	switch {
	case len(bundleId) != 0:
		bundleIDBytes := internal.HexBytes{}
		if err := bundleIDBytes.FromString(bundleId); err != nil {
			return nil
		}
		userID = buildUserID(participantId, bundleIDBytes)
	case len(electionId) != 0:
		electionIDBytes := internal.HexBytes{}
		if err := electionIDBytes.FromString(electionId); err != nil {
			return nil
		}
		userID = buildUserID(participantId, electionIDBytes)
	default:
		return nil
	}

	user, err := tf.stg.User(userID)
	if err != nil {
		log.Warnw("cannot get indexer elections", "error", err)
		return nil
	}
	// Get the last two digits of the phone and return them as extraData
	contact := user.Mail
	if contact == "" {
		contact = user.Phone
	}
	if contact != "" {
		if len(contact) < 3 {
			contact = ""
		} else {
			contact = contact[len(contact)-2:]
		}
	}
	indexerElections := []Election{}
	for _, e := range user.Elections {
		ie := Election{
			RemainingAttempts: e.RemainingAttempts,
			Consumed:          e.Consumed,
			ElectionID:        e.ElectionID,
			ExtraData:         []string{user.ExtraData, contact},
			Voted:             e.Voted,
		}
		indexerElections = append(indexerElections, ie)
	}
	return indexerElections
}

// AddProcess adds a process or process bundle to the two-factor authentication service.
// It registers all participants from the provided census to enable them to authenticate
// for the process or process bundle.
func (tf *Twofactor) AddProcess(
	pubCensusType db.CensusType,
	orgParticipants []db.CensusMembershipParticipant,
) error {
	users := make([]UserData, len(orgParticipants))
	var userID internal.HexBytes
	for i, participant := range orgParticipants {
		bundleElectionId := internal.HexBytes{}
		if err := bundleElectionId.FromString(participant.BundleId); err != nil {
			log.Warnw("invalid bundleId format", "line", i, "bundleId", participant.BundleId)
			continue
		}
		if len(participant.BundleId) != 0 {
			userID = buildUserID(participant.ParticipantNo, bundleElectionId)
		} else if len(participant.ElectionIds) != 0 && len(participant.ElectionIds[0]) != 0 {
			userID = buildUserID(participant.ParticipantNo, participant.ElectionIds[0])
		} else {
			log.Warnw("invalid electionId format", "line", i, "electionId", participant.ElectionIds)
			continue
		}
		participant.ElectionIds = append(participant.ElectionIds, bundleElectionId)
		user := UserData{
			UserID: userID,
			Phone:  participant.HashedPhone,
			Mail:   participant.HashedEmail,
		}
		user.Elections = make(map[string]UserElection, len(participant.ElectionIds))
		for _, e := range HexBytesToElection(participant.ElectionIds, tf.stg.MaxAttempts()) {
			user.Elections[e.ElectionID.String()] = e
		}
		users[i] = user
		log.Debugw("user added to process", "userID", userID.String(), "electionIDs", formatElectionIds(participant.ElectionIds))
	}
	if err := tf.stg.BulkAddUser(users); err != nil {
		log.Warnw("cannot bulk add users", "error", err)
		return fmt.Errorf("cannot bulk add users: %w", err)
	}
	return nil
}

// InitiateAuth initiates the authentication process for a user.
// It generates a challenge and sends it to the user's contact (email or phone)
// via the specified notification type. This works for both individual processes
// and process bundles, where electionID can be either a process ID or a bundle ID.
func (tf *Twofactor) InitiateAuth(
	bundleId string,
	participantId string,
	contact string,
	notifType notifications.NotificationType,
) AuthResponse {
	// If first step, build new challenge
	if len(participantId) == 0 || len(bundleId) == 0 {
		return AuthResponse{Error: "incorrect auth data fields"}
	}
	bundleIDBytes := internal.HexBytes{}
	if err := bundleIDBytes.FromString(bundleId); err != nil {
		return AuthResponse{Error: "invalid bundleId format"}
	}
	userID := buildUserID(participantId, bundleIDBytes)

	// Generate challenge and authentication token
	// We need to ensure the challenge secret is a valid base32-encoded string
	// Instead of concatenating userID.String() (hex) with otpSalt (base32),
	// we'll use a different approach to create a unique secret per user
	challengeSecret := gotp.RandomSecret(16) // Use a fresh random secret
	atoken := uuid.New()

	// Get the phone number. This methods checks for bundleId and user verification status.
	_, _, attemptNo, err := tf.stg.NewAttempt(userID, bundleIDBytes, challengeSecret, &atoken)
	if err != nil {
		log.Warnw("new attempt for user failed", "userID", userID.String(), "error", err)
		return AuthResponse{Error: err.Error()}
	}
	if contact == "" {
		log.Warnw("phone is nil for user", "userID", userID.String())
		return AuthResponse{Error: "no phone for this user data"}
	}
	// Enqueue to send the challenge
	challenge := gotp.NewDefaultHOTP(challengeSecret)
	// Generate the OTP code using the attempt number
	otpCode := challenge.At(attemptNo)

	if notifType == notifications.Email {
		if err := tf.mailQueue.add(userID, bundleIDBytes, contact, otpCode); err != nil {
			log.Warnw("cannot enqueue challenge", "error", err)
			return AuthResponse{Error: "problem with Email challenge system"}
		}
		log.Infow("user challenged", "userID", userID.String(), "otpCode", otpCode, "contact", contact)
	} else if notifType == notifications.SMS {
		if err := tf.smsQueue.add(userID, bundleIDBytes, contact, otpCode); err != nil {
			log.Warnw("cannot enqueue challenge", "error", err)
			return AuthResponse{Error: "problem with SMS challenge system"}
		}
		log.Infow("user challenged", "userID", userID.String(), "otpCode", otpCode, "contact", contact)
	} else {
		return AuthResponse{Error: "invalid notification type"}
	}

	return AuthResponse{
		Success:   true,
		AuthToken: &atoken,
		Response:  []string{contact[len(contact)-2:]},
	}
}

// Auth verifies the authentication challenge response from a user.
// If successful, it returns a token that can be used for signing.
// This works for both individual processes and process bundles.
func (tf *Twofactor) Auth(bundleId string, authToken *uuid.UUID, authData []string) AuthResponse {
	if authToken == nil || len(authData) != 1 {
		return AuthResponse{Error: "auth token not provided or missing auth data"}
	}
	solution := authData[0]

	bundleIdBytes := internal.HexBytes{}
	bundleIdBytes.SetString(bundleId)
	// Verify the challenge solution
	if err := tf.stg.VerifyChallenge(bundleIdBytes, authToken, solution); err != nil {
		log.Warnw("verify challenge failed", "solution", solution, "error", err)
		return AuthResponse{Error: "challenge not completed:" + err.Error()}
	}

	// for salted ECDSA
	// token := tf.NewRequestKey()
	// for salted blind
	// token, err := tf.stg.NewBlindRequestKey(token)
	// if err != nil {
	// 	return AuthResponse{Error: "error getting new token:" + err.Error()}
	// }
	// tokenR := r.BytesUncompressed()

	log.Infow("new user registered", "challenge", authData[0])
	return AuthResponse{
		Response:  []string{"challenge resolved"},
		AuthToken: authToken,
		Success:   true,
	}
}

// formatElectionIds converts a slice of internal.HexBytes to a string representation
// for proper logging of binary data.
func formatElectionIds(ids []internal.HexBytes) string {
	if len(ids) == 0 {
		return "[]"
	}

	result := "["
	for i, id := range ids {
		if i > 0 {
			result += ", "
		}
		result += id.String()
	}
	result += "]"
	return result
}

// Sign creates a cryptographic signature for the provided message using the specified signature type.
// It requires a valid token obtained from a successful authentication.
// For process bundles, the electionID should be the bundle ID or the first process ID in the bundle.
func (tf *Twofactor) Sign(authToken uuid.UUID, token, msg, electionID internal.HexBytes, bundleId, sigType string) AuthResponse {
	switch sigType {
	case SignatureTypeBlind:
		r, err := blindsecp256k1.NewPointFromBytesUncompressed(token)
		if err != nil {
			return AuthResponse{
				Success: false,
				Error:   err.Error(),
			}
		}
		signature, err := tf.SignBlind(r, msg, nil)
		if err != nil {
			return AuthResponse{
				Success: false,
				Error:   err.Error(),
			}
		}
		return AuthResponse{
			Success:   true,
			Signature: signature,
		}
	case SignatureTypeEthereum:
		user, err := tf.stg.GetUserFromToken(&authToken)
		if err != nil {
			return AuthResponse{
				Success: false,
				Error:   err.Error(),
			}
		}
		// find the election and check the solution
		procID := electionID.String()
		if bundleId != "" {
			procID = bundleId
		}
		election, ok := user.Elections[procID]
		if !ok {
			return AuthResponse{
				Success: false,
				Error:   ErrUserNotBelongsToElection.Error(),
			}
		}
		if !election.Consumed {
			return AuthResponse{
				Success: false,
				Error:   "user has not completed the challenge",
			}
		}

		if election.Voted != nil {
			return AuthResponse{
				Success: false,
				Error:   "user already voted",
			}
		}

		caBundle := &models.CAbundle{
			ProcessId: electionID,
			Address:   msg,
		}
		caBundleBytes, err := proto.Marshal(caBundle)
		if err != nil {
			return AuthResponse{
				Success: false,
				Error:   err.Error(),
			}
		}
		// when Salted key is being used use the sign.go signature function instead
		signature, err := tf.Signer.SignECDSA(nil, caBundleBytes)
		if err != nil {
			return AuthResponse{
				Success: false,
				Error:   err.Error(),
			}
		}
		// Mark the user as voted
		election.Voted = msg
		user.Elections[electionID.String()] = election
		if err := tf.stg.UpdateUser(user); err != nil {
			return AuthResponse{
				Success: false,
				Error:   err.Error(),
			}
		}

		return AuthResponse{
			Success:   true,
			Signature: signature,
		}
	default:
		return AuthResponse{
			Success: false,
			Error:   "invalid signature type",
		}
	}
}
