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
	// SignerTypeECDSASalted represents a legacy salted ECDSA signature type. The salt is
	// processID[:20], shared by every election of the same organization on the same chain.
	// Used only for elections already published under census origin OFF_CHAIN_CA.
	SignerTypeECDSASalted SignerType = "ecdsa-salted"
	// SignerTypeECDSASaltedV2 represents the V2 salted ECDSA signature type. The salt is
	// keccak256(processID || voteWeight-as-32-big-endian)[:20], binding the salt to the full
	// election id and the CSP-authorized weight. Used for elections published under OFF_CHAIN_CA_V2.
	SignerTypeECDSASaltedV2 SignerType = "ecdsa-salted-v2"
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
