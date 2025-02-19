package internal

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
)

const (
	EmailRegexTemplate       = `^[\w.\+\.\-]+@([\w\-]+\.)+[\w]{2,}$`
	PhoneNumberRegexTemplate = `^(?:\+|00)([1-9]\d{0,3})(?:[ -]?\d){4,14}$`
)

var (
	emailRegex       = regexp.MustCompile(EmailRegexTemplate)
	phoneNumberRegex = regexp.MustCompile(PhoneNumberRegexTemplate)
)

// ValidEmail helper function allows to validate an email address.
func ValidEmail(email string) bool {
	return emailRegex.MatchString(email)
}

// ValidPhoneNumber helper function allows to validate a phone number.
func ValidPhoneNumber(phoneNumber string) bool {
	return phoneNumberRegex.MatchString(phoneNumber)
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
