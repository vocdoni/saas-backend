package stripe

import (
	"fmt"
)

// StripeError represents a Stripe-specific error
type StripeError struct {
	Code    string
	Message string
	Type    string
	Err     error
}

func (e *StripeError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("stripe error [%s]: %s - %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("stripe error [%s]: %s", e.Code, e.Message)
}

func (e *StripeError) Unwrap() error {
	return e.Err
}

// Common Stripe errors
var (
	ErrInvalidEvent          = &StripeError{Code: "invalid_event", Message: "invalid webhook event"}
	ErrEventAlreadyProcessed = &StripeError{Code: "event_already_processed", Message: "webhook event already processed"}
	ErrOrganizationNotFound  = &StripeError{Code: "organization_not_found", Message: "organization not found"}
	ErrPlanNotFound          = &StripeError{Code: "plan_not_found", Message: "subscription plan not found"}
	ErrCustomerNotFound      = &StripeError{Code: "customer_not_found", Message: "stripe customer not found"}
	ErrSubscriptionNotFound  = &StripeError{Code: "subscription_not_found", Message: "stripe subscription not found"}
	ErrInvalidConfiguration  = &StripeError{Code: "invalid_configuration", Message: "invalid stripe configuration"}
	ErrAPICallFailed         = &StripeError{Code: "api_call_failed", Message: "stripe API call failed"}
	ErrWebhookValidation     = &StripeError{Code: "webhook_validation", Message: "webhook signature validation failed"}
)

// NewStripeError creates a new StripeError with the given code, message, and underlying error
func NewStripeError(code, message string, err error) *StripeError {
	return &StripeError{
		Code:    code,
		Message: message,
		Err:     err,
	}
}

// IsRetryableError determines if an error is retryable
func IsRetryableError(err error) bool {
	if stripeErr, ok := err.(*StripeError); ok {
		switch stripeErr.Code {
		case "api_call_failed", "rate_limit_error", "temporary_error":
			return true
		default:
			return false
		}
	}
	return false
}

// IsTemporaryError determines if an error is temporary
func IsTemporaryError(err error) bool {
	if stripeErr, ok := err.(*StripeError); ok {
		switch stripeErr.Code {
		case "rate_limit_error", "temporary_error", "api_connection_error":
			return true
		default:
			return false
		}
	}
	return false
}
