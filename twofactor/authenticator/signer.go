package authenticator

import (
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"path"
	"sync"

	blind "github.com/arnaucube/go-blindsecp256k1"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	vocdonicrypto "go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/db"
	"go.vocdoni.io/dvote/db/metadb"
)

const (
	// PrivKeyHexSize is the hexadecimal length of a private key
	PrivKeyHexSize = 64
)

// DefaultSigner implements the Signer interface
type DefaultSigner struct {
	rootKey *big.Int
	keys    db.Database
	mutex   sync.RWMutex
}

// NewSigner creates a new DefaultSigner
func NewSigner(privateKey, keysDir string) (*DefaultSigner, error) {
	if len(privateKey) != PrivKeyHexSize {
		return nil, fmt.Errorf("private key size is incorrect %d", len(privateKey))
	}

	pkb, err := hex.DecodeString(privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to decode private key: %w", err)
	}

	// Check the privKey point is a valid D value
	_, err = ethcrypto.ToECDSA(pkb)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}

	// Create keys directory if it doesn't exist
	if err := os.MkdirAll(keysDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create keys directory: %w", err)
	}

	// Initialize database for storing keys
	keysDB, err := metadb.New(db.TypePebble, path.Join(keysDir, "keys"))
	if err != nil {
		return nil, fmt.Errorf("failed to initialize keys database: %w", err)
	}

	return &DefaultSigner{
		rootKey: new(big.Int).SetBytes(pkb),
		keys:    keysDB,
	}, nil
}

// SignECDSA signs a message using ECDSA with an optional salt
func (s *DefaultSigner) SignECDSA(salt []byte, msg []byte) ([]byte, error) {
	esk := new(vocdonicrypto.SignKeys)
	if err := esk.AddHexKey(fmt.Sprintf("%x", s.rootKey.Bytes())); err != nil {
		return nil, fmt.Errorf("cannot sign ECDSA salted: %w", err)
	}

	// Apply salt if provided
	if len(salt) > 0 {
		saltBigInt := new(big.Int).SetBytes(salt[:])
		// Add it to the current key, so now we have a new private key (currentPrivKey + n)
		esk.Private.D.Add(esk.Private.D, saltBigInt)
	}

	// Return the signature
	return esk.SignEthereum(msg)
}

// SignBlind signs a blinded message using an optional salt
func (s *DefaultSigner) SignBlind(salt []byte, msgBlinded []byte, secretK *big.Int) ([]byte, error) {
	if secretK == nil {
		return nil, fmt.Errorf("secretK is nil")
	}

	// Create a new private key by adding the salt to the root key
	privKey := new(big.Int).Set(s.rootKey)
	if len(salt) > 0 {
		saltBigInt := new(big.Int).SetBytes(salt[:])
		privKey.Add(privKey, saltBigInt)
	}

	// Convert to blind private key
	blindPrivKey := blind.PrivateKey(*privKey)

	// Convert message to big.Int
	m := new(big.Int).SetBytes(msgBlinded)

	// Sign the blinded message
	signature, err := blindPrivKey.BlindSign(m, secretK)
	if err != nil {
		return nil, fmt.Errorf("failed to sign blind message: %w", err)
	}

	return signature.Bytes(), nil
}

// GetBlindPublicKey returns the public key for blind signatures
func (s *DefaultSigner) GetBlindPublicKey() *blind.PublicKey {
	pk := blind.PrivateKey(*s.rootKey)
	return pk.Public()
}

// GetECDSAPublicKey returns the public key for ECDSA signatures
func (s *DefaultSigner) GetECDSAPublicKey() (*ecdsa.PublicKey, error) {
	privK, err := ethcrypto.ToECDSA(s.rootKey.Bytes())
	if err != nil {
		return nil, fmt.Errorf("failed to convert to ECDSA: %w", err)
	}
	return &privK.PublicKey, nil
}

// GenerateSharedKey generates a shared key for a process
func (s *DefaultSigner) GenerateSharedKey(processID []byte) ([]byte, error) {
	if len(processID) == 0 {
		return nil, fmt.Errorf("processID is nil or empty")
	}

	// Use the processID as salt
	return s.SignECDSA(processID, processID)
}

// StoreKey stores a key for later use
func (s *DefaultSigner) StoreKey(index string, key *big.Int) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	tx := s.keys.WriteTx()
	defer tx.Discard()

	if err := tx.Set([]byte(index), key.Bytes()); err != nil {
		return fmt.Errorf("failed to store key: %w", err)
	}

	return tx.Commit()
}

// RetrieveKey retrieves a stored key
func (s *DefaultSigner) RetrieveKey(index string) (*big.Int, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	keyBytes, err := s.keys.Get([]byte(index))
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve key: %w", err)
	}

	return new(big.Int).SetBytes(keyBytes), nil
}

// DeleteKey deletes a stored key
func (s *DefaultSigner) DeleteKey(index string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	tx := s.keys.WriteTx()
	defer tx.Discard()

	if err := tx.Delete([]byte(index)); err != nil {
		return fmt.Errorf("failed to delete key: %w", err)
	}

	return tx.Commit()
}

// GetBlindPoint parses a blind point from bytes
func (s *DefaultSigner) GetBlindPoint(data []byte) (*blind.Point, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}

	point, err := blind.NewPointFromBytesUncompressed(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse blind point: %w", err)
	}

	return point, nil
}

// GetSecretKey retrieves a secret key for a token
func (s *DefaultSigner) GetSecretKey(token string) (*big.Int, error) {
	return s.RetrieveKey(token)
}
