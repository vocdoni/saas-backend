package stripe

import (
	"fmt"
	"os"
)

// PlanType represents the different subscription plan types
type PlanType int

const PlanCount = 5

const (
	PlanTypeNone = iota
	PlanTypeEssential
	PlanTypePremium
	PlanTypeFree
	PlanTypeCustom
)

// PlanConfig holds Stripe product and price configuration
type PlanConfig struct {
	ProductID string `yaml:"product_id" json:"product_id"`
	PriceID   string `yaml:"price_id" json:"price_id"`
}

// Config holds the complete Stripe configuration
type Config struct {
	APIKey        string                `yaml:"api_key" json:"api_key"`
	WebhookSecret string                `yaml:"webhook_secret" json:"webhook_secret"`
	Plans         [PlanCount]PlanConfig `yaml:"plans" json:"plans"`
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

	// Default plan configuration - can be overridden via environment
	plans := [PlanCount]PlanConfig{
		PlanTypeEssential: {
			ProductID: getEnvOrDefault("STRIPE_ESSENTIAL_PRODUCT_ID", "prod_R3LTVsjklmuQAL"),
			PriceID:   getEnvOrDefault("STRIPE_ESSENTIAL_PRICE_ID", "price_1QevgGDW6VLep8WGmICzD3mQ"),
		},
		PlanTypePremium: {
			ProductID: getEnvOrDefault("STRIPE_PREMIUM_PRODUCT_ID", "prod_R0kTryoMNl8I19"),
			PriceID:   getEnvOrDefault("STRIPE_PREMIUM_PRICE_ID", "price_1QevTjDW6VLep8WGk9tevmL8"),
		},
		PlanTypeFree: {
			ProductID: getEnvOrDefault("STRIPE_FREE_PRODUCT_ID", "prod_RFObcbvED7MYbz"),
			PriceID:   getEnvOrDefault("STRIPE_FREE_PRICE_ID", "price_1QMtoJDW6VLep8WGC2vsJ2CV"),
		},
		PlanTypeCustom: {
			ProductID: getEnvOrDefault("STRIPE_CUSTOM_PRODUCT_ID", "prod_RHurAb3OjkgJRy"),
			PriceID:   getEnvOrDefault("STRIPE_CUSTOM_PRICE_ID", "price_1QPL1VDW6VLep8WGKUf8A3BC"),
		},
	}

	return &Config{
		APIKey:        apiKey,
		WebhookSecret: webhookSecret,
		Plans:         plans,
	}, nil
}

// getEnvOrDefault returns the environment variable value or a default value
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
