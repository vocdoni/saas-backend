// Package saltedkey provides functionality for creating and managing salted cryptographic keys
// for secure signing operations, supporting both ECDSA and blind signatures.
package saltedkey

import (
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"

	blind "github.com/arnaucube/go-blindsecp256k1"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	vocdonicrypto "go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/crypto/saltedkey"
)

const (
	// PrivKeyHexSize is the hexadecimal length of a private key
	PrivKeyHexSize = 64
	// SaltSize is the width (in bytes) of the salt the key-derivation primitives consume.
	// It mirrors dvote's crypto/saltedkey.SaltSize; only the first SaltSize bytes of a salt
	// are read, which is what lets the legacy derivation hand the primitives a full 32-byte
	// processID and have them salt on its first 20.
	SaltSize = saltedkey.SaltSize
)

// SaltedKey is a wrapper around ECDSA and ECDSA Blind that helps signing messages
// with a per-election salt. The salt is added to the root private key to derive a
// per-election deterministic signing key; the same salt is applied to the public
// key to verify. The salt value itself is chosen by the caller — see
// csp.electionSalt, which mirrors the chain's cspproof derivation (legacy or the
// fixed hashed one, per the CSP soft fork).
type SaltedKey struct {
	rootKey *big.Int
}

// NewSaltedKey returns an initialized instance of SaltedKey using the private key
// provided in hex format.
func NewSaltedKey(privKey string) (*SaltedKey, error) {
	if len(privKey) != PrivKeyHexSize {
		return nil, fmt.Errorf("private key size is incorrect %d", len(privKey))
	}
	pkb, err := hex.DecodeString(privKey)
	if err != nil {
		return nil, err
	}
	// Check the privKey point is a valid D value
	if _, err = ethcrypto.ToECDSA(pkb); err != nil {
		return nil, err
	}
	return &SaltedKey{
		rootKey: new(big.Int).SetBytes(pkb),
	}, nil
}

// SignECDSA returns the Ethereum signature of msg signed with the per-election
// salted private key. The salted key is (d + salt) mod n, derived by dvote's
// SaltECDSAPrivKey so it stays byte-for-byte in step with the chain's
// SaltECDSAPubKey verifier. The signature itself is exactly what
// ethereum.SignKeys.SignEthereum produces (crypto.Sign of the vocdoni message
// hash), so the wire format is unchanged.
func (sk *SaltedKey) SignECDSA(salt, msg []byte) ([]byte, error) {
	root, err := ethcrypto.ToECDSA(sk.rootKey.Bytes())
	if err != nil {
		return nil, fmt.Errorf("cannot load root key: %w", err)
	}
	salted, err := saltedkey.SaltECDSAPrivKey(root, salt)
	if err != nil {
		return nil, fmt.Errorf("cannot derive salted ECDSA key: %w", err)
	}
	return ethcrypto.Sign(vocdonicrypto.Hash(msg), salted)
}

// SignBlind returns the blind signature of a blinded message, signed with the
// per-election salted blind private key and the single-use secret k. The salted
// key is (d + salt) mod n, derived by dvote's SaltBlindPrivKey to match the
// chain's SaltBlindPubKey verifier.
func (sk *SaltedKey) SignBlind(salt, msgBlinded []byte, secretK *big.Int) ([]byte, error) {
	if secretK == nil {
		return nil, fmt.Errorf("secretK is nil")
	}
	root := blind.PrivateKey(*sk.rootKey)
	salted, err := saltedkey.SaltBlindPrivKey(&root, salt)
	if err != nil {
		return nil, fmt.Errorf("cannot derive salted blind key: %w", err)
	}
	m := new(big.Int).SetBytes(msgBlinded)
	signature, err := salted.BlindSign(m, secretK)
	if err != nil {
		return nil, err
	}
	return signature.Bytes(), nil
}

// BlindPubKey returns the root public key for blind signatures.
func (sk *SaltedKey) BlindPubKey() *blind.PublicKey {
	pk := blind.PrivateKey(*sk.rootKey)
	return pk.Public()
}

// ECDSAPubKey returns the root ecdsa public key for plain signatures.
func (sk *SaltedKey) ECDSAPubKey() (*ecdsa.PublicKey, error) {
	privK, err := ethcrypto.ToECDSA(sk.rootKey.Bytes())
	if err != nil {
		return nil, err
	}
	return &privK.PublicKey, nil
}

// Salt derives the per-election salt from the process id and vote weight,
// mirroring dvote's crypto/saltedkey.Salt: keccak256(processID || weight[32-BE]).
// A nil or empty weight is treated as 1; weights above 2^160 are rejected. This
// is the post-fork derivation (see csp.electionSalt); the legacy one hands the
// primitives the raw processID instead.
func Salt(processID, weight []byte) ([]byte, error) {
	return saltedkey.Salt(processID, weight)
}

// SaltBlindPubKey returns the salted blind public key of pubKey applying the salt.
func SaltBlindPubKey(pubKey *blind.PublicKey, salt []byte) (*blind.PublicKey, error) {
	if pubKey == nil {
		return nil, fmt.Errorf("public key is nil")
	}
	return saltedkey.SaltBlindPubKey(pubKey, salt)
}

// SaltECDSAPubKey returns the salted plain public key of pubKey applying the salt,
// as uncompressed bytes, for comparison against a recovered signer.
func SaltECDSAPubKey(pubKey *ecdsa.PublicKey, salt []byte) ([]byte, error) {
	if pubKey == nil {
		return nil, fmt.Errorf("public key is nil")
	}
	salted, err := saltedkey.SaltECDSAPubKey(pubKey, salt)
	if err != nil {
		return nil, err
	}
	return ethcrypto.FromECDSAPub(salted), nil
}
