// Package stripe provides integration with the Stripe payment service,
// handling subscriptions, invoices, and webhook events.
//
// This file previously contained the old Stripe implementation.
// All functionality has been moved to the new service-based architecture:
// - stripe/service.go: Main business logic and webhook processing
// - stripe/client.go: Stripe API client wrapper
// - stripe/config.go: Configuration management
// - stripe/eventstore.go: Event idempotency handling
// - stripe/locks.go: Per-organization locking
// - stripe/errors.go: Error handling
//
// The new architecture provides:
// - Proper idempotency handling via MemoryEventStore
// - Per-organization locking instead of global mutex
// - Better error handling and logging
// - Repository pattern for database operations
// - Service layer separation of concerns
// - Improved testability and maintainability
package stripe
