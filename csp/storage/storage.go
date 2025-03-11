package storage

import (
	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/internal"
)

// Storage interface implements the storage layer for the smshandler
type Storage interface {
	Init(config any) error
	// Reset clears the storage content
	Reset() error
	// User returns the full information of a user, including the election list
	User(uID internal.HexBytes) (*UserData, error)
	UserByToken(token *uuid.UUID) (*UserData, error)
	// SetUser adds a new user to the storage
	SetUser(user UserData) error
	// SetUserProcesses sets the list of processes for a user
	SetUserProcesses(uID internal.HexBytes, attempts int, pIDs ...internal.HexBytes) error
	// SetUserBundle sets the list of processes for a process bundle for a user
	SetUserBundle(uID internal.HexBytes, bID internal.HexBytes, attempts int, pIDs ...internal.HexBytes) error
	// SetProcessAttempts sets the number of attempts for a process
	SetProcessAttempts(uID internal.HexBytes, pID internal.HexBytes, attempts int) error
	// SetBundleProcessAttempts sets the number of attempts for a process in a bundle
	SetBundleProcessAttempts(uID internal.HexBytes, bID internal.HexBytes, pID internal.HexBytes, attempts int) error
	// AddUsers adds multiple users to the storage in a single operation
	AddUsers(users []UserData) error
	// SetUserToken sets the token for a user
	IndexToken(uID internal.HexBytes, token *uuid.UUID) error
}
