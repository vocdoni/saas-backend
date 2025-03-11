package validator

import (
	"testing"

	"github.com/go-playground/validator/v10"
)

// TestValidatePhone tests the phone number validator.
func TestValidatePhone(t *testing.T) {
	type TestStruct struct {
		Phone string `validate:"omitempty,phone"`
	}

	v := New()

	// Test valid phone numbers
	validPhones := []string{
		"+1234567890",
		"+1 (234) 567-890",
		"+44 20 7946 0958",
	}

	for _, phone := range validPhones {
		err := v.Validate(&TestStruct{Phone: phone})
		if err != nil {
			t.Errorf("Expected phone number %s to be valid, but got error: %v", phone, err)
		}
	}

	// Test invalid phone numbers
	invalidPhones := []string{
		"1234567890",     // Missing +
		"phone",          // Not a phone number
		"123-456-7890",   // Missing +
		"(123) 456-7890", // Missing +
		"#1234567890",    // Invalid character
	}

	for _, phone := range invalidPhones {
		err := v.Validate(&TestStruct{Phone: phone})
		if err == nil {
			t.Errorf("Expected phone number %s to be invalid, but it was valid", phone)
		}
	}

	// Test empty phone number (should be valid since we're not using required)
	err := v.Validate(&TestStruct{Phone: ""})
	if err != nil {
		t.Errorf("Expected empty phone number to be valid, but got error: %v", err)
	}
}

// TestValidateHexColor tests the hex color validator.
func TestValidateHexColor(t *testing.T) {
	type TestStruct struct {
		Color string `validate:"omitempty,hexcolor"`
	}

	v := New()

	// Test valid hex colors
	validColors := []string{
		"#FFF",
		"#FFFFFF",
		"#000",
		"#000000",
		"#123",
		"#123456",
		"#abc",
		"#ABC",
		"#abc123",
		"#ABC123",
	}

	for _, color := range validColors {
		err := v.Validate(&TestStruct{Color: color})
		if err != nil {
			t.Errorf("Expected color %s to be valid, but got error: %v", color, err)
		}
	}

	// Test invalid hex colors
	invalidColors := []string{
		"FFF",       // Missing #
		"FFFFFF",    // Missing #
		"#FFFF",     // Invalid length
		"#FFFFF",    // Invalid length
		"#FFFFFFF",  // Invalid length
		"#FFFFFFFF", // Invalid length
		"#FFG",      // Invalid character
		"#FFGFFF",   // Invalid character
		"red",       // Not a hex color
		"#red",      // Not a hex color
	}

	for _, color := range invalidColors {
		err := v.Validate(&TestStruct{Color: color})
		if err == nil {
			t.Errorf("Expected color %s to be invalid, but it was valid", color)
		}
	}

	// Test empty color (should be valid since we're not using required)
	err := v.Validate(&TestStruct{Color: ""})
	if err != nil {
		t.Errorf("Expected empty color to be valid, but got error: %v", err)
	}
}

// TestValidateRequired tests the required validator.
func TestValidateRequired(t *testing.T) {
	type TestStruct struct {
		Name string `validate:"required"`
	}

	v := New()

	// Test valid name
	err := v.Validate(&TestStruct{Name: "John"})
	if err != nil {
		t.Errorf("Expected name to be valid, but got error: %v", err)
	}

	// Test empty name
	err = v.Validate(&TestStruct{Name: ""})
	if err == nil {
		t.Errorf("Expected empty name to be invalid, but it was valid")
	}
}

// TestValidateEmail tests the email validator.
func TestValidateEmail(t *testing.T) {
	type TestStruct struct {
		Email string `validate:"omitempty,email"`
	}

	v := New()

	// Test valid emails
	validEmails := []string{
		"test@example.com",
		"test.test@example.com",
		"test+test@example.com",
		"test@example.co.uk",
		"test@example.io",
	}

	for _, email := range validEmails {
		err := v.Validate(&TestStruct{Email: email})
		if err != nil {
			t.Errorf("Expected email %s to be valid, but got error: %v", email, err)
		}
	}

	// Test invalid emails
	invalidEmails := []string{
		"test",
		"test@",
		"@example.com",
		"test@example",
		"test@.com",
		"test@example..com",
	}

	for _, email := range invalidEmails {
		err := v.Validate(&TestStruct{Email: email})
		if err == nil {
			t.Errorf("Expected email %s to be invalid, but it was valid", email)
		}
	}

	// Test empty email (should be valid since we're not using required)
	err := v.Validate(&TestStruct{Email: ""})
	if err != nil {
		t.Errorf("Expected empty email to be valid, but got error: %v", err)
	}
}

// TestValidateURL tests the URL validator.
func TestValidateURL(t *testing.T) {
	type TestStruct struct {
		URL string `validate:"omitempty,url"`
	}

	v := New()

	// Test valid URLs
	validURLs := []string{
		"http://example.com",
		"https://example.com",
		"http://www.example.com",
		"https://www.example.com",
		"http://example.com/path",
		"https://example.com/path",
		"http://example.com/path?query=value",
		"https://example.com/path?query=value",
		"http://example.com:8080",
		"https://example.com:8080",
	}

	for _, url := range validURLs {
		err := v.Validate(&TestStruct{URL: url})
		if err != nil {
			t.Errorf("Expected URL %s to be valid, but got error: %v", url, err)
		}
	}

	// Test invalid URLs
	invalidURLs := []string{
		"example.com",
		"www.example.com",
		"example",
		"http://",
		"https://",
		"http:/example.com",
		"https:/example.com",
		"http//example.com",
		"https//example.com",
	}

	for _, url := range invalidURLs {
		err := v.Validate(&TestStruct{URL: url})
		if err == nil {
			t.Errorf("Expected URL %s to be invalid, but it was valid", url)
		}
	}

	// Test empty URL (should be valid since we're not using required)
	err := v.Validate(&TestStruct{URL: ""})
	if err != nil {
		t.Errorf("Expected empty URL to be valid, but got error: %v", err)
	}
}

// TestValidateMin tests the min validator.
func TestValidateMin(t *testing.T) {
	type TestStruct struct {
		Password string `validate:"omitempty,min=8"`
	}

	v := New()

	// Test valid password
	err := v.Validate(&TestStruct{Password: "password123"})
	if err != nil {
		t.Errorf("Expected password to be valid, but got error: %v", err)
	}

	// Test invalid password
	err = v.Validate(&TestStruct{Password: "pass"})
	if err == nil {
		t.Errorf("Expected password to be invalid, but it was valid")
	}

	// Test empty password (should be valid since we're not using required)
	err = v.Validate(&TestStruct{Password: ""})
	if err != nil {
		t.Errorf("Expected empty password to be valid, but got error: %v", err)
	}
}

// TestValidateMax tests the max validator.
func TestValidateMax(t *testing.T) {
	type TestStruct struct {
		Name string `validate:"omitempty,max=10"`
	}

	v := New()

	// Test valid name
	err := v.Validate(&TestStruct{Name: "John"})
	if err != nil {
		t.Errorf("Expected name to be valid, but got error: %v", err)
	}

	// Test invalid name
	err = v.Validate(&TestStruct{Name: "John Doe Smith"})
	if err == nil {
		t.Errorf("Expected name to be invalid, but it was valid")
	}

	// Test empty name (should be valid since we're not using required)
	err = v.Validate(&TestStruct{Name: ""})
	if err != nil {
		t.Errorf("Expected empty name to be valid, but got error: %v", err)
	}
}

// TestValidateCombined tests combined validators.
func TestValidateCombined(t *testing.T) {
	type TestStruct struct {
		Name     string `validate:"required,max=10"`
		Email    string `validate:"required,email"`
		Password string `validate:"required,min=8"`
		Phone    string `validate:"omitempty,phone"`
		Color    string `validate:"omitempty,hexcolor"`
		Website  string `validate:"omitempty,url"`
	}

	v := New()

	// Test valid struct
	err := v.Validate(&TestStruct{
		Name:     "John",
		Email:    "john@example.com",
		Password: "password123",
		Phone:    "+1234567890",
		Color:    "#FFF",
		Website:  "https://example.com",
	})
	if err != nil {
		t.Errorf("Expected struct to be valid, but got error: %v", err)
	}

	// Test invalid struct
	err = v.Validate(&TestStruct{
		Name:     "",             // Required
		Email:    "john@example", // Invalid email
		Password: "pass",         // Too short
		Phone:    "1234567890",   // Missing +
		Color:    "red",          // Not a hex color
		Website:  "example.com",  // Not a URL
	})
	if err == nil {
		t.Errorf("Expected struct to be invalid, but it was valid")
	}

	// Check that we get the expected number of validation errors
	validationErrors, ok := err.(validator.ValidationErrors)
	if !ok {
		t.Errorf("Expected validator.ValidationErrors, but got %T", err)
	}
	if len(validationErrors) != 6 {
		t.Errorf("Expected 6 validation errors, but got %d", len(validationErrors))
	}
}
