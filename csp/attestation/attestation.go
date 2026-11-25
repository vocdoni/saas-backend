// Package attestation verifies the EIP-712 attestations an integrator signs for the voters of an
// attested census: the integrator vouches for a voter (an opaque voterId) and, when the census uses
// 2FA, for a salted hash of their contact. The SaaS checks the signature, the active signer, the
// process and the time window, then runs the 2FA itself.
package attestation

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

const (
	// DomainName is the EIP-712 domain name of every attestation.
	DomainName = "Vocdoni CSP attestation"
	// DomainVersion is the EIP-712 domain version of every attestation.
	DomainVersion = "1"
	// ProcessIDLen is the length of a SaaS process id (a Mongo ObjectID).
	ProcessIDLen = 12
	// SignatureLen is the length of an r ‖ s ‖ v signature.
	SignatureLen = 65
	// MaxClockSkew is how far in the past expiresAt may lie and still be accepted.
	MaxClockSkew = 30 * time.Second
	// MaxLifetime is how far in the future expiresAt may lie.
	MaxLifetime = time.Hour
)

var (
	// ErrInvalid reports an attestation whose fields or signature cannot be valid.
	ErrInvalid = fmt.Errorf("invalid attestation")
	// ErrExpired reports an attestation outside its accepted time window.
	ErrExpired = fmt.Errorf("attestation expired or too far in the future")

	domainTypeHash = ethcrypto.Keccak256(
		[]byte("EIP712Domain(string name,string version,address verifyingContract)"))
	attestationTypeHash = ethcrypto.Keccak256(
		[]byte("Attestation(bytes12 processId,bytes32 voterId,bytes32 contactHash,uint64 expiresAt)"))
	nameHash    = ethcrypto.Keccak256([]byte(DomainName))
	versionHash = ethcrypto.Keccak256([]byte(DomainVersion))
)

// Attestation is the message an integrator signs for one voter of one process.
type Attestation struct {
	ProcessID   [ProcessIDLen]byte
	VoterID     common.Hash
	ContactHash common.Hash
	ExpiresAt   uint64
}

// Domain is the EIP-712 domain of the attestations an environment accepts. VerifyingContract is the
// address of the SaaS CSP signer, so an attestation for one environment is worthless in another.
type Domain struct {
	VerifyingContract common.Address
}

// Separator returns the EIP-712 domain separator.
func (d Domain) Separator() common.Hash {
	return common.BytesToHash(ethcrypto.Keccak256(
		domainTypeHash,
		nameHash,
		versionHash,
		common.LeftPadBytes(d.VerifyingContract.Bytes(), 32),
	))
}

// StructHash returns the EIP-712 hashStruct of the attestation. bytes12 is right-padded and uint64
// left-padded to 32 bytes, as EIP-712 encodes them.
func (a *Attestation) StructHash() common.Hash {
	var processID, expiresAt [32]byte
	copy(processID[:], a.ProcessID[:])
	binary.BigEndian.PutUint64(expiresAt[24:], a.ExpiresAt)
	return common.BytesToHash(ethcrypto.Keccak256(
		attestationTypeHash,
		processID[:],
		a.VoterID.Bytes(),
		a.ContactHash.Bytes(),
		expiresAt[:],
	))
}

// Digest returns the EIP-712 digest the integrator signs: keccak256(0x19 0x01 ‖ domain ‖ struct).
func (d Domain) Digest(a *Attestation) common.Hash {
	return common.BytesToHash(ethcrypto.Keccak256(
		[]byte{0x19, 0x01},
		d.Separator().Bytes(),
		a.StructHash().Bytes(),
	))
}

// Recover checks the 65-byte r ‖ s ‖ v signature over the attestation and returns its signer and
// the signature in canonical form (low-s, v ∈ {27, 28}). It accepts v ∈ {0, 1, 27, 28} and
// refuses a high-s signature, so a signature has exactly one accepted encoding.
func (d Domain) Recover(a *Attestation, signature []byte) (common.Address, []byte, error) {
	if len(signature) != SignatureLen {
		return common.Address{}, nil, fmt.Errorf("%w: signature must be %d bytes", ErrInvalid, SignatureLen)
	}
	v := signature[64]
	if v >= 27 {
		v -= 27
	}
	if v > 1 {
		return common.Address{}, nil, fmt.Errorf("%w: signature v must be 0, 1, 27 or 28", ErrInvalid)
	}
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:64])
	// homestead rules refuse s > N/2, the malleable twin of a low-s signature
	if !ethcrypto.ValidateSignatureValues(v, r, s, true) {
		return common.Address{}, nil, fmt.Errorf("%w: signature values out of range or not low-s", ErrInvalid)
	}
	sig := make([]byte, SignatureLen)
	copy(sig, signature[:64])
	sig[64] = v
	pub, err := ethcrypto.SigToPub(d.Digest(a).Bytes(), sig)
	if err != nil {
		return common.Address{}, nil, fmt.Errorf("%w: cannot recover the signer", ErrInvalid)
	}
	signer := ethcrypto.PubkeyToAddress(*pub)
	if signer == (common.Address{}) {
		return common.Address{}, nil, fmt.Errorf("%w: zero signer", ErrInvalid)
	}
	sig[64] = v + 27
	return signer, sig, nil
}

// Verify recovers the signer of the attestation and checks it equals want, a non-zero address. It
// returns the canonical signature.
func (d Domain) Verify(a *Attestation, signature []byte, want common.Address) ([]byte, error) {
	if want == (common.Address{}) {
		return nil, fmt.Errorf("%w: no signer configured", ErrInvalid)
	}
	signer, canonical, err := d.Recover(a, signature)
	if err != nil {
		return nil, err
	}
	if signer != want {
		return nil, fmt.Errorf("%w: not signed by the active signer", ErrInvalid)
	}
	return canonical, nil
}

// CheckExpiry checks expiresAt lies in (now - MaxClockSkew, now + MaxLifetime].
func CheckExpiry(expiresAt uint64, now time.Time) error {
	lowerSec := now.Add(-MaxClockSkew).Unix()
	upperSec := now.Add(MaxLifetime).Unix()
	if lowerSec < 0 || expiresAt <= uint64(lowerSec) || expiresAt > uint64(upperSec) {
		return ErrExpired
	}
	return nil
}

// UserID derives the CSP user id of an attested voter: sha256("attested" ‖ processId ‖ voterId).
// The prefix keeps attested ids out of the key space of member ObjectIDs.
func UserID(processID [ProcessIDLen]byte, voterID common.Hash) []byte {
	preimage := make([]byte, 0, len("attested")+ProcessIDLen+common.HashLength)
	preimage = append(preimage, "attested"...)
	preimage = append(preimage, processID[:]...)
	preimage = append(preimage, voterID.Bytes()...)
	sum := sha256.Sum256(preimage)
	return sum[:]
}
