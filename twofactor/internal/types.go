package internal

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Common errors for the twofactor package
var (
	ErrTooManyAttempts          = errors.New("too many authentication attempts")
	ErrUserNotFound             = errors.New("user not found")
	ErrUserAlreadyVerified      = errors.New("user already verified")
	ErrUserNotInElection        = errors.New("user not eligible for election")
	ErrInvalidToken             = errors.New("invalid authentication token")
	ErrChallengeFailed          = errors.New("challenge verification failed")
	ErrCooldownPeriodNotElapsed = errors.New("cooldown period not elapsed")
)

// UserID represents a unique identifier for a user
type UserID []byte

// String returns the hexadecimal string representation of the UserID
func (id UserID) String() string {
	return fmt.Sprintf("%x", []byte(id))
}

// ElectionID represents a unique identifier for an election
type ElectionID []byte

// String returns the hexadecimal string representation of the ElectionID
func (id ElectionID) String() string {
	return fmt.Sprintf("%x", []byte(id))
}

// FromString initializes an ElectionID from a hexadecimal string
func (id *ElectionID) FromString(s string) error {
	b, err := hex.DecodeString(s)
	if err != nil {
		return fmt.Errorf("invalid hex string: %w", err)
	}
	*id = b
	return nil
}

// AuthToken represents an authentication token
type AuthToken uuid.UUID

// String returns the string representation of the AuthToken
func (t AuthToken) String() string {
	return uuid.UUID(t).String()
}

// NewAuthToken creates a new random authentication token
func NewAuthToken() AuthToken {
	return AuthToken(uuid.New())
}

// FromUUID creates an AuthToken from a uuid.UUID
func FromUUID(id uuid.UUID) AuthToken {
	return AuthToken(id)
}

// ToUUID converts an AuthToken to a uuid.UUID
func (t AuthToken) ToUUID() uuid.UUID {
	return uuid.UUID(t)
}

// ContactType represents the type of contact information
type ContactType int

const (
	// ContactTypeEmail represents an email contact
	ContactTypeEmail ContactType = iota
	// ContactTypeSMS represents an SMS contact
	ContactTypeSMS
)

// Contact represents contact information for a user
type Contact struct {
	Type  ContactType
	Value string
}

// VerificationStatus represents the status of a verification
type VerificationStatus int

const (
	// StatusPending indicates verification is pending
	StatusPending VerificationStatus = iota
	// StatusVerified indicates verification is complete
	StatusVerified
	// StatusFailed indicates verification has failed
	StatusFailed
)

// Challenge represents an authentication challenge
type Challenge struct {
	Token     AuthToken
	Secret    string
	Attempts  int
	ExpiresAt time.Time
}

// SignatureType represents the type of signature
type SignatureType string

const (
	// SignatureTypeBlind is a secp256k1 blind signature
	SignatureTypeBlind SignatureType = "blind"
	// SignatureTypeECDSA is the standard secp256k1 signature used in Ethereum
	SignatureTypeECDSA SignatureType = "ecdsa"
	// SignatureTypeSharedKey identifies the shared key (common for all users on the same processId)
	SignatureTypeSharedKey SignatureType = "sharedkey"
)

// AllSignatureTypes contains all available signature types
var AllSignatureTypes = []SignatureType{
	SignatureTypeBlind,
	SignatureTypeECDSA,
	SignatureTypeSharedKey,
}

// User represents a user in the system
type User struct {
	ID        UserID
	Elections map[string]Election
	Email     string
	Phone     string
	ExtraData string
}

// Election represents an election a user can participate in
type Election struct {
	ID                ElectionID
	RemainingAttempts int
	LastAttempt       *time.Time
	Verified          bool
	AuthToken         *AuthToken
	ChallengeSecret   string
	VotedWith         []byte
}

// AuthRequest represents an authentication request
type AuthRequest struct {
	UserID     UserID
	ElectionID ElectionID
	Contact    Contact
}

// AuthResponse represents the response to an authentication request
type AuthResponse struct {
	Success   bool
	Message   []string
	Token     *AuthToken
	Signature []byte
	Error     string
}

// BuildUserID creates a UserID from a participant ID and an election or bundle ID
func BuildUserID(participantID string, electionOrBundleID []byte) UserID {
	// Use SHA-256 hash as in the original code
	hash := sha256.Sum256(append([]byte(participantID), electionOrBundleID...))
	return UserID(hash[:])
}
