package internal

import (
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/notifications"
)

// Authenticator defines the interface for two-factor authentication
type Authenticator interface {
	// Initialize initializes the authenticator
	Initialize(config *Config) error

	// AddProcess adds a process or process bundle to the authenticator
	AddProcess(censusType db.CensusType, participants []db.CensusMembershipParticipant) error

	// StartAuthentication initiates the authentication process
	StartAuthentication(
		bundleID, participantID, contact string,
		notificationType notifications.NotificationType,
	) (*AuthResponse, error)

	// VerifyChallenge verifies a challenge response
	VerifyChallenge(
		electionID ElectionID,
		token AuthToken,
		solution string,
	) (*AuthResponse, error)

	// Sign signs a message using the specified signature type
	Sign(
		token AuthToken,
		message []byte,
		electionID ElectionID,
		bundleID string,
		sigType SignatureType,
	) (*AuthResponse, error)

	// GetPublicKey returns the public key for the specified signature type
	GetPublicKey(processID []byte, sigType SignatureType) (string, error)

	// GenerateSharedKey generates a shared key for a process
	GenerateSharedKey(processID []byte) ([]byte, error)

	// GetUser retrieves a user by ID
	GetUser(userID UserID) (*User, error)
}

// Config contains configuration for the authenticator
type Config struct {
	// Storage is the storage configuration
	Storage *StorageConfig

	// Notification is the notification configuration
	Notification *NotificationConfig

	// Signer is the signer configuration
	Signer *SignerConfig

	// NotificationServices contains the notification services
	NotificationServices struct {
		SMS  notifications.NotificationService
		Mail notifications.NotificationService
	}
}

// NewConfig creates a new configuration with default values
func NewConfig() *Config {
	return &Config{
		Storage:      NewStorageConfig(),
		Notification: NewNotificationConfig(),
		Signer:       &SignerConfig{},
	}
}
