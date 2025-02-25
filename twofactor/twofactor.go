package twofactor

import (
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/nyaruka/phonenumbers"
	"github.com/vocdoni/saas-backend/notifications"
	"github.com/xlzd/gotp"
	"go.vocdoni.io/dvote/log"
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

type TwofactorConfig struct {
	NotificationServices []*notifications.NotificationService
	MaxAttempts          int
	CoolDownTime         time.Duration
	ThrottleTime         time.Duration
	MaxRetries           int
	PrivKey              string
}

type Twofactor struct {
	stg                  *JSONstorage
	notificationServices []*notifications.NotificationService
	maxAttempts          int
	coolDownTime         time.Duration
	throttleTime         time.Duration
	maxRetries           int
	queue                Queue
	otpSalt              string
	signer               *SaltedKey
}

// SendChallengeFunc is the function that sends the SMS challenge to a phone number.
type SendChallengeFunc func(phone *phonenumbers.PhoneNumber, challenge string) error

func (tf *Twofactor) New(conf *TwofactorConfig) (*Twofactor, error) {
	if conf == nil {
		return nil, nil
	}
	if len(conf.NotificationServices) == 0 {
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
	if tf.signer, err = NewSaltedKey(conf.PrivKey); err != nil {
		return nil, err
	}

	if err := tf.stg.Init(
		filepath.Join(".", "storage"),
		maxAttempts,
		coolDownTime,
	); err != nil {
		return nil, err
	}

	go tf.queue.run()
	go tf.queueController()

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

func (tf *Twofactor) queueController() {
	for {
		r := <-tf.queue.response
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

// // CSV file must follow the format:
// // userId, phone, extraInfo, electionID1, electionID2, ..., electionIDn
// func (tf *Twofactor) importCSVfile(filepath string) {
// 	log.Infof("importing CSV file %s", filepath)
// 	f, err := os.Open(filepath)
// 	if err != nil {
// 		log.Fatal(err)
// 	}
// 	defer func() {
// 		if err := f.Close(); err != nil {
// 			panic(err)
// 		}
// 	}()

// 	// read csv values using csv.Reader
// 	csvReader := csv.NewReader(f)
// 	data, err := csvReader.ReadAll()
// 	if err != nil {
// 		log.Fatal(err)
// 	}
// 	for i, line := range data {
// 		if len(line) < 4 {
// 			log.Warnf("wrong CSV entry (missing fields): %s", line)
// 			continue
// 		}
// 		userID := HexBytes{}
// 		if err := userID.FromString(line[0]); err != nil {
// 			log.Warnf("wrong data field at line %d", i)
// 			continue
// 		}

// 		electionIDs := []HexBytes{}
// 		for _, eid := range line[3:] {
// 			eidh := HexBytes{}
// 			if err := eidh.FromString(eid); err != nil {
// 				log.Warnf("wrong electionID at line %d", i)
// 				continue
// 			}
// 			electionIDs = append(electionIDs, eidh)
// 		}

// 		if err := tf.stg.AddUser(userID, electionIDs, line[1], line[2]); err != nil {
// 			log.Warnf("cannot add user from line %d", i)
// 		}
// 	}
// 	log.Debug(tf.stg.String())
// }

// // Info returns the handler options and information.
// func (tf *Twofactor) Info() *Message {
// 	return &Message{
// 		Title:    "SMS code handler",
// 		AuthType: "auth",
// 		SignType: []string{SignatureTypeBlind},
// 		AuthSteps: []*AuthField{
// 			{Title: "UserId", Type: "text"},
// 			{Title: "Code", Type: "int4"},
// 		},
// 	}
// }

// Indexer takes a unique user identifier and returns the list of processIDs where
// the user is elegible for participation. This is a helper function that might not
// be implemented (depends on the handler use case).
func (tf *Twofactor) Indexer(userID HexBytes) []Election {
	user, err := tf.stg.User(userID)
	if err != nil {
		log.Warnf("cannot get indexer elections: %v", err)
		return nil
	}
	// Get the last two digits of the phone and return them as extraData
	phoneStr := ""
	if user.Phone != nil {
		phoneStr = strconv.FormatUint(user.Phone.GetNationalNumber(), 10)
		if len(phoneStr) < 3 {
			phoneStr = ""
		} else {
			phoneStr = phoneStr[len(phoneStr)-2:]
		}
	}
	indexerElections := []Election{}
	for _, e := range user.Elections {
		ie := Election{
			RemainingAttempts: e.RemainingAttempts,
			Consumed:          e.Consumed,
			ElectionID:        e.ElectionID,
			ExtraData:         []string{phoneStr},
		}
		indexerElections = append(indexerElections, ie)
	}
	return indexerElections
}

func (tf *Twofactor) InitiateAuth(electionID []byte, userId string) AuthResponse {
	// If first step, build new challenge
	if len(userId) == 0 {
		return AuthResponse{Error: "incorrect auth data fields"}
	}
	var userID HexBytes
	if err := userID.FromString(userId); err != nil {
		return AuthResponse{Error: "incorrect format for userId"}
	}

	// Generate challenge and authentication token
	challengeSecret := userID.String() + tf.otpSalt
	atoken := uuid.New()

	// Get the phone number. This methods checks for electionID and user verification status.
	phone, attemptNo, err := tf.stg.NewAttempt(userID, electionID, challengeSecret, &atoken)
	if err != nil {
		log.Warnf("new attempt for user %s failed: %v", userID, err)
		return AuthResponse{Error: err.Error()}
	}
	if phone == nil {
		log.Warnf("phone is nil for user %s", userID)
		return AuthResponse{Error: "no phone for this user data"}
	}
	// Enqueue to send the SMS challenge
	challenge := gotp.NewDefaultHOTP(challengeSecret).At(attemptNo)
	if err := tf.queue.add(userID, electionID, phone, challenge); err != nil {
		log.Errorf("cannot enqueue challenge: %v", err)
		return AuthResponse{Error: "problem with SMS challenge system"}
	}
	log.Infof("user %s challenged with %s at contact %d", userID.String(), challenge, phone.GetNationalNumber())

	// Build success reply
	phoneStr := strconv.FormatUint(phone.GetNationalNumber(), 10)
	if len(phoneStr) < 3 {
		return AuthResponse{Error: "error parsing the phone number"}
	}
	return AuthResponse{
		Success:   true,
		AuthToken: &atoken,
		Response:  []string{phoneStr[len(phoneStr)-2:]},
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
		return AuthResponse{Error: "challenge not completed"}
	}

	log.Infof("new user registered, challenge resolved %s", authData[0])
	return AuthResponse{
		Response: []string{"challenge resolved"},
		Success:  true,
		TokenR:   tf.NewRequestKey(),
	}
}

func (tf *Twofactor) Sign(electionID, payload, tokenR HexBytes, address string) AuthResponse {
	signature, err := tf.SignECDSA(tokenR, payload, electionID)
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
}
