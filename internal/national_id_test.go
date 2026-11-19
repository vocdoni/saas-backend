package internal

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestCanonicalNationalID(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		ok             bool
	}{
		{"canonical DNI", "12345678Z", "12345678Z", true},
		{"lowercase letter", "12345678z", "12345678Z", true},
		{"spaces", " 1234 5678 z ", "12345678Z", true},
		{"tab and no-break space", "12345678\tZ\u00a0", "12345678Z", true},
		{"dots and hyphen", "12.345.678-Z", "12345678Z", true},
		{"underscore and slash", "12_345/678Z", "12345678Z", true},
		{"short digits padded", "1234567L", "01234567L", true},
		{"short digits with separator", "1234567-l", "01234567L", true},
		{"extra leading zero", "001234567L", "01234567L", true},
		{"extra leading zero, wrong letter", "001234567Z", "001234567Z", false},
		{"one digit", "1R", "00000001R", true},
		{"NIE", "X1234567L", "X1234567L", true},
		{"NIE extra leading zero", "X01234567L", "X1234567L", true},
		{"NIE short digits", "y-123-e", "Y0000123E", true},
		{"NIE with Z prefix", "Z1234567R", "Z1234567R", true},
		{"invalid letter", "12345678A", "12345678A", false},
		{"invalid NIE letter", "X1234567A", "X1234567A", false},
		{"too many digits", "123456789Z", "123456789Z", false},
		{"foreign ID", " P1234567 ", "P1234567", false},
		{"letterless DNI", "55555555", "55555555", false},
		{"letterless DNI with dots", "55.555.555", "55.555.555", false},
		{"letterless DNI of 7 digits", "1234567", "1234567", false},
		{"letterless NIE", "Y1234567", "Y1234567", false},
		{"letterless NIE lowercase", "x-1234567", "x-1234567", false},
		{"letterless short number", "123", "123", false},
		{"letterless 6 digits", "123456", "123456", false},
		{"letterless 9 digits", "123456789", "123456789", false},
		{"letterless short NIE", "X123456", "X123456", false},
		{"empty", "", "", false},
		{"blank", "   ", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := CanonicalNationalID(tc.in)
			qt.Assert(t, got, qt.Equals, tc.want)
			qt.Assert(t, ok, qt.Equals, tc.ok)
		})
	}
}
