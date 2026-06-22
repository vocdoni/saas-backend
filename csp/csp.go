package csp

import (
	"context"
	"sync"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/csp/signers/saltedkey"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	saasNotifications "github.com/vocdoni/saas-backend/notifications" //revive:disable:import-alias-naming

	"go.vocdoni.io/dvote/log"
)

const (
	DefaultNotificationCoolDownTime = time.Second * 60
	DefaultOTPExpiry                = time.Minute * 15
)

// MaxChallengeAttempts is the maximum number of failed challenge-code
// verification attempts allowed for a single authentication token before it is
// rejected.
const MaxChallengeAttempts = 5

// Config struct contains the configuration for the CSP service. It includes
// the database name, the MongoDB client, the notification cooldown time, the
// notification queue settings, the SMS service and the mail service.
type Config struct {
	// db stuff
	DB *db.MongoStorage
	// signer stuff
	PasswordSalt string
	RootKey      internal.HexBytes
	// notification stuff
	NotificationCoolDownTime time.Duration
	// NotificationThrottleTime is deprecated and ignored. The notification queue
	// is now drained by a concurrent worker pool guarded by per-provider circuit
	// breakers instead of a single throttled loop. Kept for backwards
	// compatibility with existing configurations.
	NotificationThrottleTime time.Duration
	// OTPExpiry is how long a challenge OTP remains valid for verification.
	// Resends within this window repeat the same code. Zero uses DefaultOTPExpiry (15 min).
	OTPExpiry time.Duration
	// NotificationQueueWorkers is the number of concurrent notification senders.
	// It bounds the maximum number of in-flight provider sends. Zero uses the
	// default (notifications.DefaultQueueWorkers).
	NotificationQueueWorkers int
	// NotificationQueueTTL is the maximum age of a queued challenge before it is
	// dropped. Zero uses the default (notifications.DefaultQueueTTL).
	NotificationQueueTTL time.Duration
	// NotificationBreakerMaxFailures and NotificationBreakerCooldown configure
	// the per-provider circuit breakers. Zero uses the defaults.
	NotificationBreakerMaxFailures int
	NotificationBreakerCooldown    time.Duration
	SMSService                     saasNotifications.NotificationService
	MailService                    saasNotifications.NotificationService
	// SyncDelivery makes the challenge-enqueuing path (BundleAuthToken and
	// ResendChallenge) block until the challenge has been delivered or given up,
	// so callers observe a deterministic happens-before instead of racing the
	// concurrent queue workers. It exists to make tests deterministic; leave it
	// false in production, where notifications are delivered asynchronously.
	SyncDelivery bool
}

// CSP struct contains the CSP service. It includes the storage, the
// notification queue and the per-user notification cooldown time.
type CSP struct {
	PasswordSalt string
	Signer       *saltedkey.SaltedKey
	Storage      *db.MongoStorage
	signerLock   sync.Map
	notifyQueue  *notifications.Queue
	ctx          context.Context

	notificationCoolDownTime time.Duration
	otpExpiry                time.Duration
	// notifySync, when true, makes pushChallenge block until the challenge has
	// been delivered or given up. Used by tests for deterministic delivery.
	notifySync bool
}

// New method creates a new CSP service. It requires a CSPConfig struct with
// the configuration for the service. It returns the CSP service or an error
// if the storage fails to initialize. It initializes the storage with the
// MongoDB client and the database name provided in the configuration, and
// creates a new notification queue (a concurrent worker pool guarded by
// per-provider circuit breakers) with the SMS and mail services.
func New(ctx context.Context, config *Config) (*CSP, error) {
	s, err := saltedkey.NewSaltedKey(config.RootKey.String())
	if err != nil {
		return nil, err
	}
	queue := notifications.NewQueue(ctx, notifications.QueueConfig{
		TTL:                config.NotificationQueueTTL,
		Workers:            config.NotificationQueueWorkers,
		MailService:        config.MailService,
		SMSService:         config.SMSService,
		BreakerMaxFailures: config.NotificationBreakerMaxFailures,
		BreakerCooldown:    config.NotificationBreakerCooldown,
	})
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
				switch ch.Type {
				case notifications.EmailChallenge:
					if err := config.DB.IncrementOrganizationSentEmailsCounter(ch.OrgAddress); err != nil {
						log.Errorf("failed to increment org %s email counter: %v", ch.OrgAddress, err)
					}
				case notifications.SMSChallenge:
					if err := config.DB.IncrementOrganizationSentSMSCounter(ch.OrgAddress); err != nil {
						log.Errorf("failed to increment org %s sms counter: %v", ch.OrgAddress, err)
					}
				default:
					log.Warnf("can't count notification of unknown type %s", ch.Type)
				}
			}
		}
	}()
	go queue.Start()
	notificationCoolDownTime := config.NotificationCoolDownTime
	if notificationCoolDownTime <= 0 {
		notificationCoolDownTime = DefaultNotificationCoolDownTime
	}
	otpExpiry := config.OTPExpiry
	if otpExpiry <= 0 {
		otpExpiry = DefaultOTPExpiry
	}
	return &CSP{
		Storage:                  config.DB,
		Signer:                   s,
		notifyQueue:              queue,
		ctx:                      ctx,
		notificationCoolDownTime: notificationCoolDownTime,
		otpExpiry:                otpExpiry,
		notifySync:               config.SyncDelivery,
	}, nil
}

// pushChallenge enqueues a notification challenge. In synchronous-delivery mode
// (notifySync, used by tests) it blocks until the challenge has been delivered
// or given up, so callers observe a deterministic happens-before instead of
// racing the concurrent queue workers; otherwise it is fire-and-forget. The
// wait is bounded by a hard timeout so a stuck or breaker-open provider cannot
// hang a request indefinitely.
func (c *CSP) pushChallenge(ch *notifications.NotificationChallenge) error {
	if !c.notifySync {
		return c.notifyQueue.Push(ch)
	}
	done, err := c.notifyQueue.PushWait(ch)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	defer cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// PubKey method returns the root public key of the CSP.
func (c *CSP) PubKey() (internal.HexBytes, error) {
	pub, err := c.Signer.ECDSAPubKey()
	if err != nil {
		return nil, err
	}
	return ethcrypto.CompressPubkey(pub), nil
}
