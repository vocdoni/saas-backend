package stripe

import (
	"fmt"
	"os"
)

// PlanType represents the different subscription plan types
type PlanType string

const (
	PlanTypeEssential PlanType = "essential"
	PlanTypePremium   PlanType = "premium"
	PlanTypeFree      PlanType = "free"
	PlanTypeCustom    PlanType = "custom"
)

// ProductConfig holds Stripe product and price configuration
type ProductConfig struct {
	ProductID string `yaml:"product_id" json:"product_id"`
	PriceID   string `yaml:"price_id" json:"price_id"`
}

// Config holds the complete Stripe configuration
type Config struct {
	APIKey        string                     `yaml:"api_key" json:"api_key"`
	WebhookSecret string                     `yaml:"webhook_secret" json:"webhook_secret"`
	Products      map[PlanType]ProductConfig `yaml:"products" json:"products"`
}

// NewConfig creates a new Stripe configuration from environment variables
func NewConfig() (*Config, error) {
	apiKey := os.Getenv("STRIPE_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("STRIPE_API_KEY environment variable is required")
	}

	webhookSecret := os.Getenv("STRIPE_WEBHOOK_SECRET")
	if webhookSecret == "" {
		return nil, fmt.Errorf("STRIPE_WEBHOOK_SECRET environment variable is required")
	}

	// Default product configuration - can be overridden via environment
	products := map[PlanType]ProductConfig{
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
		Products:      products,
	}, nil
}

// GetProductConfig returns the product configuration for a given plan type
func (c *Config) GetProductConfig(planType PlanType) (ProductConfig, error) {
	config, exists := c.Products[planType]
	if !exists {
		return ProductConfig{}, fmt.Errorf("product configuration not found for plan type: %s", planType)
	}
	return config, nil
}

// GetAllProductIDs returns all configured product IDs
func (c *Config) GetAllProductIDs() []string {
	var productIDs []string
	for _, config := range c.Products {
		productIDs = append(productIDs, config.ProductID)
	}
	return productIDs
}

// getEnvOrDefault returns the environment variable value or a default value
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
