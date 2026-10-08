package internal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"maps"
	"math/big"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/nyaruka/phonenumbers"
)

const (
	// EmailRegexTemplate is the regular expression used to validate email addresses.
	EmailRegexTemplate = `^[\w.\+\.\-]+@([\w\-]+\.)+[\w]{2,}$`
	// DefaultPhoneCountry is the default country code used for phone number validation.
	DefaultPhoneCountry = "ES"
)

var emailRegex = regexp.MustCompile(EmailRegexTemplate)

// ValidEmail helper function allows to validate an email address.
func ValidEmail(email string) bool {
	return emailRegex.MatchString(email)
}

// NormalizeEmail returns the canonical form of an email address used for
// storage and login matching: trimmed of surrounding whitespace and
// lowercased. Storing and comparing emails in this canonical form makes the
// CSP 2FA login case-insensitive.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// HashLoginFields returns the login-hash digest used to look up census
// participants during CSP authentication, over field name → value pairs.
// Fields are ordered by name and every name and value is length-prefixed, so
// distinct inputs never share an encoding: swapping two fields' values or
// moving characters between them changes the hash.
func HashLoginFields(fields map[string]string) []byte {
	var buf []byte
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		buf = binary.AppendUvarint(buf, uint64(len(name)))
		buf = append(buf, name...)
		buf = binary.AppendUvarint(buf, uint64(len(fields[name])))
		buf = append(buf, fields[name]...)
	}
	sum := sha256.Sum256(buf)
	return sum[:]
}

// SanitizeAndVerifyPhoneNumber helper function allows to sanitize and verify a phone number
// using a specific country code as the default for numbers without country codes.
// If country is the empty string, it falls back to internal.DefaultPhoneCountry
func SanitizeAndVerifyPhoneNumber(phone, country string) (string, error) {
	// Use default country if country is empty
	if country == "" {
		country = DefaultPhoneCountry
	}

	pn, err := phonenumbers.Parse(phone, country)
	if err != nil {
		return "", fmt.Errorf("invalid phone number %s: %w", phone, err)
	}
	if !phonenumbers.IsValidNumber(pn) {
		return "", fmt.Errorf("invalid phone number %s", phone)
	}
	// Build the phone number string
	return fmt.Sprintf("+%d%d", pn.GetCountryCode(), pn.GetNationalNumber()), nil
}

// RandomInt returns a secure random integer in the range [0, maxInt).
func RandomInt(maxInt int) int {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(maxInt)))
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

// SealToken encrypts a token using AES-GCM with a key derived from argon2hash.
// Returns the encrypted token (nonce + ciphertext).
func SealToken(token, email, secret string) ([]byte, error) {
	if token == "" || email == "" || secret == "" {
		return nil, fmt.Errorf("token, email, and secret cannot be empty")
	}

	// Derive encryption key using existing argon2hash function
	key := argon2hash([]byte(secret), []byte(email))

	// Create AES cipher (key is already 32 bytes from argon2hash)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	// Create GCM mode
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	// Generate random nonce using existing RandomBytes function
	nonce := RandomBytes(gcm.NonceSize())

	// Encrypt with email as additional data for binding
	ciphertext := gcm.Seal(nil, nonce, []byte(token), []byte(email))

	// Combine nonce + ciphertext
	sealedToken := append(nonce, ciphertext...)
	return sealedToken, nil
}

// OpenToken decrypts a token using AES-GCM with argon2hash.
// Takes the sealed token (nonce + ciphertext) and returns the original token as string.
func OpenToken(sealedToken []byte, email, secret string) (string, error) {
	if len(sealedToken) == 0 || email == "" || secret == "" {
		return "", fmt.Errorf("sealedToken, email, and secret cannot be empty")
	}

	// Derive the same encryption key using existing argon2hash
	key := argon2hash([]byte(secret), []byte(email))

	// Create AES cipher
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	// Create GCM mode
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	// Check minimum length (nonce + at least some ciphertext)
	nonceSize := gcm.NonceSize()
	if len(sealedToken) < nonceSize {
		return "", fmt.Errorf("invalid encrypted data: too short")
	}

	// Extract nonce and ciphertext
	nonce := sealedToken[:nonceSize]
	ciphertext := sealedToken[nonceSize:]

	// Decrypt with email as additional data
	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte(email))
	if err != nil {
		return "", fmt.Errorf("failed to decrypt token: %w", err)
	}

	return string(plaintext), nil
}

// RedactURL returns rawURL with any password replaced, so connection strings can be logged.
// A URL that can't be parsed is not echoed at all, since it may still contain credentials.
func RedactURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "<unparseable url>"
	}
	return u.Redacted()
}
