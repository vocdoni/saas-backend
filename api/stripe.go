package api

// This file previously contained the old Stripe implementation.
// All Stripe functionality has been moved to the new service-based architecture
// in api/stripe_handlers.go and stripe/service.go.
//
// The old implementation has been removed to avoid confusion and ensure
// all Stripe operations use the new, more robust system with:
// - Proper idempotency handling via MemoryEventStore
// - Better error handling and logging
// - Repository pattern for database operations
// - Service layer separation of concerns
