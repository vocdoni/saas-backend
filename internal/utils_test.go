package internal

import (
	"fmt"
	"testing"

	"github.com/frankban/quicktest"
	"github.com/nyaruka/phonenumbers"
)

const (
	email  = "user@example.com"
	secret = "secret"
	token  = "123456"
)

func TestSanitizePhone(t *testing.T) {
	c := quicktest.New(t)
	defaultCC := phonenumbers.GetCountryCodeForRegion(DefaultPhoneCountry)

	tests := []struct {
		name    string
		phone   string
		want    string
		wantErr bool
	}{
		{
			name:  "valid spanish phone number without country code",
			phone: "623456789",
			want:  "+34623456789",
		},
		{
			name:  "valid spanish phone number with country code",
			phone: "+34623456789",
			want:  "+34623456789",
		},
		{
			name:  "valid spanish phone number with country code and spaces",
			phone: "+34 623 456 789",
			want:  "+34623456789",
		},
		{
			name:  "valid spanish phone number with country code as 0034",
			phone: "0034623456789",
			want:  "+34623456789",
		},
		{
			name:  "valid US phone number with country code",
			phone: "+12125552368",
			want:  "+12125552368",
		},
		{
			name:  "valid UK phone number with country code",
			phone: "+447911123456",
			want:  "+447911123456",
		},
		{
			name:    "invalid phone number (too short)",
			phone:   "12345",
			want:    "",
			wantErr: true,
		},
		{
			name:    "invalid phone number (contains letters)",
			phone:   "123abc456",
			want:    "",
			wantErr: true,
		},
		{
			name:    "empty phone number",
			phone:   "",
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		c.Run(tt.name, func(c *quicktest.C) {
			got, err := SanitizeAndVerifyPhoneNumber(tt.phone, "")

			if tt.wantErr {
				c.Assert(err, quicktest.IsNotNil)
			} else {
				c.Assert(err, quicktest.IsNil)
				c.Assert(got, quicktest.Equals, tt.want)
			}
		})
	}

	// Test that a number with and without the default country code results in the same output
	c.Run("same output with and without default country code", func(c *quicktest.C) {
		// Phone number without country code
		phoneWithoutCC := "623456789"
		resultWithoutCC, err := SanitizeAndVerifyPhoneNumber(phoneWithoutCC, "")
		c.Assert(err, quicktest.IsNil)

		// Same phone number with country code
		phoneWithCC := fmt.Sprintf("+%d%s", defaultCC, phoneWithoutCC)
		resultWithCC, err := SanitizeAndVerifyPhoneNumber(phoneWithCC, "")
		c.Assert(err, quicktest.IsNil)

		// Both should result in the same sanitized number
		c.Assert(resultWithoutCC, quicktest.Equals, resultWithCC)
	})
}

func TestEncryptDecryptToken(t *testing.T) {
	c := quicktest.New(t)

	tests := []struct {
		name    string
		token   string
		email   string
		secret  string
		wantErr bool
	}{
		{
			name:    "valid token encryption and decryption",
			token:   "123456",
			email:   "user@example.com",
			secret:  "my-secret-key",
			wantErr: false,
		},
		{
			name:    "valid long token",
			token:   "a1b2c3d4e5f6g7h8i9j0",
			email:   "test@test.com",
			secret:  "another-secret",
			wantErr: false,
		},
		{
			name:    "valid with special characters in email",
			token:   "987654",
			email:   "user+test@example.co.uk",
			secret:  "secret123",
			wantErr: false,
		},
		{
			name:    "empty token should fail",
			token:   "",
			email:   "user@example.com",
			secret:  "secret",
			wantErr: true,
		},
		{
			name:    "empty email should fail",
			token:   "123456",
			email:   "",
			secret:  "secret",
			wantErr: true,
		},
		{
			name:    "empty secret should fail",
			token:   "123456",
			email:   "user@example.com",
			secret:  "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		c.Run(tt.name, func(c *quicktest.C) {
			// Encrypt the token
			encrypted, err := SealToken(tt.token, tt.email, tt.secret)

			if tt.wantErr {
				c.Assert(err, quicktest.IsNotNil)
				return
			}

			c.Assert(err, quicktest.IsNil)
			c.Assert(encrypted, quicktest.Not(quicktest.Equals), "")
			c.Assert(encrypted, quicktest.Not(quicktest.Equals), tt.token)

			// Decrypt the token
			decrypted, err := OpenToken(encrypted, tt.email, tt.secret)
			c.Assert(err, quicktest.IsNil)
			c.Assert(decrypted, quicktest.Equals, tt.token)
		})
	}

	// Test that each encryption produces different output (due to random nonce)
	c.Run("different nonces produce different ciphertexts", func(c *quicktest.C) {
		encrypted1, err := SealToken(token, email, secret)
		c.Assert(err, quicktest.IsNil)

		encrypted2, err := SealToken(token, email, secret)
		c.Assert(err, quicktest.IsNil)

		// Even with same inputs, outputs should differ due to random nonce
		c.Assert(encrypted1, quicktest.Not(quicktest.Equals), encrypted2)

		// But both should decrypt to the same token
		decrypted1, err := OpenToken(encrypted1, email, secret)
		c.Assert(err, quicktest.IsNil)
		c.Assert(decrypted1, quicktest.Equals, token)

		decrypted2, err := OpenToken(encrypted2, email, secret)
		c.Assert(err, quicktest.IsNil)
		c.Assert(decrypted2, quicktest.Equals, token)
	})

	// Test that different secrets produce different results
	c.Run("different secrets produce different keys", func(c *quicktest.C) {
		encrypted1, err := SealToken(token, email, "secret1")
		c.Assert(err, quicktest.IsNil)

		encrypted2, err := SealToken(token, email, "secret2")
		c.Assert(err, quicktest.IsNil)

		// Different secrets should produce different outputs
		c.Assert(encrypted1, quicktest.Not(quicktest.Equals), encrypted2)

		// And trying to decrypt with wrong secret should fail
		_, err = OpenToken(encrypted1, email, "secret2")
		c.Assert(err, quicktest.IsNotNil)
	})

	// Test that different emails produce different results (email is part of key and AAD)
	c.Run("different emails produce different results", func(c *quicktest.C) {
		encrypted1, err := SealToken(token, "user1@example.com", secret)
		c.Assert(err, quicktest.IsNil)

		encrypted2, err := SealToken(token, "user2@example.com", secret)
		c.Assert(err, quicktest.IsNil)

		// Different emails should produce different outputs
		c.Assert(encrypted1, quicktest.Not(quicktest.Equals), encrypted2)

		// And trying to decrypt with wrong email should fail
		_, err = OpenToken(encrypted1, "user2@example.com", secret)
		c.Assert(err, quicktest.IsNotNil)
	})
}

func TestDecryptTokenFromHexErrors(t *testing.T) {
	c := quicktest.New(t)

	// Get a valid encrypted token for manipulation tests
	validEncrypted, err := SealToken(token, email, secret)
	c.Assert(err, quicktest.IsNil)

	tests := []struct {
		name       string
		sealedCode []byte
		email      string
		secret     string
		wantError  string
	}{
		{
			name:       "empty code",
			sealedCode: []byte{},
			email:      email,
			secret:     secret,
			wantError:  "sealedToken, email, and secret cannot be empty",
		},
		{
			name:       "empty email",
			sealedCode: validEncrypted,
			email:      "",
			secret:     secret,
			wantError:  "sealedToken, email, and secret cannot be empty",
		},
		{
			name:       "empty secret",
			sealedCode: validEncrypted,
			email:      email,
			secret:     "",
			wantError:  "sealedToken, email, and secret cannot be empty",
		},
		{
			name:       "hex too short",
			sealedCode: []byte{0x00, 0x01},
			email:      email,
			secret:     secret,
			wantError:  "invalid encrypted data: too short",
		},
		{
			name:       "wrong email",
			sealedCode: validEncrypted,
			email:      "wrong@example.com",
			secret:     secret,
			wantError:  "failed to decrypt token",
		},
		{
			name:       "wrong secret",
			sealedCode: validEncrypted,
			email:      email,
			secret:     "wrong-secret",
			wantError:  "failed to decrypt token",
		},
	}

	for _, tt := range tests {
		c.Run(tt.name, func(c *quicktest.C) {
			_, err := OpenToken(tt.sealedCode, tt.email, tt.secret)
			c.Assert(err, quicktest.IsNotNil)
			c.Assert(err.Error(), quicktest.Contains, tt.wantError)
		})
	}

	// Test tampering detection
	c.Run("tampered ciphertext is detected", func(c *quicktest.C) {
		encrypted, err := SealToken(token, email, secret)
		c.Assert(err, quicktest.IsNil)

		// Tamper with the last byte of the hex string
		copy(encrypted[:4], []byte{0xff, 0xff, 0xff, 0xff})

		_, err = OpenToken(encrypted, email, secret)
		c.Assert(err, quicktest.IsNotNil)
		c.Assert(err.Error(), quicktest.Contains, "failed to decrypt token")
	})
}
