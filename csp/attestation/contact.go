package attestation

import (
	"crypto/subtle"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/nyaruka/phonenumbers"
	"github.com/vocdoni/saas-backend/internal"
	"golang.org/x/text/unicode/norm"
)

// ContactKind is the channel a contact belongs to; it is part of the hashed preimage.
type ContactKind string

const (
	// ContactEmail is an email address, lowercase, trimmed and NFC-normalized.
	ContactEmail ContactKind = "email"
	// ContactPhone is a phone number in E.164.
	ContactPhone ContactKind = "phone"
	// SaltLen is the length of the salt the integrator draws for each attestation.
	SaltLen = 32
)

// ErrNonCanonicalContact reports a contact that is not in the canonical form of its kind. The
// message never names the contact.
var ErrNonCanonicalContact = fmt.Errorf("contact is not in canonical form")

// CheckCanonical reports whether contact is already in the canonical form of kind, without
// changing it: the hash covers the bytes as received, so the SaaS never re-normalizes them.
func CheckCanonical(kind ContactKind, contact string) error {
	switch kind {
	case ContactEmail:
		if !internal.ValidEmail(contact) || contact != norm.NFC.String(strings.ToLower(strings.TrimSpace(contact))) {
			return ErrNonCanonicalContact
		}
		return nil
	case ContactPhone:
		if !strings.HasPrefix(contact, "+") {
			return ErrNonCanonicalContact
		}
		pn, err := phonenumbers.Parse(contact, "ZZ")
		if err != nil || !phonenumbers.IsValidNumber(pn) || phonenumbers.Format(pn, phonenumbers.E164) != contact {
			return ErrNonCanonicalContact
		}
		return nil
	default:
		return ErrNonCanonicalContact
	}
}

// ContactHash returns keccak256(salt ‖ utf8(kind ‖ ":" ‖ contact)).
func ContactHash(salt []byte, kind ContactKind, contact string) common.Hash {
	return common.BytesToHash(ethcrypto.Keccak256(salt, []byte(string(kind)+":"+contact)))
}

// MatchContact reports, in constant time, whether salt and contact hash to want. A salt of the wrong
// length never matches.
func MatchContact(want common.Hash, salt []byte, kind ContactKind, contact string) bool {
	if len(salt) != SaltLen {
		return false
	}
	got := ContactHash(salt, kind, contact)
	return subtle.ConstantTimeCompare(got.Bytes(), want.Bytes()) == 1
}
