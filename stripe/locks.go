package stripe

import (
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

// LockManager manages per-organization locks to prevent concurrent webhook processing
// for the same organization while allowing parallel processing for different organizations
type LockManager struct {
	locks sync.Map // map[string]*sync.Mutex
}

// NewLockManager creates a new lock manager
func NewLockManager() *LockManager {
	return &LockManager{}
}

// LockOrganization acquires a lock for the given organization address
// Returns a function that must be called to release the lock
func (lm *LockManager) LockOrganization(orgAddress common.Address) func() {
	// Get or create a mutex for this organization
	lockInterface, _ := lm.locks.LoadOrStore(orgAddress, &sync.Mutex{})
	lock, ok := lockInterface.(*sync.Mutex)
	if !ok {
		// This should never happen if we only store *sync.Mutex values
		panic("unexpected type in lock manager")
	}

	// Acquire the lock
	lock.Lock()

	// Return unlock function
	return func() {
		lock.Unlock()
	}
}

// CleanupLocks removes unused locks (optional optimization)
// This can be called periodically to prevent memory leaks from inactive organizations
func (lm *LockManager) CleanupLocks() {
	// Note: This is a simple implementation. In production, you might want to track
	// lock usage and only clean up locks that haven't been used recently
	lm.locks.Range(func(key, value any) bool {
		lock, ok := value.(*sync.Mutex)
		if !ok {
			// This should never happen if we only store *sync.Mutex values
			return true
		}
		// Try to acquire the lock without blocking
		if lock.TryLock() {
			// If we can acquire it, it's not in use, so we can remove it
			lock.Unlock()
			lm.locks.Delete(key)
		}
		return true
	})
}
