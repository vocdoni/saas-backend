package validator

import (
	"regexp"

	"github.com/go-playground/validator/v10"
)

var (
	// phoneRegex is a regular expression to validate phone numbers.
	phoneRegex = regexp.MustCompile(`^\+[0-9\s\(\)\-]+$`)

	// hexColorRegex is a regular expression to validate hex color codes.
	hexColorRegex = regexp.MustCompile(`^#([A-Fa-f0-9]{6}|[A-Fa-f0-9]{3})$`)
)

// Validator is a wrapper around the go-playground/validator package.
type Validator struct {
	validator *validator.Validate
}

// New creates a new Validator instance.
func New() *Validator {
	v := validator.New()

	// Register custom validation functions
	_ = v.RegisterValidation("phone", validatePhone)
	_ = v.RegisterValidation("hexcolor", validateHexColor)

	return &Validator{
		validator: v,
	}
}

// Validate validates a struct using the validator package.
func (v *Validator) Validate(s interface{}) error {
	return v.validator.Struct(s)
}

// validatePhone validates a phone number.
func validatePhone(fl validator.FieldLevel) bool {
	// If the field is empty, it's valid (use required tag if it's required)
	if fl.Field().String() == "" {
		return true
	}

	// Use the pre-compiled regex for better performance
	return phoneRegex.MatchString(fl.Field().String())
}

// validateHexColor validates a hex color code.
func validateHexColor(fl validator.FieldLevel) bool {
	// If the field is empty, it's valid (use required tag if it's required)
	if fl.Field().String() == "" {
		return true
	}

	// Use the pre-compiled regex for better performance
	return hexColorRegex.MatchString(fl.Field().String())
}
