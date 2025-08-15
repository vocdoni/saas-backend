package internal

import (
	"fmt"
	"testing"

	"github.com/frankban/quicktest"
	"github.com/nyaruka/phonenumbers"
)

func TestSanitizePhoneWithCountry(t *testing.T) {
	c := quicktest.New(t)
	defaultCC := phonenumbers.GetCountryCodeForRegion(DefaultPhoneCountry)

	tests := []struct {
		name    string
		phone   string
		country string
		want    string
		wantErr bool
	}{
		// Country-specific tests
		{
			name:    "US phone number without country code using US country",
			phone:   "2125552368",
			country: "US",
			want:    "+12125552368",
		},
		{
			name:    "UK phone number without country code using UK country",
			phone:   "7911123456",
			country: "GB",
			want:    "+447911123456",
		},
		{
			name:    "French phone number without country code using FR country",
			phone:   "123456789",
			country: "FR",
			want:    "+33123456789",
		},
		{
			name:    "Spanish phone number without country code using ES country",
			phone:   "623456789",
			country: "ES",
			want:    "+34623456789",
		},
		// Phone numbers with country codes (should ignore country parameter)
		{
			name:    "Spanish phone number with country code",
			phone:   "+34623456789",
			country: "US",
			want:    "+34623456789",
		},
		{
			name:    "Spanish phone number with country code and spaces",
			phone:   "+34 623 456 789",
			country: "US",
			want:    "+34623456789",
		},
		{
			name:    "US phone number with country code ignores country parameter",
			phone:   "+12125552368",
			country: "ES",
			want:    "+12125552368",
		},
		{
			name:    "UK phone number with country code ignores country parameter",
			phone:   "+447911123456",
			country: "US",
			want:    "+447911123456",
		},
		// Fallback scenarios
		{
			name:    "empty country code falls back to default",
			phone:   "623456789",
			country: "",
			want:    "+34623456789", // Should use ES default
		},
		// Error cases for invalid formats
		{
			name:    "Spanish phone number with country code as 0034 (invalid format)",
			phone:   "0034623456789",
			country: "US",
			want:    "",
			wantErr: true,
		},
		{
			name:    "invalid country code causes error",
			phone:   "623456789",
			country: "XX",
			want:    "",
			wantErr: true,
		},
		// Error cases
		{
			name:    "invalid phone number (too short)",
			phone:   "12345",
			country: "ES",
			want:    "",
			wantErr: true,
		},
		{
			name:    "invalid phone number (contains letters)",
			phone:   "123abc456",
			country: "ES",
			want:    "",
			wantErr: true,
		},
		{
			name:    "empty phone number",
			phone:   "",
			country: "US",
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		c.Run(tt.name, func(c *quicktest.C) {
			got, err := SanitizeAndVerifyPhoneNumber(tt.phone, tt.country)

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
		resultWithoutCC, err := SanitizeAndVerifyPhoneNumber(phoneWithoutCC, DefaultPhoneCountry)
		c.Assert(err, quicktest.IsNil)

		// Same phone number with country code
		phoneWithCC := fmt.Sprintf("+%d%s", defaultCC, phoneWithoutCC)
		resultWithCC, err := SanitizeAndVerifyPhoneNumber(phoneWithCC, DefaultPhoneCountry)
		c.Assert(err, quicktest.IsNil)

		// Both should result in the same sanitized number
		c.Assert(resultWithoutCC, quicktest.Equals, resultWithCC)
	})
}
