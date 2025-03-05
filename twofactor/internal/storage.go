package internal

import (
	"time"
)

// Storage defines the interface for storing and retrieving user and authentication data
type Storage interface {
	// Initialize the storage with configuration parameters
	Initialize(dataDir string, maxAttempts int, cooldownPeriod time.Duration) error

	// Reset clears all data in the storage
	Reset() error

	// User operations
	AddUser(userID UserID, electionIDs []ElectionID, email, phone, extraData string) error
	BulkAddUser(users []*User) error
	GetUser(userID UserID) (*User, error)
	UpdateUser(user *User) error
	DeleteUser(userID UserID) error
	ListUsers() ([]UserID, error)
	SearchUsers(term string) ([]UserID, error)

	// Election operations
	IsUserInElection(userID UserID, electionID ElectionID) (bool, error)
	IsUserVerified(userID UserID, electionID ElectionID) (bool, error)
	UpdateAttempts(userID UserID, electionID ElectionID, delta int) error

	// Challenge operations
	CreateChallenge(userID UserID, electionID ElectionID, secret string, token AuthToken) (string, string, int, error)
	VerifyChallenge(electionID ElectionID, token AuthToken, solution string) error
	GetUserByToken(token AuthToken) (*User, error)

	// Import/Export operations
	ImportData(data []byte) error
	ExportData() ([]byte, error)
}

// StorageConfig contains configuration for storage implementations
type StorageConfig struct {
	// DataDir is the directory where data will be stored
	DataDir string

	// MaxAttempts is the maximum number of authentication attempts allowed
	MaxAttempts int

	// CooldownPeriod is the time to wait between authentication attempts
	CooldownPeriod time.Duration

	// MongoURI is the URI for MongoDB connection (if using MongoDB storage)
	MongoURI string
}

// NewStorageConfig creates a new storage configuration with default values
func NewStorageConfig() *StorageConfig {
	return &StorageConfig{
		MaxAttempts:    5,
		CooldownPeriod: 2 * time.Minute,
	}
}
