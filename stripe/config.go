package stripe

import (
	"fmt"
	"os"
	"strconv"
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

const FreeTrialDays = 15

// PlanConfig holds Stripe product and price configuration
type PlanConfig struct {
	ProductID string `yaml:"product_id" json:"product_id"`
}

// Config holds the complete Stripe configuration
type Config struct {
	APIKey        string                `yaml:"api_key" json:"api_key"`
	WebhookSecret string                `yaml:"webhook_secret" json:"webhook_secret"`
	Plans         [PlanCount]PlanConfig `yaml:"plans" json:"plans"`
	FreeTrialDays int                   `yaml:"free_trial_days" json:"free_trial_days"`
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

	freeTrialDays := FreeTrialDays
	if value := os.Getenv("VOCDONI_STRIPEFREETRIALDAYS"); value != "" {
		if days, err := strconv.Atoi(value); err == nil && days > 0 {
			freeTrialDays = days
		}
	}

	// Default plan configuration - can be overridden via environment
	plans := [PlanCount]PlanConfig{
		PlanTypeEssential: {
			ProductID: getEnvOrDefault("STRIPE_ESSENTIAL_PRODUCT_ID", "prod_T7otvdbt7MpK8f"),
		},
		PlanTypePremium: {
			ProductID: getEnvOrDefault("STRIPE_PREMIUM_PRODUCT_ID", "prod_T7p24o8zSFM26b"),
		},
		PlanTypeFree: {
			ProductID: getEnvOrDefault("STRIPE_FREE_PRODUCT_ID", "prod_T7p0GQJLxXDxZT"),
		},
		PlanTypeCustom: {
			ProductID: getEnvOrDefault("STRIPE_CUSTOM_PRODUCT_ID", "prod_T7pH46AnyE6ydC"),
		},
	}

	return &Config{
		APIKey:        apiKey,
		WebhookSecret: webhookSecret,
		Plans:         plans,
		FreeTrialDays: freeTrialDays,
	}, nil
}

// getEnvOrDefault returns the environment variable value or a default value
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
