package internal

import (
	"fmt"
	"testing"

	"github.com/frankban/quicktest"
	"github.com/nyaruka/phonenumbers"
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
			got, err := SanitizeAndVerifyPhoneNumber(tt.phone)

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
		resultWithoutCC, err := SanitizeAndVerifyPhoneNumber(phoneWithoutCC)
		c.Assert(err, quicktest.IsNil)

		// Same phone number with country code
		phoneWithCC := fmt.Sprintf("+%d%s", defaultCC, phoneWithoutCC)
		resultWithCC, err := SanitizeAndVerifyPhoneNumber(phoneWithCC)
		c.Assert(err, quicktest.IsNil)

		// Both should result in the same sanitized number
		c.Assert(resultWithoutCC, quicktest.Equals, resultWithCC)
	})
}
