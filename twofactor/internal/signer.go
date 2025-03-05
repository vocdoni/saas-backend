package internal

import (
	"crypto/ecdsa"
	"errors"
	"math/big"

	blind "github.com/arnaucube/go-blindsecp256k1"
)

// Signer defines the interface for cryptographic signing operations
type Signer interface {
	// SignECDSA signs a message using ECDSA with an optional salt
	SignECDSA(salt []byte, message []byte) ([]byte, error)

	// SignBlind signs a blinded message using an optional salt
	SignBlind(salt []byte, blindedMessage []byte, secretK *big.Int) ([]byte, error)

	// GetBlindPublicKey returns the public key for blind signatures
	GetBlindPublicKey() *blind.PublicKey

	// GetECDSAPublicKey returns the public key for ECDSA signatures
	GetECDSAPublicKey() (*ecdsa.PublicKey, error)

	// GenerateSharedKey generates a shared key for a process
	GenerateSharedKey(processID []byte) ([]byte, error)

	// StoreKey stores a key for later use
	StoreKey(index string, key *big.Int) error

	// RetrieveKey retrieves a stored key
	RetrieveKey(index string) (*big.Int, error)

	// DeleteKey deletes a stored key
	DeleteKey(index string) error

	// GetBlindPoint parses a blind point from bytes
	GetBlindPoint(data []byte) (*blind.Point, error)

	// GetSecretKey retrieves a secret key for a token
	GetSecretKey(token string) (*big.Int, error)
}

// SignerConfig contains configuration for the signer
type SignerConfig struct {
	// PrivateKey is the private key for signing
	PrivateKey string

	// KeysDir is the directory where keys are stored
	KeysDir string
}

// SaltSize is the size of the salt used for deriving new keys
const SaltSize = 20

// SaltBlindPublicKey applies a salt to a blind public key
func SaltBlindPublicKey(pubKey *blind.PublicKey, salt []byte) (*blind.PublicKey, error) {
	if pubKey == nil {
		return nil, ErrInvalidKey
	}
	if len(salt) < SaltSize {
		return nil, ErrInvalidSalt
	}

	// Create a private key from the salt
	privKey := blind.PrivateKey(*new(big.Int).SetBytes(salt[:SaltSize]))

	// Get the public key (point) from the private key
	saltPoint := privKey.Public().Point()

	// Add the salt point to the public key
	return (*blind.PublicKey)(pubKey.Point().Add(saltPoint)), nil
}

// SaltECDSAPublicKey applies a salt to an ECDSA public key
func SaltECDSAPublicKey(pubKey *ecdsa.PublicKey, salt []byte) (*ecdsa.PublicKey, error) {
	if pubKey == nil {
		return nil, ErrInvalidKey
	}
	if len(salt) < SaltSize {
		return nil, ErrInvalidSalt
	}

	// Create a new point from the salt
	x, y := pubKey.Curve.ScalarBaseMult(salt[:SaltSize])

	// Add the salt point to the public key
	resultX, resultY := pubKey.Curve.Add(pubKey.X, pubKey.Y, x, y)

	// Create a new public key with the result
	result := &ecdsa.PublicKey{
		Curve: pubKey.Curve,
		X:     resultX,
		Y:     resultY,
	}

	return result, nil
}

// Common errors for the signer
var (
	ErrInvalidKey  = errors.New("invalid key")
	ErrInvalidSalt = errors.New("invalid salt")
)
