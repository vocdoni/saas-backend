package stripe

import (
	"sync"
	"time"
)

// MemoryEventStore is a simple in-memory implementation of EventStore
// In production, you'd want to use a persistent store like Redis or database
type MemoryEventStore struct {
	events map[string]time.Time
	mutex  sync.RWMutex
	ttl    time.Duration
}

// NewMemoryEventStore creates a new in-memory event store
func NewMemoryEventStore(ttl time.Duration) *MemoryEventStore {
	if ttl == 0 {
		ttl = 24 * time.Hour // Default TTL of 24 hours
	}

	store := &MemoryEventStore{
		events: make(map[string]time.Time),
		ttl:    ttl,
	}

	// Start cleanup goroutine
	go store.cleanup()

	return store
}

// EventExists checks if an event has already been processed
func (m *MemoryEventStore) EventExists(eventID string) bool {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	_, exists := m.events[eventID]
	return exists
}

// MarkProcessed marks an event as processed
func (m *MemoryEventStore) MarkProcessed(eventID string) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	m.events[eventID] = time.Now()
	return nil
}

// cleanup removes expired events periodically
func (m *MemoryEventStore) cleanup() {
	ticker := time.NewTicker(time.Hour) // Cleanup every hour
	defer ticker.Stop()

	for range ticker.C {
		m.mutex.Lock()
		now := time.Now()
		for eventID, timestamp := range m.events {
			if now.Sub(timestamp) > m.ttl {
				delete(m.events, eventID)
			}
		}
		m.mutex.Unlock()
	}
}

// Size returns the number of stored events (for monitoring/debugging)
func (m *MemoryEventStore) Size() int {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return len(m.events)
}
