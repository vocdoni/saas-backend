package internal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"regexp"

	"github.com/ethereum/go-ethereum/common"
	"github.com/nyaruka/phonenumbers"
	"golang.org/x/crypto/argon2"
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

// HashOrgData hashes organization data using the organization address as salt.
func HashOrgData(orgAddress common.Address, data string) []byte {
	return argon2hash([]byte(data), orgAddress.Bytes())
}

func argon2hash(data, salt []byte) []byte {
	// Argon2 parameters for hashing, if modified, the current hashes will be invalidated
	memory := uint32(64 * 1024)
	argonTime := uint32(4)
	argonThreads := uint8(8)
	return argon2.IDKey([]byte(data), []byte(salt), argonTime, memory, argonThreads, 32)
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
