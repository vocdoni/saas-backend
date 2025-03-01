package twofactor

import (
	"context"
	"encoding/hex"
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
	"github.com/xlzd/gotp"
	dvotedb "go.vocdoni.io/dvote/db"
	"go.vocdoni.io/dvote/db/metadb"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// The twofactor service is responsible for managing the two-factor authentication
// using any of the supported notification services.

const (
	// DefaultMaxSMSattempts defines the default maximum number of SMS allowed attempts.
	DefaultMaxSMSattempts = 5
	// DefaultSMScoolDownTime defines the default cool down time window for sending challenges.
	DefaultSMScoolDownTime = 2 * time.Minute
	// DefaultSMSthrottleTime is the default throttle time for the SMS provider API.
	DefaultSMSthrottleTime = time.Millisecond * 500
	// DefaultSMSqueueMaxRetries is how many times to retry delivering an SMS in case upstream provider returns an error
	DefaultSMSqueueMaxRetries = 10
	// DefaultPhoneCountry defines the default country code for phone numbers.
	DefaultPhoneCountry = "ES"
)

type NotifServices struct {
	SMS  notifications.NotificationService
	Mail notifications.NotificationService
}

type TwofactorConfig struct {
	NotificationServices NotifServices
	MaxAttempts          int
	CoolDownTime         time.Duration
	ThrottleTime         time.Duration
	MaxRetries           int
	PrivKey              string
}

type Twofactor struct {
	stg                  *JSONstorage
	notificationServices NotifServices
	maxAttempts          int
	coolDownTime         time.Duration
	throttleTime         time.Duration
	maxRetries           int
	smsQueue             *Queue
	mailQueue            *Queue
	otpSalt              string
	Signer               *SaltedKey
	keys                 dvotedb.Database
	keysLock             sync.RWMutex
}

// SendChallengeFunc is the function that sends the SMS challenge to a phone number.
type SendChallengeFunc func(contact string, challenge string) error

type MailNotification struct {
	MailNotificationService notifications.NotificationService
	ToAddress               string
	Subject                 string
	Body                    string
}

type SmsNotification struct {
	SmsNotificationService notifications.NotificationService
	ToNumber               string
	Subject                string
	Body                   string
}

func NewMailNotifcation(notifService notifications.NotificationService) *MailNotification {
	MailNotificationService := notifService
	return &MailNotification{MailNotificationService, "", "", ""}
}

func NewSmsNotifcation(notifService notifications.NotificationService) *SmsNotification {
	SmsNotificationService := notifService
	return &SmsNotification{SmsNotificationService, "", "", ""}
}

func (mf *MailNotification) SendChallenge(mail string, challenge string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	notif := &notifications.Notification{
		ToAddress: mail,
		Subject:   "Vocdoni verification code",
		PlainBody: fmt.Sprintf("Your authentication code is %s", challenge),
		Body:      fmt.Sprintf("Your authentication code is %s", challenge),
	}
	// return tf.notificationServices.Mail.SendNotification(ctx, notif)
	return mf.MailNotificationService.SendNotification(ctx, notif)
}

func (sn *SmsNotification) SendChallenge(phone string, challenge string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	notif := &notifications.Notification{
		ToNumber:  phone,
		Subject:   "Vocdoni verification code",
		PlainBody: fmt.Sprintf("Your authentication code is %s", challenge),
	}
	// return tf.notificationServices.Mail.SendNotification(ctx, notif)
	return sn.SmsNotificationService.SendNotification(ctx, notif)
}

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

	tf.stg = new(JSONstorage)
	if err := tf.stg.Init(
		path.Join(dataDir, "storage"),
		maxAttempts,
		coolDownTime,
	); err != nil {
		return nil, err
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

func (tf *Twofactor) queueController(queue *Queue) {
	for {
		r := <-queue.response
		if r.success {
			if err := tf.stg.SetAttempts(r.userID, r.electionID, -1); err != nil {
				log.Warnf("challenge cannot be sent: %v", err)
			} else {
				log.Infof("%s: challenge successfully sent to user %s", r, r.userID)
			}
		} else {
			log.Warnf("%s: challenge sending failed", r)
		}
	}
}

// Indexer takes a unique user identifier and returns the list of processIDs where
// the user is elegible for participation. This is a helper function that might not
// be implemented (depends on the handler use case).
func (tf *Twofactor) Indexer(userID internal.HexBytes) []Election {
	user, err := tf.stg.User(userID)
	if err != nil {
		log.Warnf("cannot get indexer elections: %v", err)
		return nil
	}
	// Get the last two digits of the phone and return them as extraData
	contact := ""
	if user.Contact != "" {
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
		}
		indexerElections = append(indexerElections, ie)
	}
	return indexerElections
}

func (tf *Twofactor) AddProcess(
	pubCensusType db.CensusType,
	orgParticipants []db.CensusMembershipParticipant,
) error {
	for i, participant := range orgParticipants {
		userID := make(internal.HexBytes, hex.EncodedLen(len(participant.ParticipantNo)))
		hex.Encode(userID, []byte(participant.ParticipantNo))

		electionId := internal.HexBytes{}
		if err := electionId.FromString(participant.ElectionID); err != nil {
			return fmt.Errorf("wrong electionID at participant %d", i)
		}
		electionIDs := []internal.HexBytes{electionId}

		if err := tf.stg.AddUser(userID, electionIDs, participant.HashedEmail, participant.HashedPhone, ""); err != nil {
			log.Warnf("cannot add user from line %d", i)
		}
		log.Debugf("user %s added to process %s", userID, electionIDs)
	}
	// log.Debug(tf.stg.String())
	return nil
}

func (tf *Twofactor) InitiateAuth(
	electionID []byte,
	userId []byte,
	contact string,
	notifType notifications.NotificationType,
) AuthResponse {
	// If first step, build new challenge
	if len(userId) == 0 {
		return AuthResponse{Error: "incorrect auth data fields"}
	}
	// var userID internal.HexBytes
	// if err := userID (userId); err != nil {
	// 	return AuthResponse{Error: "incorrect format for userId"}
	// }
	userID := internal.HexBytes(userId)

	// Generate challenge and authentication token
	// We need to ensure the challenge secret is a valid base32-encoded string
	// Instead of concatenating userID.String() (hex) with otpSalt (base32),
	// we'll use a different approach to create a unique secret per user
	challengeSecret := gotp.RandomSecret(16) // Use a fresh random secret
	atoken := uuid.New()

	// Get the phone number. This methods checks for electionID and user verification status.
	_, _, attemptNo, err := tf.stg.NewAttempt(userID, electionID, challengeSecret, &atoken)
	if err != nil {
		log.Warnf("new attempt for user %s failed: %v", userID, err)
		return AuthResponse{Error: err.Error()}
	}
	if contact == "" {
		log.Warnf("phone is nil for user %s", userID)
		return AuthResponse{Error: "no phone for this user data"}
	}
	// Enqueue to send the challenge
	challenge := gotp.NewDefaultHOTP(challengeSecret)
	// Generate the OTP code using the attempt number
	otpCode := challenge.At(attemptNo)

	if notifType == notifications.Email {
		if err := tf.mailQueue.add(userID, electionID, contact, otpCode); err != nil {
			log.Errorf("cannot enqueue challenge: %v", err)
			return AuthResponse{Error: "problem with Email challenge system"}
		}
		log.Infof("user %s challenged with %s at contact %s", userID.String(), otpCode, contact)
	} else if notifType == notifications.SMS {
		if err := tf.smsQueue.add(userID, electionID, contact, otpCode); err != nil {
			log.Errorf("cannot enqueue challenge: %v", err)
			return AuthResponse{Error: "problem with SMS challenge system"}
		}
		log.Infof("user %s challenged with %s at contact %s", userID.String(), otpCode, contact)
	} else {
		return AuthResponse{Error: "invalid notification type"}
	}

	// Build success reply
	// phoneStr := strconv.FormatUint(phone.GetNationalNumber(), 10)
	// if len(phoneStr) < 3 {
	// 	return AuthResponse{Error: "error parsing the phone number"}
	// }
	return AuthResponse{
		Success:   true,
		AuthToken: &atoken,
		Response:  []string{contact[len(contact)-2:]},
	}
}

func (tf *Twofactor) Auth(electionID []byte, authToken *uuid.UUID, authData []string) AuthResponse {
	if authToken == nil || len(authData) != 1 {
		return AuthResponse{Error: "auth token not provided or missing auth data"}
	}
	solution := authData[0]
	// Verify the challenge solution
	if err := tf.stg.VerifyChallenge(electionID, authToken, solution); err != nil {
		log.Warnf("verify challenge %s failed: %v", solution, err)
		return AuthResponse{Error: "challenge not completed:" + err.Error()}
	}
	token := tf.NewRequestKey()
	// token, err := tf.stg.NewBlindRequestKey(token)
	// if err != nil {
	// 	return AuthResponse{Error: "error getting new token:" + err.Error()}
	// }
	// tokenR := r.BytesUncompressed()

	log.Infof("new user registered, challenge resolved %s", authData[0])
	return AuthResponse{
		Response: []string{"challenge resolved"},
		Success:  true,
		TokenR:   token,
	}
}

func (tf *Twofactor) Sign(token, msg, electionID internal.HexBytes, sigType string) AuthResponse {
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
		signature, err := tf.SignECDSA(token, caBundleBytes, nil)
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
	default:
		return AuthResponse{
			Success: false,
			Error:   "invalid signature type",
		}
	}
}
