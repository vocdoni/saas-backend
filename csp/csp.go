package csp

import (
	"context"
	"crypto/ecdsa"
	"fmt"
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

// MaxChallengeAttempts is the maximum number of challenge-code verification
// attempts allowed for a single authentication token before it is rejected.
const MaxChallengeAttempts = 5

// MaxChallengeResends is the number of resend requests one challenge code can take, across every
// authentication token answering it. Resends are also spaced by the notification cooldown, so
// this bounds the emails/SMS a member can be sent for one code.
const MaxChallengeResends = 3

// MaxChallengeTokens is the number of extra authentication tokens repeated auth requests can be
// issued for one pending challenge code. Past it, a request gets a new code instead, subject to
// the notification cooldown and the daily send cap, so the tokens sharing a code stay bounded.
const MaxChallengeTokens = 10

// DefaultMaxDailyChallengeSends is the default cap on the OTP challenges (new tokens and resends
// together) one member can be sent for one voting process per UTC day. The cooldown only spaces
// sends out; this bounds their total, so a voter's contact cannot be flooded all day at the
// platform's expense. Stored in Mongo, so it survives restarts.
const DefaultMaxDailyChallengeSends = 10

// DefaultMaxHourlyAuthFailures is the default cap on failed step-0 authentications for one
// submission (the submitted login and contact fields) on one voting process per UTC hour. Once
// reached, that submission is refused on that process until the next hour. The claimed voter (the
// login fields alone) gets VoterAuthFailuresFactor times this budget across every contact tried,
// which bounds guessing one voter's contact (to 2400 guesses a day at the default) without letting
// whoever knows a voter's login fields lock them out cheaply: it takes that many failures in the
// hour, and the voter's own submission with the right contact keeps its separate budget until then.
// There is deliberately no process-wide cap: anyone could spend it with junk requests and lock
// every voter out of the election.
const DefaultMaxHourlyAuthFailures = 10

// VoterAuthFailuresFactor multiplies MaxHourlyAuthFailures into the hourly failure budget of one
// claimed voter across all the contacts submitted with their login fields.
const VoterAuthFailuresFactor = 10

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
	// MaxDailyChallengeSends caps the challenges (new tokens + resends) one member can be sent for
	// one voting process per UTC day. Zero uses DefaultMaxDailyChallengeSends; a negative value
	// disables the cap.
	MaxDailyChallengeSends int
	// MaxHourlyAuthFailures caps the failed step-0 authentications of one submission (login plus
	// contact fields) on one voting process per UTC hour; one claimed voter gets
	// VoterAuthFailuresFactor times it. Zero uses DefaultMaxHourlyAuthFailures; a negative value
	// disables the cap.
	MaxHourlyAuthFailures int
	// NotificationTTL is how long a CSP OTP challenge remains valid. After
	// this window ResendChallenge returns ErrTokenExpired and queued but
	// undelivered notifications are dropped. Distinct from
	// NotificationCoolDownTime (the anti-spam minimum interval between sends
	// of a challenge code). Zero uses notifications.DefaultOTPExpiry.
	NotificationTTL time.Duration
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
	// SyncDelivery makes AuthToken and ResendChallenge block until the
	// challenge notification has actually been delivered, instead of returning as
	// soon as it is enqueued. It exists to make tests deterministic; leave false
	// in production (fire-and-forget).
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
	notificationTTL          time.Duration
	maxDailyChallengeSends   int
	maxHourlyAuthFailures    int
	notifySync               bool
}

// countSentNotification bumps the organization's sent counters for a challenge
// the queue reports. Only delivered challenges count: the counters gate
// publishing, so given-up ones (expired, permanently failed, no service
// configured) must not use up the quota.
func countSentNotification(storage *db.MongoStorage, ch *notifications.NotificationChallenge) {
	if !ch.Success {
		return
	}
	switch ch.Type {
	case notifications.EmailChallenge:
		if err := storage.IncrementOrganizationSentEmailsCounter(ch.OrgAddress); err != nil {
			log.Errorf("failed to increment org %s email counter: %v", ch.OrgAddress, err)
		}
	case notifications.SMSChallenge:
		if err := storage.IncrementOrganizationSentSMSCounter(ch.OrgAddress); err != nil {
			log.Errorf("failed to increment org %s sms counter: %v", ch.OrgAddress, err)
		}
	default:
		log.Warnf("can't count notification of unknown type %s", ch.Type)
	}
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
					"anchorID", ch.AnchorID)
				countSentNotification(config.DB, ch)
			}
		}
	}()
	go queue.Start()
	notificationCoolDownTime := config.NotificationCoolDownTime
	if notificationCoolDownTime <= 0 {
		notificationCoolDownTime = saasNotifications.DefaultOTPCooldown
	}
	notificationTTL := config.NotificationTTL
	if notificationTTL <= 0 {
		notificationTTL = saasNotifications.DefaultOTPExpiry
	}
	maxDailyChallengeSends := config.MaxDailyChallengeSends
	if maxDailyChallengeSends == 0 {
		maxDailyChallengeSends = DefaultMaxDailyChallengeSends
	}
	maxHourlyAuthFailures := config.MaxHourlyAuthFailures
	if maxHourlyAuthFailures == 0 {
		maxHourlyAuthFailures = DefaultMaxHourlyAuthFailures
	}
	return &CSP{
		Storage:                  config.DB,
		Signer:                   s,
		notifyQueue:              queue,
		ctx:                      ctx,
		notificationCoolDownTime: notificationCoolDownTime,
		notificationTTL:          notificationTTL,
		maxDailyChallengeSends:   maxDailyChallengeSends,
		maxHourlyAuthFailures:    maxHourlyAuthFailures,
		notifySync:               config.SyncDelivery,
	}, nil
}

// pushChallenge enqueues a notification challenge. In synchronous-delivery mode
// (notifySync, used by tests) it blocks until the challenge has been delivered
// or given up, so callers observe a deterministic happens-before instead of
// racing the queue worker; otherwise it is fire-and-forget. The wait is bounded
// by the service context and a hard timeout so a stuck provider cannot hang a
// request indefinitely.
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
		return fmt.Errorf("%w: %w", errChallengeStillQueued, ctx.Err())
	}
}

// errChallengeStillQueued reports a synchronous delivery that timed out with the challenge already
// in the queue: it may still be sent, so its send unit must stay spent.
var errChallengeStillQueued = fmt.Errorf("challenge queued but not confirmed delivered")

// PubKey method returns the root public key of the CSP.
func (c *CSP) PubKey() (internal.HexBytes, error) {
	pub, err := c.Signer.ECDSAPubKey()
	if err != nil {
		return nil, err
	}
	return ethcrypto.CompressPubkey(pub), nil
}

// BlindPubKey returns the CSP blind-signature root public key, serialized as a compressed
// secp256k1 point. It is the census root of a blind (OFF_CHAIN_CA_V2) census: the Vochain
// decompresses it and reads it back as a blind public key when verifying ECDSA_BLIND proofs.
func (c *CSP) BlindPubKey() (internal.HexBytes, error) {
	bp := c.Signer.BlindPubKey()
	if bp == nil || bp.X == nil || bp.Y == nil {
		return nil, fmt.Errorf("blind public key is not available")
	}
	return ethcrypto.CompressPubkey(&ecdsa.PublicKey{Curve: ethcrypto.S256(), X: bp.X, Y: bp.Y}), nil
}
