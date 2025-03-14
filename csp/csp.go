package csp

import (
	"context"
	"sync"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/csp/signers/saltedkey"
	"github.com/vocdoni/saas-backend/csp/storage"
	"github.com/vocdoni/saas-backend/internal"
	saasNotifications "github.com/vocdoni/saas-backend/notifications"

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
	// signer stuff
	PasswordSalt string
	RootKey      internal.HexBytes
	// notification stuff
	NotificationCoolDownTime time.Duration
	NotificationThrottleTime time.Duration
	SMSService               saasNotifications.NotificationService
	MailService              saasNotifications.NotificationService
}

// CSP struct contains the CSP service. It includes the storage, the
// notification queue, the maximum notification attempts, the notification
// throttle time and the notification cooldown time.
type CSP struct {
	PasswordSalt string
	Signer       *saltedkey.SaltedKey
	Storage      storage.Storage
	signerLock   sync.Map
	notifyQueue  *notifications.Queue

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
	s, err := saltedkey.NewSaltedKey(config.RootKey.String())
	if err != nil {
		return nil, err
	}
	stg := new(storage.MongoStorage)
	if err := stg.Init(&storage.MongoConfig{
		DBName: config.DBName,
		Client: config.MongoClient,
	}); err != nil {
		return nil, err
	}
	queue := notifications.NewQueue(
		ctx,
		config.NotificationCoolDownTime,
		config.NotificationThrottleTime,
		config.MailService,
		config.SMSService,
	)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ch := <-queue.NotificationsSent:
				log.Debugw("notification pop from queue",
					"success", ch.Success,
					"type", ch.Type,
					"userID", ch.UserID,
					"bundleID", ch.BundleID)
			}
		}
	}()
	go queue.Start()
	return &CSP{
		Storage:                  stg,
		Signer:                   s,
		notifyQueue:              queue,
		notificationThrottleTime: config.NotificationThrottleTime,
		notificationCoolDownTime: config.NotificationCoolDownTime,
	}, nil
}

// NewUserForBundle method creates a new user data for a bundle. It requires a
// user ID, a phone or email, a bundle ID and a list the bundle processes IDs.
// It returns the user data created or an error if the user ID is not provided,
// if the phone or email is not provided, if the bundle ID is not provided, if
// the process ID is not provided or if there is no process ID.
func NewUserForBundle(uID internal.HexBytes, bID internal.HexBytes, eIDs ...internal.HexBytes) (
	*storage.UserData, error,
) {
	if len(uID) == 0 {
		return nil, ErrNoUserID
	}
	if len(eIDs) == 0 {
		return nil, ErrNoProcessID
	}
	user := &storage.UserData{
		ID:      uID,
		Bundles: make(map[string]storage.BundleData),
	}
	user.Bundles[bID.String()] = storage.BundleData{
		ID:        bID,
		Processes: make(map[string]storage.ProcessData),
	}
	for _, eID := range eIDs {
		user.Bundles[bID.String()].Processes[eID.String()] = storage.ProcessData{ID: eID}
	}
	return user, nil
}

// SetUsers method registers the users to the storage. It calls the storage
// SetUsers method with the list of users provided. The users should be
// created if they do not exist in the storage, or updated if they already
// exist. It returns an error if the storage fails to set the users.
func (c *CSP) SetUsers(users []*storage.UserData) error {
	return c.Storage.SetUsers(users)
}

// PubKey method returns the root public key of the CSP.
func (c *CSP) PubKey() (internal.HexBytes, error) {
	pub, err := c.Signer.ECDSAPubKey()
	if err != nil {
		return nil, err
	}
	return ethcrypto.CompressPubkey(pub), nil
}
