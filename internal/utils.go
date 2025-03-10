package internal

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"

	"github.com/nyaruka/phonenumbers"
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
	return sha256.New().Sum([]byte(salt + password))
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
	h := sha256.New()
	h.Write([]byte(orgAddress + data))
	return h.Sum(nil)
}
