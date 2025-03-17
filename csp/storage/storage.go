package storage

import "github.com/vocdoni/saas-backend/internal"

// Storage interface implements the storage layer for the smshandler
type Storage interface {
	Init(config any) error
	// Reset clears the storage content
	Reset() error

	LastCSPAuthToken(userID, bundleID internal.HexBytes) (*CSPAuthToken, error)
	SetCSPAuthToken(token, userID, bundleID internal.HexBytes) error
	CSPAuthToken(token internal.HexBytes) (*CSPAuthToken, error)
	VerifyCSPAuthToken(token internal.HexBytes) error
	CSPAuthTokenStatus(token, processID internal.HexBytes) (*CSPAuthTokenStatus, error)
	ConsumeCSPAuthToken(token, processID, address internal.HexBytes) error
	IsPIDConsumedCSP(userID, processID internal.HexBytes) (bool, error)
}
