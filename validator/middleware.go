package validator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/vocdoni/saas-backend/errors"
	"go.vocdoni.io/dvote/log"
)

// keys for storing models in context
type (
	ModelKey          struct{}
	ValidatedModelKey struct{}
)

// ValidationError represents an individual validation error.
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ValidationErrors is a slice of ValidationError.
type ValidationErrors []ValidationError

// Error returns a string representation of the validation errors.
func (ve ValidationErrors) Error() string {
	var sb strings.Builder
	for i, err := range ve {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(fmt.Sprintf("%s: %s", err.Field, err.Message))
	}
	return sb.String()
}

// ValidateMiddleware is the legacy middleware that validates the request body
// against the provided model type.
func (v *Validator) ValidateMiddleware(model interface{}) func(next http.Handler) http.Handler {
	modelType := reflect.TypeOf(model)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Create a new instance of the model.
			instance := reflect.New(modelType).Interface()

			body, err := io.ReadAll(r.Body)
			if err != nil {
				errors.ErrMalformedBody.Write(w)
				return
			}
			// Restore the body for downstream handlers.
			r.Body = io.NopCloser(bytes.NewBuffer(body))

			if err := json.Unmarshal(body, instance); err != nil {
				errors.ErrMalformedBody.Write(w)
				return
			}

			if err := v.validator.Struct(instance); err != nil {
				var validationErrors ValidationErrors
				for _, fieldErr := range err.(validator.ValidationErrors) {
					validationErrors = append(validationErrors, ValidationError{
						Field:   fieldErr.Field(),
						Message: getErrorMessage(fieldErr),
					})
				}
				errors.ErrMalformedBody.WithErr(validationErrors).Write(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// AddModelMiddleware adds the provided model to the request context.
func (v *Validator) AddModelMiddleware(model interface{}) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), ModelKey{}, model)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// InputValidator validates the JSON request body against the model stored in the context.
// If successful, the validated instance is added to the context for downstream handlers.
func (v *Validator) InputValidator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only validate for methods that may have a body.
		if r.Method == http.MethodGet || r.Method == http.MethodHead ||
			r.Method == http.MethodOptions || r.Method == http.MethodDelete {
			next.ServeHTTP(w, r)
			return
		}

		// Ensure the Content-Type header indicates JSON.
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			next.ServeHTTP(w, r)
			return
		}

		// Retrieve the model from context.
		model, ok := r.Context().Value(ModelKey{}).(any)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		modelType := reflect.TypeOf(model)
		instance := reflect.New(modelType).Interface()

		body, err := io.ReadAll(r.Body)
		if err != nil {
			errors.ErrMalformedBody.Write(w)
			return
		}
		// Reset the body for downstream use.
		r.Body = io.NopCloser(bytes.NewBuffer(body))

		if err := json.Unmarshal(body, instance); err != nil {
			errors.ErrMalformedBody.Write(w)
			return
		}

		if err := v.validator.Struct(instance); err != nil {
			var validationErrors ValidationErrors
			for _, fieldErr := range err.(validator.ValidationErrors) {
				validationErrors = append(validationErrors, ValidationError{
					Field:   fieldErr.Field(),
					Message: getErrorMessage(fieldErr),
				})
			}
			log.Debugw("validation errors", "errors", validationErrors)
			errors.ErrMalformedBody.WithErr(validationErrors).Write(w)
			return
		}

		ctx := context.WithValue(r.Context(), ValidatedModelKey{}, instance)
		r.Body = io.NopCloser(bytes.NewBuffer(body))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetValidatedModel retrieves the validated model from the context.
func GetValidatedModel(ctx context.Context) (interface{}, bool) {
	model, ok := ctx.Value(ValidatedModelKey{}).(interface{})
	return model, ok
}

// getErrorMessage returns a human-readable error message for a validation error.
func getErrorMessage(err validator.FieldError) string {
	switch err.Tag() {
	case "required":
		return "This field is required"
	case "email":
		return "Invalid email format"
	case "min":
		return fmt.Sprintf("Must be at least %s characters long", err.Param())
	case "max":
		return fmt.Sprintf("Must be at most %s characters long", err.Param())
	case "url":
		return "Invalid URL format"
	case "phone":
		return "Invalid phone number format"
	case "hexcolor":
		return "Invalid hex color format (e.g. #FFF or #FFFFFF)"
	default:
		return fmt.Sprintf("Invalid value: %s", err.Tag())
	}
}
