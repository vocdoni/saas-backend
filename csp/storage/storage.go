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
	// SetUser adds a new user to the storage
	SetUser(user UserData) error
	// SetUserBundle sets the list of processes for a process bundle for a user
	SetUserBundle(uID internal.HexBytes, bID internal.HexBytes, pIDs ...internal.HexBytes) error
	// AddUsers adds multiple users to the storage in a single operation
	AddUsers(users []UserData) error
	// IndexAuthToken sets the token for a user
	IndexAuthToken(uID, bID internal.HexBytes, token *uuid.UUID) error
	UserAuthToken(token *uuid.UUID) (*AuthToken, *UserData, error)
}
