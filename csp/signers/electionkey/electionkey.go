// Package electionkey derives the per-election CSP keys of OFF_CHAIN_CA_V3_ECDSA and
// OFF_CHAIN_CA_V3_BLIND elections from the CSP root key, and builds the weight certificates that
// bind a blind signing key to one election and one vote weight.
//
// Every key is a keyed hash of the root secret and a public seed (the Mongo ObjectID of the
// VotingProcessQuestion behind the election), so no two published keys have a publicly computable
// relation. Nothing is stored: the same root and seed always give the same keys.
//
//	hashToScalar(tag, data) = first keccak256(tag || u32be(i) || data), i = 0..255, in (0, n)
//	dE = hashToScalar("vocdoni/csp/v3/election-ecdsa", root || seed)   census root of an ECDSA election
//	dB = hashToScalar("vocdoni/csp/v3/election-blind", root || seed)   census root of a blind election
//	dX = hashToScalar("vocdoni/csp/v3/weight-key", u256be(dB) || u256be(w))   blind key for weight w
//
// dE signs CA bundles. dB only signs weight certificates. dX only signs blind messages. The tags
// are prefix-free (no tag is a prefix of another, including CertTag); a new tag must keep that.
package electionkey

import (
	"crypto/ecdsa"
	"encoding/binary"
	"fmt"
	"math/big"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	vocdonicrypto "go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/proto/build/go/models"
)

const (
	// TagElectionECDSA derives dE, the census root key of an OFF_CHAIN_CA_V3_ECDSA election.
	TagElectionECDSA = "vocdoni/csp/v3/election-ecdsa"
	// TagElectionBlind derives dB, the census root key of an OFF_CHAIN_CA_V3_BLIND election.
	TagElectionBlind = "vocdoni/csp/v3/election-blind"
	// TagWeightKey derives dX, the blind signing key of one vote weight of a blind election.
	TagWeightKey = "vocdoni/csp/v3/weight-key"
	// CertTag prefixes every weight certificate message. Its first byte (0x76) has protobuf wire
	// type 6, so a certificate message is never a valid CAbundle encoding.
	CertTag = "vocdoni/csp/v3/weight-cert"

	// SeedSize is the size of a derivation seed: a Mongo ObjectID.
	SeedSize = 12
	// RootSize is the size of the CSP root private key.
	RootSize = 32
	// ProcessIDSize is the size of an on-chain election id.
	ProcessIDSize = 32
	// WeightSize is the fixed width of a vote weight inside derivations and certificates.
	WeightSize = 32
	// PubKeySize is the size of a compressed secp256k1 public key.
	PubKeySize = 33
	// CertMessageSize is the size of a weight certificate message.
	CertMessageSize = len(CertTag) + ProcessIDSize + WeightSize + PubKeySize

	// maxCounter bounds hashToScalar. A keccak output falls outside (0, n) with probability
	// about 2^-128, so the bound is never reached in practice.
	maxCounter = 256
)

// The census origins of per-election CSP keys, as numbered by the CensusOrigin enum of
// go.vocdoni.io/proto (vochain.proto, 1.18.0). This module pins an older proto release that
// predates them, so they are declared here, together with their enum names, which is the form
// stored on each published question (db.VotingProcessQuestion.CensusOrigin).
const (
	// OriginECDSA is CensusOrigin OFF_CHAIN_CA_V3_ECDSA: proofs are ProofCA ECDSA, signed by dE.
	OriginECDSA models.CensusOrigin = 5
	// OriginBlind is CensusOrigin OFF_CHAIN_CA_V3_BLIND: proofs are ProofCA ECDSA_BLIND, signed
	// by dX and carrying a weight certificate signed by dB.
	OriginBlind models.CensusOrigin = 6
	// OriginECDSAName is the enum name of OriginECDSA.
	OriginECDSAName = "OFF_CHAIN_CA_V3_ECDSA"
	// OriginBlindName is the enum name of OriginBlind.
	OriginBlindName = "OFF_CHAIN_CA_V3_BLIND"
)

// IsV3Origin reports whether a stored census origin name is one of the per-election key origins.
func IsV3Origin(name string) bool {
	return name == OriginECDSAName || name == OriginBlindName
}

// OriginName returns the enum name of a per-election CSP census origin, or "" for any other
// origin. The protobuf release this module pins has no names for these origins.
func OriginName(origin models.CensusOrigin) string {
	switch origin {
	case OriginECDSA:
		return OriginECDSAName
	case OriginBlind:
		return OriginBlindName
	default:
		return ""
	}
}

// curveN is the order of the secp256k1 group.
var curveN = ethcrypto.S256().Params().N

// HashToScalar returns the first keccak256(tag || u32be(i) || data), for i = 0..255, that is a
// valid secp256k1 private scalar (0 < h < n).
func HashToScalar(tag string, data []byte) (*big.Int, error) {
	var counter [4]byte
	for i := range maxCounter {
		binary.BigEndian.PutUint32(counter[:], uint32(i))
		h := new(big.Int).SetBytes(ethcrypto.Keccak256([]byte(tag), counter[:], data))
		if h.Sign() > 0 && h.Cmp(curveN) < 0 {
			return h, nil
		}
	}
	return nil, fmt.Errorf("no valid scalar for tag %q after %d attempts", tag, maxCounter)
}

// Root is the CSP root secret the per-election keys derive from.
type Root struct {
	key [RootSize]byte
}

// NewRoot returns the Root for a 32-byte CSP private key.
func NewRoot(key []byte) (*Root, error) {
	if len(key) != RootSize {
		return nil, fmt.Errorf("root key is %d bytes, expected %d", len(key), RootSize)
	}
	d := new(big.Int).SetBytes(key)
	if d.Sign() == 0 || d.Cmp(curveN) >= 0 {
		return nil, fmt.Errorf("root key is not a valid secp256k1 scalar")
	}
	r := &Root{}
	copy(r.key[:], key)
	return r, nil
}

// ElectionECDSAKey returns dE, the key whose public key is the census root of the
// OFF_CHAIN_CA_V3_ECDSA election backed by seed, and which signs its CA bundles.
func (r *Root) ElectionECDSAKey(seed [SeedSize]byte) (*ecdsa.PrivateKey, error) {
	return r.electionKey(TagElectionECDSA, seed)
}

// ElectionBlindKey returns dB, the key whose public key is the census root of the
// OFF_CHAIN_CA_V3_BLIND election backed by seed, and which signs its weight certificates.
func (r *Root) ElectionBlindKey(seed [SeedSize]byte) (*ecdsa.PrivateKey, error) {
	return r.electionKey(TagElectionBlind, seed)
}

func (r *Root) electionKey(tag string, seed [SeedSize]byte) (*ecdsa.PrivateKey, error) {
	data := make([]byte, 0, RootSize+SeedSize)
	data = append(data, r.key[:]...)
	data = append(data, seed[:]...)
	d, err := HashToScalar(tag, data)
	if err != nil {
		return nil, err
	}
	return scalarToKey(d)
}

// WeightKey returns dX, the blind signing key of vote weight w in the blind election whose
// election key is electionBlindKey (dB).
func WeightKey(electionBlindKey *ecdsa.PrivateKey, w *big.Int) (*ecdsa.PrivateKey, error) {
	if electionBlindKey == nil {
		return nil, fmt.Errorf("nil election blind key")
	}
	weight, err := u256(w)
	if err != nil {
		return nil, err
	}
	data := make([]byte, 0, 2*WeightSize)
	data = append(data, ethcrypto.FromECDSA(electionBlindKey)...)
	data = append(data, weight...)
	d, err := HashToScalar(TagWeightKey, data)
	if err != nil {
		return nil, err
	}
	return scalarToKey(d)
}

// CertMessage returns the message a weight certificate signs:
// CertTag || processID (32 bytes) || u256be(w) || weightKey (33 bytes, compressed).
func CertMessage(processID []byte, w *big.Int, weightKey []byte) ([]byte, error) {
	if len(processID) != ProcessIDSize {
		return nil, fmt.Errorf("process id is %d bytes, expected %d", len(processID), ProcessIDSize)
	}
	if len(weightKey) != PubKeySize {
		return nil, fmt.Errorf("weight key is %d bytes, expected %d", len(weightKey), PubKeySize)
	}
	weight, err := u256(w)
	if err != nil {
		return nil, err
	}
	msg := make([]byte, 0, CertMessageSize)
	msg = append(msg, CertTag...)
	msg = append(msg, processID...)
	msg = append(msg, weight...)
	msg = append(msg, weightKey...)
	return msg, nil
}

// WeightCredential is the blind signing key of one vote weight of a blind election, together with
// its public weight certificate.
type WeightCredential struct {
	// Key is dX: it blind-signs the ballots of this weight and never leaves the CSP.
	Key *ecdsa.PrivateKey
	// WeightKey is the compressed public key of Key (33 bytes).
	WeightKey []byte
	// Certificate is the Ethereum personal-sign signature by dB of CertMessage(processID, w, WeightKey)
	// (65 bytes). It is deterministic (RFC 6979 nonces) and public.
	Certificate []byte
}

// IssueWeightCredential derives dX for weight w from electionBlindKey (dB) and certifies it for
// processID.
func IssueWeightCredential(electionBlindKey *ecdsa.PrivateKey, processID []byte, w *big.Int) (*WeightCredential, error) {
	key, err := WeightKey(electionBlindKey, w)
	if err != nil {
		return nil, err
	}
	weightPub := CompressedPubKey(key)
	msg, err := CertMessage(processID, w, weightPub)
	if err != nil {
		return nil, err
	}
	sig, err := SignEthereum(electionBlindKey, msg)
	if err != nil {
		return nil, fmt.Errorf("could not sign weight certificate: %w", err)
	}
	return &WeightCredential{Key: key, WeightKey: weightPub, Certificate: sig}, nil
}

// SignEthereum returns the 65-byte Ethereum personal-sign signature of msg by key, with the
// recovery byte in {0, 1}: the signature the Vochain recovers CSP keys from.
func SignEthereum(key *ecdsa.PrivateKey, msg []byte) ([]byte, error) {
	if key == nil {
		return nil, fmt.Errorf("nil key")
	}
	return ethcrypto.Sign(vocdonicrypto.Hash(msg), key)
}

// CompressedPubKey returns the 33-byte compressed public key of key.
func CompressedPubKey(key *ecdsa.PrivateKey) []byte {
	return ethcrypto.CompressPubkey(&key.PublicKey)
}

// NormalizeWeight turns a CSP-authorized weight as carried in a CA bundle (minimal big-endian
// bytes) into the integer the keys and certificates use. A missing or empty weight is 1, the
// Vochain's default when a bundle has no voteWeight. Zero and weights wider than 32 bytes are
// rejected, as the Vochain rejects them.
func NormalizeWeight(weight []byte) (*big.Int, error) {
	if len(weight) == 0 {
		return big.NewInt(1), nil
	}
	if len(weight) > WeightSize {
		return nil, fmt.Errorf("weight is %d bytes, at most %d allowed", len(weight), WeightSize)
	}
	w := new(big.Int).SetBytes(weight)
	if w.Sign() == 0 {
		return nil, fmt.Errorf("weight is zero")
	}
	return w, nil
}

// u256 encodes w as 32 big-endian bytes, rejecting nil, zero, negative or oversized weights.
func u256(w *big.Int) ([]byte, error) {
	if w == nil || w.Sign() <= 0 {
		return nil, fmt.Errorf("weight must be positive")
	}
	if w.BitLen() > 8*WeightSize {
		return nil, fmt.Errorf("weight does not fit in %d bytes", WeightSize)
	}
	return w.FillBytes(make([]byte, WeightSize)), nil
}

func scalarToKey(d *big.Int) (*ecdsa.PrivateKey, error) {
	key, err := ethcrypto.ToECDSA(d.FillBytes(make([]byte, RootSize)))
	if err != nil {
		return nil, fmt.Errorf("invalid derived scalar: %w", err)
	}
	return key, nil
}
