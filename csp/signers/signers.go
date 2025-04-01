// Package signers provides types and error definitions for cryptographic signers
// used in the CSP (Census Service Provider), including different signature types
// and error handling.
package signers

import (
	"fmt"
)

// SignerType represents the type of cryptographic signer to be used.
type SignerType string

const (
	// SignerTypeBlindSalted represents a salted blind signature type.
	SignerTypeBlindSalted SignerType = "blind-salted"
	// SignerTypeECDSASalted represents a salted ECDSA signature type using the secp256k1 curve.
	SignerTypeECDSASalted SignerType = "ecdsa-salted"
)

var (
	// ErrInvalidSignerType is returned when the signer type is not supported
	ErrInvalidSignerType = fmt.Errorf("invalid signer type")
	// ErrInvalidRootKey is returned when the root key provided is not valid
	// for the signer type
	ErrInvalidRootKey = fmt.Errorf("invalid root key")
	// ErrSignOperation is returned when the signer cannot sign the message
	ErrSignOperation = fmt.Errorf("cannot sign the message")
)
