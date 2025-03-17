package storage

import "github.com/vocdoni/saas-backend/internal"

// Storage interface implements the storage layer for the smshandler
type Storage interface {
	Init(config any) error
	// Reset clears the storage content
	Reset() error
	// LastCSPAuth returns the last CSPAuth for the user and bundle.
	LastCSPAuth(userID, bundleID internal.HexBytes) (*CSPAuth, error)
	// SetCSPAuth creates a new CSPAuth for the user and bundle.
	SetCSPAuth(token, userID, bundleID internal.HexBytes) error
	// CSPAuth returns the CSPAuth for the given token.
	CSPAuth(token internal.HexBytes) (*CSPAuth, error)
	// VerifyCSPAuth verifies the CSPAuth token.
	VerifyCSPAuth(token internal.HexBytes) error
	// CSPProcess returns the CSPProcess for the given token and processID.
	CSPProcess(token, processID internal.HexBytes) (*CSPProcess, error)
	// IsCSPProcessConsumed returns true if the process has been consumed by
	// the user or not.
	IsCSPProcessConsumed(userID, processID internal.HexBytes) (bool, error)
	// ConsumeCSPProcess consumes the process for the user.
	ConsumeCSPProcess(token, processID, address internal.HexBytes) error
}
