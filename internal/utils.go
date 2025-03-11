package internal

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"regexp"

	"github.com/nyaruka/phonenumbers"
	"go.vocdoni.io/dvote/log"
	"golang.org/x/crypto/argon2"
)

const (
	EmailRegexTemplate  = `^[\w.\+\.\-]+@([\w\-]+\.)+[\w]{2,}$`
	DefaultPhoneCountry = "ES"
)

var emailRegex = regexp.MustCompile(EmailRegexTemplate)

// ValidEmail helper function allows to validate an email address.
func ValidEmail(email string) bool {
	return emailRegex.MatchString(email)
}

// SanitizeAndVerifyEmail helper function allows to sanitize and verify a phone number.
func SanitizeAndVerifyPhoneNumber(phone string) (string, error) {
	pn, err := phonenumbers.Parse(phone, DefaultPhoneCountry)
	if err != nil {
		return "", fmt.Errorf("invalid phone number %s: %w", phone, err)
	}
	if !phonenumbers.IsValidNumber(pn) {
		return "", fmt.Errorf("invalid phone number %s", phone)
	}
	// Build the phone number string
	return fmt.Sprintf("+%d%d", pn.GetCountryCode(), pn.GetNationalNumber()), nil
}

// RandomInt returns a secure random integer in the range [0, max).
func RandomInt(max int) int {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		panic(err)
	}
	return int(n.Int64())
}

// RandomBytes helper function allows to generate a random byte slice of n bytes.
func RandomBytes(n int) []byte {
	b := make([]byte, n)
	_, err := rand.Read(b)
	if err != nil {
		panic(err)
	}
	return b
}

// RandomHex helper function allows to generate a random hex string of n bytes.
func RandomHex(n int) string {
	return fmt.Sprintf("%x", RandomBytes(n))
}

// HashPassword helper function allows to hash a password using a salt.
func HashPassword(salt, password string) []byte {
	return argon2hash([]byte(password), []byte(salt))
}

// HexHashPassword helper function allows to hash a password using a salt and
// return the result as a hex string.
func HexHashPassword(salt, password string) string {
	return hex.EncodeToString(HashPassword(salt, password))
}

// HashVerificationCode helper function allows to hash a verification code
// associated to the email of the user that requested it.
func HashVerificationCode(userEmail, code string) string {
	return hex.EncodeToString(sha256.New().Sum([]byte(userEmail + code)))
}

func HashOrgData(orgAddress, data string) []byte {
	var salt []byte
	hb := HexBytes{}
	if err := hb.FromString(orgAddress); err != nil {
		// This should never happened but if it does, let's try to keep going
		log.Warnw("invalid org address for hashing", "address", orgAddress)
		salt = []byte(orgAddress)
	} else {
		salt = hb.Bytes()
	}

	return argon2hash([]byte(data), salt)
}

func argon2hash(data, salt []byte) []byte {
	// Argon2 parameters for hashing, if modified, the current hashes will be invalidated
	memory := uint32(64 * 1024)
	argonTime := uint32(4)
	argonThreads := uint8(8)
	return argon2.IDKey([]byte(data), []byte(salt), argonTime, memory, argonThreads, 32)
}
