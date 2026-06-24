package stripe

import (
	"fmt"
	"os"
)

// Config holds the complete Stripe configuration.
//
// Plans are no longer enumerated here: the service discovers them dynamically from Stripe
// (the single source of truth) by listing active products that carry our plan metadata. See
// stripe.Service.GetPlansFromStripe.
type Config struct {
	APIKey        string `yaml:"api_key" json:"api_key"`
	WebhookSecret string `yaml:"webhook_secret" json:"webhook_secret"`
}

// NewConfig creates a new Stripe configuration from environment variables
func NewConfig() (*Config, error) {
	apiKey := os.Getenv("VOCDONI_STRIPEAPISECRET")
	if apiKey == "" {
		return nil, fmt.Errorf("VOCDONI_STRIPEAPISECRET environment variable is required")
	}

	webhookSecret := os.Getenv("VOCDONI_STRIPEWEBHOOKSECRET")
	if webhookSecret == "" {
		return nil, fmt.Errorf("VOCDONI_STRIPEWEBHOOKSECRET environment variable is required")
	}

	return &Config{
		APIKey:        apiKey,
		WebhookSecret: webhookSecret,
	}, nil
}
