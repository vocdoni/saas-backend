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
	saasNotifications "github.com/vocdoni/saas-backend/notifications" //revive:disable:import-alias-naming

	"go.mongodb.org/mongo-driver/mongo"
	"go.vocdoni.io/dvote/log"
)

// Config struct contains the configuration for the CSP service. It includes
// the database name, the MongoDB client, the notification cooldown time, the
// notification throttle time, the maximum notification attempts, the SMS
// service and the mail service.
type Config struct {
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
func New(ctx context.Context, config *Config) (*CSP, error) {
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

// PubKey method returns the root public key of the CSP.
func (c *CSP) PubKey() (internal.HexBytes, error) {
	pub, err := c.Signer.ECDSAPubKey()
	if err != nil {
		return nil, err
	}
	return ethcrypto.CompressPubkey(pub), nil
}
