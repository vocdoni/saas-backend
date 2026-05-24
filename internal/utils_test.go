package internal

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/frankban/quicktest"
	"github.com/nyaruka/phonenumbers"
)

const (
	email  = "user@example.com"
	secret = "secret"
	token  = "123456"
)

func TestValidEmail(t *testing.T) {
	c := quicktest.New(t)

	tests := []struct {
		name  string
		email string
		want  bool
	}{
		{name: "simple valid", email: email, want: true},
		{name: "plus addressing", email: "user+tag@example.com", want: true},
		{name: "subdomain", email: "user@mail.example.com", want: true},
		{name: "dot in local part", email: "first.last@example.org", want: true},
		{name: "hyphen in domain", email: "user@my-domain.com", want: true},
		{name: "two-char TLD", email: "user@example.io", want: true},
		{name: "long TLD", email: "user@example.museum", want: true},
		{name: "mixed case", email: "User@Example.COM", want: true},
		{name: "country subdomain", email: "user+test@example.co.uk", want: true},
		{name: "no at sign", email: "notanemail", want: false},
		{name: "missing local part", email: "@example.com", want: false},
		{name: "missing domain", email: "user@", want: false},
		{name: "no TLD dot", email: "user@example", want: false},
		{name: "trailing dot in domain", email: "user@example.", want: false},
		{name: "single char TLD", email: "user@example.c", want: false},
		{name: "empty string", email: "", want: false},
		{name: "spaces in email", email: "user @example.com", want: false},
		{name: "double at sign", email: "user@@example.com", want: false},
	}

	for _, tt := range tests {
		c.Run(tt.name, func(c *quicktest.C) {
			got := ValidEmail(tt.email)
			c.Assert(got, quicktest.Equals, tt.want)
		})
	}
}

func TestRandomInt(t *testing.T) {
	c := quicktest.New(t)

	tests := []struct {
		name   string
		maxInt int
	}{
		{name: "small range", maxInt: 10},
		{name: "large range", maxInt: 1000000},
		{name: "range of one", maxInt: 1},
		{name: "range of two", maxInt: 2},
	}

	for _, tt := range tests {
		c.Run(tt.name, func(c *quicktest.C) {
			for range 50 {
				got := RandomInt(tt.maxInt)
				c.Assert(got >= 0, quicktest.IsTrue)
				c.Assert(got < tt.maxInt, quicktest.IsTrue)
			}
		})
	}
}

func TestRandomBytes(t *testing.T) {
	c := quicktest.New(t)

	tests := []struct {
		name string
		n    int
	}{
		{name: "zero bytes", n: 0},
		{name: "one byte", n: 1},
		{name: "small slice", n: 16},
		{name: "large slice", n: 256},
	}

	for _, tt := range tests {
		c.Run(tt.name, func(c *quicktest.C) {
			got := RandomBytes(tt.n)
			c.Assert(got, quicktest.HasLen, tt.n)
		})
	}

	c.Run("non-deterministic output", func(c *quicktest.C) {
		a := RandomBytes(32)
		b := RandomBytes(32)
		c.Assert(string(a) != string(b), quicktest.IsTrue)
	})
}

func TestRandomHex(t *testing.T) {
	c := quicktest.New(t)

	hexChars := regexp.MustCompile(`^[0-9a-f]*$`)

	tests := []struct {
		name string
		n    int
	}{
		{name: "zero bytes", n: 0},
		{name: "one byte", n: 1},
		{name: "eight bytes", n: 8},
		{name: "32 bytes", n: 32},
	}

	for _, tt := range tests {
		c.Run(tt.name, func(c *quicktest.C) {
			got := RandomHex(tt.n)
			c.Assert(got, quicktest.HasLen, tt.n*2)
			c.Assert(hexChars.MatchString(got), quicktest.IsTrue)
		})
	}
}

func TestHashPassword(t *testing.T) {
	c := quicktest.New(t)

	c.Run("deterministic output", func(c *quicktest.C) {
		h1 := HashPassword("salt", "password")
		h2 := HashPassword("salt", "password")
		c.Assert(h1, quicktest.DeepEquals, h2)
	})

	c.Run("output length is 32 bytes", func(c *quicktest.C) {
		h := HashPassword("salt", "password")
		c.Assert(h, quicktest.HasLen, 32)
	})

	c.Run("different salts produce different hashes", func(c *quicktest.C) {
		h1 := HashPassword("salt1", "password")
		h2 := HashPassword("salt2", "password")
		c.Assert(string(h1) != string(h2), quicktest.IsTrue)
	})

	c.Run("different passwords produce different hashes", func(c *quicktest.C) {
		h1 := HashPassword("salt", "password1")
		h2 := HashPassword("salt", "password2")
		c.Assert(string(h1) != string(h2), quicktest.IsTrue)
	})
}

func TestHexHashPassword(t *testing.T) {
	c := quicktest.New(t)

	hexChars := regexp.MustCompile(`^[0-9a-f]+$`)

	c.Run("output length is 64 chars", func(c *quicktest.C) {
		h := HexHashPassword("salt", "password")
		c.Assert(h, quicktest.HasLen, 64)
	})

	c.Run("valid hex encoding", func(c *quicktest.C) {
		h := HexHashPassword("salt", "password")
		c.Assert(hexChars.MatchString(h), quicktest.IsTrue)
	})

	c.Run("deterministic output", func(c *quicktest.C) {
		h1 := HexHashPassword("salt", "password")
		h2 := HexHashPassword("salt", "password")
		c.Assert(h1, quicktest.Equals, h2)
	})

	c.Run("consistent with HashPassword", func(c *quicktest.C) {
		raw := HashPassword("salt", "password")
		hex := HexHashPassword("salt", "password")
		c.Assert(hex, quicktest.HasLen, len(raw)*2)
	})
}

func TestHashVerificationCode(t *testing.T) {
	c := quicktest.New(t)

	hexChars := regexp.MustCompile(`^[0-9a-f]+$`)

	c.Run("deterministic output", func(c *quicktest.C) {
		h1 := HashVerificationCode(email, "123456")
		h2 := HashVerificationCode(email, "123456")
		c.Assert(h1, quicktest.Equals, h2)
	})

	c.Run("output is valid hex", func(c *quicktest.C) {
		h := HashVerificationCode(email, "123456")
		c.Assert(hexChars.MatchString(h), quicktest.IsTrue)
	})

	c.Run("output length encodes input plus 32-byte hash suffix", func(c *quicktest.C) {
		userEmail, code := email, "123456"
		h := HashVerificationCode(userEmail, code)
		// sha256.New().Sum(b) appends the sha256-of-empty (32 bytes) to b
		wantLen := (len(userEmail) + len(code) + 32) * 2
		c.Assert(h, quicktest.HasLen, wantLen)
	})

	c.Run("different emails produce different output", func(c *quicktest.C) {
		h1 := HashVerificationCode("alice@example.com", "123456")
		h2 := HashVerificationCode("bob@example.com", "123456")
		c.Assert(h1 != h2, quicktest.IsTrue)
	})

	c.Run("different codes produce different output", func(c *quicktest.C) {
		h1 := HashVerificationCode(email, "111111")
		h2 := HashVerificationCode(email, "222222")
		c.Assert(h1 != h2, quicktest.IsTrue)
	})
}

func TestHashOrgData(t *testing.T) {
	c := quicktest.New(t)

	addr1 := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	addr2 := common.HexToAddress("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")

	c.Run("output length is 32 bytes", func(c *quicktest.C) {
		h := HashOrgData(addr1, "some data")
		c.Assert(h, quicktest.HasLen, 32)
	})

	c.Run("deterministic output", func(c *quicktest.C) {
		h1 := HashOrgData(addr1, "some data")
		h2 := HashOrgData(addr1, "some data")
		c.Assert(h1, quicktest.DeepEquals, h2)
	})

	c.Run("different addresses produce different hashes", func(c *quicktest.C) {
		h1 := HashOrgData(addr1, "some data")
		h2 := HashOrgData(addr2, "some data")
		c.Assert(string(h1) != string(h2), quicktest.IsTrue)
	})

	c.Run("different data produces different hashes", func(c *quicktest.C) {
		h1 := HashOrgData(addr1, "data-a")
		h2 := HashOrgData(addr1, "data-b")
		c.Assert(string(h1) != string(h2), quicktest.IsTrue)
	})
}

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
